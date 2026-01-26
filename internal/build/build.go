package build

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/buildbuddy-io/reninja/internal/build_config"
	"github.com/buildbuddy-io/reninja/internal/build_log"
	"github.com/buildbuddy-io/reninja/internal/debug_flags"
	"github.com/buildbuddy-io/reninja/internal/dependency_scan"
	"github.com/buildbuddy-io/reninja/internal/depfile_parser"
	"github.com/buildbuddy-io/reninja/internal/deps_log"
	"github.com/buildbuddy-io/reninja/internal/disk"
	"github.com/buildbuddy-io/reninja/internal/dyndep"
	"github.com/buildbuddy-io/reninja/internal/dyndep_parser"
	"github.com/buildbuddy-io/reninja/internal/exit_status"
	"github.com/buildbuddy-io/reninja/internal/explanations"
	"github.com/buildbuddy-io/reninja/internal/graph"
	"github.com/buildbuddy-io/reninja/internal/jobserver"
	"github.com/buildbuddy-io/reninja/internal/metrics"
	"github.com/buildbuddy-io/reninja/internal/priority_queue"
	"github.com/buildbuddy-io/reninja/internal/span"
	"github.com/buildbuddy-io/reninja/internal/spawn"
	"github.com/buildbuddy-io/reninja/internal/state"
	"github.com/buildbuddy-io/reninja/internal/status"
	"github.com/buildbuddy-io/reninja/internal/timestamp"
	"github.com/buildbuddy-io/reninja/internal/util"
)

// Enumerate possible steps we want for an edge.
type Want int

const (
	// We do not want to build the edge, but we might want to build one of
	// its dependents.
	WantNothing = iota

	// We want to build the edge, but have not yet scheduled it.
	WantToStart

	// We want to build the edge, have scheduled it, and are waiting
	// for it to complete.
	WantToFinish
)

// Plan stores the state of a build plan: what we intend to build,
// which steps we're ready to execute.
type Plan struct {
	want    map[*graph.Edge]Want
	ready   *priority_queue.ThreadSafePriorityQueue[*graph.Edge]
	builder *Builder

	// user provided targets in build order, earlier one have higher priority
	targets []*graph.Node

	// Total number of edges that have commands (not phony).
	commandEdges int

	// Total remaining number of wanted edges.
	wantedEdges int
}

func NewPlan(builder *Builder) *Plan {
	return &Plan{
		want:    make(map[*graph.Edge]Want, 0),
		ready:   priority_queue.New[*graph.Edge](graph.EdgePriorityLess),
		builder: builder,
		targets: make([]*graph.Node, 0),
	}
}

// Add a target to our plan (including all its dependencies).
// Returns false if we don't need to build this target.
func (p *Plan) AddTarget(target *graph.Node) (bool, error) {
	p.targets = append(p.targets, target)
	b, err := p.AddSubTarget(target, nil, nil)
	return b, err
}

// Pop a ready edge off the queue of edges to build.
// Returns NULL if there's no work to do.
func (p *Plan) FindWork() *graph.Edge {
	if p.ready.Len() == 0 {
		return nil
	}

	work, ok := p.ready.Peek()
	if !ok {
		panic("priority queue not empty but peeked item was nil")
	}

	// If jobserver mode is enabled, try to acquire a token first,
	// and return null in case of failure.
	if p.builder != nil && p.builder.jobserver != nil {
		work.SetJobSlot(p.builder.jobserver.TryAcquire())
		if !work.JobSlot().Valid() {
			return nil
		}
	}
	realWork, ok := p.ready.Pop()
	if realWork != work {
		panic("peek returned different value than pop")
	}
	return work
}

// Returns true if there's more work to be done.
func (p *Plan) MoreToDo() bool {
	return p.wantedEdges > 0 && p.commandEdges > 0
}

// Dumps the current state of the plan.
func (p *Plan) Dump() {
	fmt.Printf("pending: %d\n", len(p.want))
	for e, w := range p.want {
		if w != WantNothing {
			fmt.Printf("want ")
		}
		e.Dump("")
	}
	fmt.Printf("ready: %d\n", p.ready.Len())
}

type EdgeResult int

const (
	EdgeFailed = iota
	EdgeSucceeded
)

func (p *Plan) EdgeFinished(edge *graph.Edge, result EdgeResult) (bool, error) {
	w, ok := p.want[edge]
	if !ok {
		panic("edge not found")
	}

	// See if this job frees up any delayed jobs.
	directlyWanted := w != WantNothing
	if directlyWanted {
		edge.Pool().EdgeFinished(edge)
	}
	edge.Pool().RetrieveReadyEdges(p.ready)

	// Release job slot if needed.
	if p.builder != nil && p.builder.jobserver != nil {
		p.builder.jobserver.Release(edge.JobSlot())
	}

	// The rest of this function only applies to successful commands.
	if result != EdgeSucceeded {
		return true, nil
	}

	if directlyWanted {
		p.wantedEdges -= 1
	}
	delete(p.want, edge)
	edge.SetOutputsReady(true)

	// Check off any nodes we were waiting for with this edge.
	for _, o := range edge.Outputs() {
		done, err := p.NodeFinished(o)
		if err != nil {
			return false, err
		}
		if !done {
			return false, nil
		}
	}
	return true, nil
}

func (p *Plan) CleanNode(scan *dependency_scan.DependencyScan, node *graph.Node) bool {
	node.SetDirty(false)

	for _, outEdge := range node.OutEdges() {
		// Don't process edges that we don't actually want.
		want, ok := p.want[outEdge]
		if !ok || want == WantNothing {
			continue
		}

		// Don't attempt to clean an edge if it failed to load deps.
		if outEdge.DepsMissing() {
			continue
		}

		// If all non-order-only inputs for this edge are now clean,
		// we might have changed the dirty state of the outputs.
		nonOrderOnlyInputs := outEdge.NonOrderOnlyInputs()
		dirtyFunc := func(i int) bool {
			return nonOrderOnlyInputs[i].Dirty()
		}
		if sort.Search(len(nonOrderOnlyInputs), dirtyFunc) == len(nonOrderOnlyInputs) {
			// Recompute most_recent_input.
			var mostRecentInput *graph.Node
			for _, node := range nonOrderOnlyInputs {
				if mostRecentInput == nil || node.Mtime() > mostRecentInput.Mtime() {
					mostRecentInput = node
				}
			}

			// Now, this edge is dirty if any of the outputs are dirty.
			// If the edge isn't dirty, clean the outputs and mark the edge as not
			// wanted.
			outputsDirty := false
			if !scan.RecomputeOutputsDirty(outEdge, mostRecentInput, &outputsDirty) {
				return false
			}

			if !outputsDirty {
				for _, o := range outEdge.Outputs() {
					if !p.CleanNode(scan, o) {
						return false
					}
				}

				p.want[outEdge] = WantNothing
				p.wantedEdges -= 1

				if !outEdge.IsPhony() {
					p.commandEdges -= 1
					if p.builder != nil {
						p.builder.status.EdgeRemovedFromPlan(outEdge)
					}
				}
			}
		}
	}
	return true
}

func (p *Plan) CommandEdgeCount() int {
	return p.commandEdges
}

func (p *Plan) Reset() {
	p.commandEdges = 0
	p.wantedEdges = 0
	for range p.ready.Len() {
		p.ready.Pop()
	}
	for k := range p.want {
		delete(p.want, k)
	}
}

func (p *Plan) PrepareQueue() {
	p.ComputeCriticalPath()
	p.ScheduleInitialEdges()
}

type oePair struct {
	first  *graph.Edge
	second *dyndep.Dyndeps
}

func (p *Plan) DyndepsLoaded(scan *dependency_scan.DependencyScan, node *graph.Node, ddf dyndep.DyndepFile) (bool, error) {
	// Recompute the dirty state of all our direct and indirect dependents now
	// that our dyndep information has been loaded.
	ok, err := p.RefreshDyndepDependents(scan, node)
	if err != nil || !ok {
		return ok, err
	}

	// We loaded dyndep information for those out_edges of the dyndep node that
	// specify the node in a dyndep binding, but they may not be in the plan.
	// Starting with those already in the plan, walk newly-reachable portion
	// of the graph through the dyndep-discovered dependencies.
	dyndepRoots := make([]oePair, 0)
	for edge, dyndeps := range ddf {
		// If the edge outputs are ready we do not need to consider it here.
		if edge.OutputsReady() {
			continue
		}

		// If the edge has not been encountered before then nothing already in the
		// plan depends on it so we do not need to consider the edge yet either.
		_, ok := p.want[edge]
		if !ok {
			continue
		}

		// This edge is already in the plan so queue it for the walk.
		dyndepRoots = append(dyndepRoots, oePair{first: edge, second: dyndeps})
	}

	// Walk dyndep-discovered portion of the graph to add it to the build plan.
	dyndepWalk := make(map[*graph.Edge]struct{}, 0)
	for _, oe := range dyndepRoots {
		for _, i := range oe.second.ImplicitInputs {
			added, err := p.AddSubTarget(i, oe.first.Outputs()[0], dyndepWalk)
			if err != nil || !added {
				return false, err
			}
		}
	}

	// Add out edges from this node that are in the plan (just as
	// Plan::NodeFinished would have without taking the dyndep code path).
	for _, outEdge := range node.OutEdges() {
		if _, ok := p.want[outEdge]; ok {
			dyndepWalk[outEdge] = struct{}{}
		}
	}

	// See if any encountered edges are now ready.
	for wi := range dyndepWalk {
		want, ok := p.want[wi]
		if !ok {
			continue
		}
		if ready, err := p.EdgeMaybeReady(wi, want); err != nil || !ready {
			return false, err
		}
	}

	return true, nil
}

// Heuristic for edge priority weighting.
// Phony edges are free (0 cost), all other edges are weighted equally.
func EdgeWeightHeuristic(edge *graph.Edge) int64 {
	if edge.IsPhony() {
		return 0
	}
	return 1
}

func (p *Plan) ComputeCriticalPath() {
	defer metrics.Record("ComputeCriticalPath")()
	topoSort := NewTopoSort()
	for _, target := range p.targets {
		topoSort.VisitTarget(target)
	}
	sortedEdges := topoSort.Results()

	// First, reset all weights to 1.
	for _, edge := range sortedEdges {
		edge.SetCriticalPathWeight(EdgeWeightHeuristic(edge))
	}

	// Second propagate / increment weights from
	// children to parents. Scan the list
	// in reverse order to do so.
	for i := len(sortedEdges) - 1; i >= 0; i-- {
		edge := sortedEdges[i]
		edgeWeight := edge.CriticalPathWeight()

		for _, input := range edge.Inputs() {
			producer := input.InEdge()
			if producer == nil {
				continue
			}

			producerWeight := producer.CriticalPathWeight()
			candidateWeight := edgeWeight + EdgeWeightHeuristic(producer)
			if candidateWeight > producerWeight {
				producer.SetCriticalPathWeight(candidateWeight)
			}
		}
	}
}
func (p *Plan) RefreshDyndepDependents(scan *dependency_scan.DependencyScan, node *graph.Node) (bool, error) {
	// Collect the transitive closure of dependents and mark their edges
	// as not yet visited by RecomputeDirty.
	dependents := make(map[*graph.Node]struct{}, 0)
	p.UnmarkDependents(node, dependents)

	// Update the dirty state of all dependents and check if their edges
	// have become wanted.
	for n := range dependents {
		// Check if this dependent node is now dirty. Also checks for new cycles.
		validationNodes, err := scan.RecomputeDirty(n, nil)
		if err != nil {
			return false, err
		}

		// Add any validation nodes found during RecomputeDirty as new top level
		// targets.
		for _, v := range validationNodes {
			if inEdge := v.InEdge(); inEdge != nil {
				if !inEdge.OutputsReady() {
					added, err := p.AddTarget(v)
					if err != nil || !added {
						return added, err
					}
				}
			}
		}
		if !n.Dirty() {
			continue
		}

		// This edge was encountered before.  However, we may not have wanted to
		// build it if the outputs were not known to be dirty.  With dyndep
		// information an output is now known to be dirty, so we want the edge.
		edge := n.InEdge()
		if edge == nil || edge.OutputsReady() {
			panic("already encountered edge not in expected state")
		}
		want, ok := p.want[edge]
		if !ok {
			panic("edge should be wanted")
		}
		if want == WantNothing {
			p.want[edge] = WantToStart
			p.EdgeWanted(edge)
		}
	}
	return true, nil

}
func (p *Plan) UnmarkDependents(node *graph.Node, dependents map[*graph.Node]struct{}) {
	for _, edge := range node.OutEdges() {
		if _, ok := p.want[edge]; !ok {
			continue
		}

		if edge.Mark() != graph.VisitNone {
			edge.SetMark(graph.VisitNone)
			for _, o := range edge.Outputs() {
				if _, ok := dependents[o]; !ok {
					dependents[o] = struct{}{}
					p.UnmarkDependents(o, dependents)
				}
			}
		}
	}
}

func (p *Plan) AddSubTarget(node, dependent *graph.Node, dyndepWalk map[*graph.Edge]struct{}) (bool, error) {
	edge := node.InEdge()
	if edge == nil {
		// Leaf node, this can be either a regular input from the manifest
		// (e.g. a source file), or an implicit input from a depfile or dyndep
		// file. In the first case, a dirty flag means the file is missing,
		// and the build should stop. In the second, do not do anything here
		// since there is no producing edge to add to the plan.
		var err error
		if node.Dirty() && !node.GeneratedByDepLoader() {
			var referenced string
			if dependent != nil {
				referenced = fmt.Sprintf(", needed by '%s',", dependent.Path())
			}
			errMsg := fmt.Sprintf("'%s'", node.Path()) + referenced + " missing and no known rule to make it"
			err = fmt.Errorf("%s", errMsg)
		}
		return false, err

	}

	if edge.OutputsReady() {
		return false, nil // Don't need to do anything.
	}

	// If an entry in want_ does not already exist for edge, create an entry which
	// maps to kWantNothing, indicating that we do not want to build this entry itself.
	want, ok := p.want[edge]
	if !ok {
		p.want[edge] = WantNothing
		want = WantNothing
	}
	if dyndepWalk != nil && want == WantToFinish {
		return false, nil // Don't need to do anything with already-scheduled edge.
	}

	// If we do need to build edge and we haven't already marked it as wanted,
	// mark it now.
	if node.Dirty() && want == WantNothing {
		p.want[edge] = WantToStart
		p.EdgeWanted(edge)
	}

	if dyndepWalk != nil {
		dyndepWalk[edge] = struct{}{}
	}

	if ok {
		return true, nil // We've already processed the inputs.
	}

	for _, input := range edge.Inputs() {
		added, err := p.AddSubTarget(input, node, dyndepWalk)
		if !added && err != nil {
			return false, err
		}
	}
	return true, nil
}

type TopoSort struct {
	visitedSet  map[*graph.Edge]struct{}
	sortedEdges []*graph.Edge
}

func NewTopoSort() *TopoSort {
	return &TopoSort{
		visitedSet:  make(map[*graph.Edge]struct{}),
		sortedEdges: make([]*graph.Edge, 0),
	}
}

func (s *TopoSort) VisitTarget(target *graph.Node) {
	producer := target.InEdge()
	if producer != nil {
		s.Visit(producer)
	}
}
func (s *TopoSort) Results() []*graph.Edge {
	return s.sortedEdges
}

func (s *TopoSort) Visit(edge *graph.Edge) {
	if _, ok := s.visitedSet[edge]; ok {
		return
	}
	s.visitedSet[edge] = struct{}{}

	for _, input := range edge.Inputs() {
		producer := input.InEdge()
		if producer != nil {
			s.Visit(producer)
		}
	}
	s.sortedEdges = append(s.sortedEdges, edge)
}

func (p *Plan) ScheduleInitialEdges() {
	if p.ready.Len() != 0 {
		panic("priority queue should begin empty")
	}
	pools := make(map[*graph.Pool]struct{})
	for edge, w := range p.want {
		if w == WantToStart && edge.AllInputsReady() {
			pool := edge.Pool()
			if pool.ShouldDelayEdge() {
				pool.DelayEdge(edge)
				pools[pool] = struct{}{}
			} else {
				p.ScheduleWork(edge, w)
			}
		}
	}

	// Call RetrieveReadyEdges only once at the end so higher priority
	// edges are retrieved first, not the ones that happen to be first
	// in the want_ map.
	for pool := range pools {
		pool.RetrieveReadyEdges(p.ready)
	}
}

func (p *Plan) NodeFinished(node *graph.Node) (bool, error) {
	if node.DyndepPending() {
		if p.builder == nil {
			panic("dyndep requires Plan to have a Builder")
		}
		// Load the now-clean dyndep file. This will also update the
		// build plan and schedule any new work that is ready.
		if err := p.builder.LoadDyndeps(node); err != nil {
			return false, err
		}
		return true, nil
	}

	for _, outEdge := range node.OutEdges() {
		want, ok := p.want[outEdge]
		if !ok {
			continue
		}

		if ready, err := p.EdgeMaybeReady(outEdge, want); err != nil || !ready {
			return false, err
		}
	}
	return true, nil
}

func (p *Plan) EdgeWanted(edge *graph.Edge) {
	p.wantedEdges += 1
	if !edge.IsPhony() {
		p.commandEdges += 1
		if p.builder != nil {
			p.builder.status.EdgeAddedToPlan(edge)
		}
	}
}

func (p *Plan) EdgeMaybeReady(edge *graph.Edge, want Want) (bool, error) {
	if edge.AllInputsReady() {
		if want != WantNothing {
			p.ScheduleWork(edge, want)
		} else {
			// We do not need to build this edge, but we might need to build one of
			// its dependents.
			done, err := p.EdgeFinished(edge, EdgeSucceeded)
			if err != nil || !done {
				return false, err
			}
		}
	}
	return true, nil
}

func (p *Plan) ScheduleWork(edge *graph.Edge, want Want) {
	if want == WantToFinish {
		// This edge has already been scheduled.  We can get here again if an edge
		// and one of its dependencies share an order-only input, or if a node
		// duplicates an out edge (see https://github.com/ninja-build/ninja/pull/519).
		// Avoid scheduling the work again.
		return
	}

	if want != WantToStart {
		panic("cannot schedule work; not in state Want-to-start")
	}
	p.want[edge] = WantToFinish

	pool := edge.Pool()
	if pool.ShouldDelayEdge() {
		pool.DelayEdge(edge)
		pool.RetrieveReadyEdges(p.ready)
	} else {
		pool.EdgeScheduled(edge)
		p.ready.Push(edge)
	}
}

type RunningEdgeMap = map[*graph.Edge]time.Time

type Builder struct {
	state         *state.State
	config        *build_config.Config
	plan          *Plan
	jobserver     jobserver.Client
	commandRunner CommandRunner
	status        status.Status

	runningEdges  RunningEdgeMap
	startTime     time.Time
	lockFilePath  string
	diskInterface disk.Interface
	explanations  *explanations.OptionalExplanations

	scan     *dependency_scan.DependencyScan
	exitCode exit_status.ExitStatusType
}

func NewBuilder(state *state.State, config *build_config.Config, buildLog *build_log.BuildLog,
	depsLog *deps_log.DepsLog, diskInterface disk.Interface, status status.Status, startTimeMillis int64) *Builder {
	b := &Builder{
		state:         state,
		config:        config,
		status:        status,
		startTime:     time.UnixMilli(startTimeMillis),
		diskInterface: diskInterface,
		runningEdges:  make(RunningEdgeMap, 0),
	}
	b.plan = NewPlan(b)

	var realExp *explanations.Explanations
	if debug_flags.Explaining {
		realExp = explanations.New()
	}
	b.explanations = explanations.NewOptional(realExp)
	status.SetExplanations(realExp)

	lockFilePath := ".ninja_lock"
	if buildDir := state.Bindings().LookupVariable("builddir"); buildDir != "" {
		lockFilePath = buildDir + "/" + lockFilePath
	}
	b.lockFilePath = lockFilePath

	b.scan = dependency_scan.New(state, buildLog, depsLog, diskInterface, config.DepfileParserOptions, realExp)
	return b
}
func (b *Builder) Close() {
	b.Cleanup()
	b.explanations = explanations.NewOptional(nil)
}

func (b *Builder) Cleanup() {
	if b.commandRunner != nil {
		activeEdges := b.commandRunner.GetActiveEdges()
		b.commandRunner.Abort()

		for _, e := range activeEdges {
			depfile := e.GetUnescapedDepfile()
			for _, o := range e.Outputs() {
				// Only delete this output if it was actually modified.  This is
				// important for things like the generator where we don't want to
				// delete the manifest file if we can avoid it.  But if the rule
				// uses a depfile, always delete.  (Consider the case where we
				// need to rebuild an output because of a modified header file
				// mentioned in a depfile, and the command touches its depfile
				// but is interrupted before it touches its output file.)
				newMtime, err := b.diskInterface.Stat(o.Path())
				if newMtime == -1 { // Log and ignore Stat() errors.
					b.status.Error("%s", err)
				}
				if depfile != "" || o.Mtime() != newMtime {
					b.diskInterface.RemoveFile(o.Path())
				}
			}
			if depfile != "" {
				b.diskInterface.RemoveFile(depfile)
			}
		}
	}

	if lockfileMtime, err := b.diskInterface.Stat(b.lockFilePath); err == nil && lockfileMtime > 0 {
		b.diskInterface.RemoveFile(b.lockFilePath)
	}
}

func (b *Builder) SetJobserverClient(jobserver jobserver.Client) {
	b.jobserver = jobserver
}

func (b *Builder) AddTargetByName(name string) (*graph.Node, error) {
	node := b.state.LookupNode(name)
	if node == nil {
		return nil, fmt.Errorf("unknown target: '%s'", name)
	}
	added, err := b.AddTarget(node)
	if err != nil || !added {
		return nil, err
	}
	return node, nil
}

func (b *Builder) AddTarget(target *graph.Node) (bool, error) {
	validationNodes, err := b.scan.RecomputeDirty(target, nil)
	if err != nil {
		return false, err
	}
	inEdge := target.InEdge()
	if inEdge == nil || !inEdge.OutputsReady() {
		added, err := b.plan.AddTarget(target)
		if err != nil || !added {
			return false, err
		}
	}

	// Also add any validation nodes found during RecomputeDirty as top level
	// targets.
	for _, n := range validationNodes {
		if validationInEdge := n.InEdge(); validationInEdge != nil {
			if outputsReady := validationInEdge.OutputsReady(); !outputsReady {
				added, err := b.plan.AddTarget(n)
				if err != nil || !added {
					return false, err
				}
			}
		}
	}
	return true, nil
}

func (b *Builder) AlreadyUpToDate() bool {
	return !b.plan.MoreToDo()
}

func (b *Builder) Build() (exit_status.ExitStatusType, error) {
	if b.AlreadyUpToDate() {
		panic("already up to date!")
	}
	b.plan.PrepareQueue()

	pendingCommands := 0
	failuresAllowed := b.config.FailuresAllowed

	// Set up the command runner if we haven't done so already.
	// TODO(tylerw): why is this happening here? should have happened in New??
	if b.commandRunner == nil {
		if b.config.DryRun {
			b.commandRunner = NewDryCommandRunner()
		} else {
			b.commandRunner = NewRealCommandRunner(b.config, b.jobserver)
		}
	}

	// We are about to start the build process.
	b.status.BuildStarted(b.startTime)

	// This main loop runs the entire build process.
	// It is structured like this:
	// First, we attempt to start as many commands as allowed by the
	// command runner.
	// Second, we attempt to wait for / reap the next finished command.
	for b.plan.MoreToDo() {
		// See if we can start any more commands.
		if failuresAllowed > 0 {
			capacity := b.commandRunner.CanRunMore()
			for capacity > 0 {
				edge := b.plan.FindWork()
				if edge == nil {
					break
				}

				if edge.GetBindingBool("generator") {
					b.scan.BuildLog().Close()
				}

				if started, err := b.StartEdge(edge); err != nil || !started {
					b.Cleanup()
					b.status.BuildFinished()
					return exit_status.ExitFailure, err
				}

				if edge.IsPhony() {
					finished, err := b.plan.EdgeFinished(edge, EdgeSucceeded)
					if err != nil || !finished {
						b.Cleanup()
						b.status.BuildFinished()
						return exit_status.ExitFailure, err
					}
				} else {
					pendingCommands += 1
					capacity -= 1

					// Re-evaluate capacity.
					currentCapacity := b.commandRunner.CanRunMore()
					if currentCapacity < capacity {
						capacity = currentCapacity
					}
				}
			}

			// We are finished with all work items and have no pending
			// commands. Therefore, break out of the main loop.
			if pendingCommands == 0 && !b.plan.MoreToDo() {
				break
			}
		}

		// See if we can reap any finished commands.
		if pendingCommands > 0 {
			result := b.commandRunner.WaitForCommand()
			if result == nil || result.Status == exit_status.ExitInterrupted {
				b.Cleanup()
				b.status.BuildFinished()
				return exit_status.ExitInterrupted, fmt.Errorf("interrupted by user")
			}

			pendingCommands -= 1

			commandFinished, err := b.FinishCommand(result)
			b.SetFailureCode(result.Status)
			if !commandFinished {
				b.Cleanup()
				b.status.BuildFinished()
				if result.Success() {
					// If the command pretend succeeded, the status wasn't set to a proper exit code,
					// so we set it to ExitFailure.
					result.Status = exit_status.ExitFailure
					b.SetFailureCode(result.Status)
				}
				return result.Status, err
			}

			if !result.Success() {
				if failuresAllowed > 0 {
					failuresAllowed -= 1
				}
			}

			// We made some progress; start the main loop over.
			continue
		}

		// If we get here, we cannot make any more progress.
		b.status.BuildFinished()

		var err error
		if failuresAllowed == 0 {
			if b.config.FailuresAllowed > 1 {
				err = fmt.Errorf("subcommands failed")
			} else {
				err = fmt.Errorf("subcommand failed")
			}
		} else if failuresAllowed < b.config.FailuresAllowed {
			err = fmt.Errorf("cannot make progress due to previous errors")
		} else {
			err = fmt.Errorf("stuck [this is a bug]")
		}

		return b.GetExitCode(), err
	}

	if cacher, ok := b.commandRunner.(CachingCommandRunner); ok {
		if err := cacher.WaitForUploads(); err != nil {
			return exit_status.ExitFailure, err
		}
	}

	b.status.BuildFinished()
	return exit_status.ExitSuccess, nil
}

func (b *Builder) StartEdge(edge *graph.Edge) (bool, error) {
	defer metrics.Record("StartEdge")()
	if edge.IsPhony() {
		return true, nil
	}
	startTime := time.Now()
	b.runningEdges[edge] = startTime
	b.status.BuildEdgeStarted(edge, startTime)

	var buildStart timestamp.TimeStamp
	if b.config.DryRun {
		buildStart = 0
	} else {
		buildStart = -1
	}

	// Create directories necessary for outputs and remember the current
	// filesystem mtime to record later
	// XXX: this will block; do we care?
	for _, o := range edge.Outputs() {
		if err := b.diskInterface.MakeDirs(o.Path()); err != nil {
			return false, err
		}
		if buildStart == -1 {
			b.diskInterface.WriteFile(b.lockFilePath, []byte{}, false)
			buildStart, _ = b.diskInterface.Stat(b.lockFilePath)
			if buildStart == -1 {
				buildStart = 0
			}
		}
	}

	edge.SetCommandStartTime(buildStart)

	// Create depfile directory if needed.
	// XXX: this may also block; do we care?
	depfile := edge.GetUnescapedDepfile()
	if depfile != "" {
		if err := b.diskInterface.MakeDirs(depfile); err != nil {
			return false, err
		}
	}

	// Create response file, if needed
	// XXX: this may also block; do we care?
	rspFile := edge.GetUnescapedRspfile()
	if rspFile != "" {
		content := edge.GetBinding("rspfile_content")
		if err := b.diskInterface.WriteFile(rspFile, []byte(content), true); err != nil {
			return false, err
		}
	}

	// start command computing and run it
	if err := b.commandRunner.StartCommand(edge); err != nil {
		return false, fmt.Errorf("command '%s' failed: %w", edge.EvaluateCommand(false), err)
	}

	return true, nil
}

func (b *Builder) FinishCommand(result *spawn.Result) (bool, error) {
	defer metrics.Record("FinishCommand")()
	edge := result.Edge

	// First try to extract dependencies from the result, if any.
	// This must happen first as it filters the command output (we want
	// to filter /showIncludes output, even on compile failure) and
	// extraction itself can fail, which makes the command fail from a
	// build perspective.
	var depsNodes []*graph.Node
	depsType := edge.GetBinding("deps")
	depsPrefix := edge.GetBinding("msvc_deps_prefix")
	if depsType != "" {
		dn, err := b.ExtractDeps(result, depsType, depsPrefix)
		if err != nil && result.Success() {
			if result.Output != "" {
				result.Output += "\n"
			}
			result.Output += err.Error()
			result.Status = exit_status.ExitFailure
		}
		depsNodes = dn
	}

	// At this point, dyndeps have been read, if they exist.
	// If the command runner implements the caching interface, notify it
	// so that it can cache the result under the correct key (including all
	// depfiles as inputs)
	if cacher, ok := b.commandRunner.(CachingCommandRunner); ok {
		if err := cacher.CacheResult(result, depsNodes); err != nil {
			util.Errorf("Error caching result of command: %s", result.Edge.EvaluateCommand(false))
		}
	}

	absoluteStart := b.runningEdges[edge]
	absoluteEnd := time.Now()
	delete(b.runningEdges, edge)

	result.Start = absoluteStart
	result.End = absoluteEnd
	b.status.BuildEdgeFinished(edge, result)

	// The rest of this function only applies to successful commands.
	if !result.Success() {
		return b.plan.EdgeFinished(edge, EdgeFailed)
	}

	// Restat the edge outputs
	recordMtime := timestamp.TimeStamp(0)
	if !b.config.DryRun {
		restat := edge.GetBindingBool("restat")
		generator := edge.GetBindingBool("generator")
		nodeCleaned := false
		recordMtime = edge.CommandStartTime()

		// restat and generator rules must restat the outputs after the build
		// has finished. if record_mtime == 0, then there was an error while
		// attempting to touch/stat the temp file when the edge started and
		// we should fall back to recording the outputs' current mtime in the
		// log.
		if recordMtime == 0 || restat || generator {
			for _, o := range edge.Outputs() {
				newMtime, err := b.diskInterface.Stat(o.Path())
				if err != nil || newMtime == -1 {
					return false, err
				}
				if newMtime > recordMtime {
					recordMtime = newMtime
				}
				if o.Mtime() == newMtime && restat {
					// The rule command did not change the output.  Propagate the clean
					// state through the build graph.
					// Note that this also applies to nonexistent outputs (mtime == 0).
					if !b.plan.CleanNode(b.scan, o) {
						return false, nil
					}
					nodeCleaned = true
				}
			}
		}

		if nodeCleaned {
			recordMtime = edge.CommandStartTime()
		}
	}
	if done, err := b.plan.EdgeFinished(edge, EdgeSucceeded); err != nil || !done {
		return done, err
	}

	// Delete any left over response file.
	rspFile := edge.GetUnescapedRspfile()
	if rspFile != "" && !debug_flags.KeepRsp {
		b.diskInterface.RemoveFile(rspFile)
	}

	if b.scan.BuildLog() != nil {
		startTimeMillis := absoluteStart.Sub(b.startTime).Milliseconds()
		endTimeMillis := absoluteEnd.Sub(b.startTime).Milliseconds()
		if err := b.scan.BuildLog().RecordCommand(edge, startTimeMillis, endTimeMillis, recordMtime); err != nil {
			return false, fmt.Errorf("Error writing to build log: %s", err)
		}
	}

	if depsType != "" && !b.config.DryRun {
		if len(edge.Outputs()) == 0 {
			panic("should have been rejected by parser")
		}
		for _, o := range edge.Outputs() {
			depsMtime, err := b.diskInterface.Stat(o.Path())
			if err != nil || depsMtime == -1 {
				return false, err
			}
			if err := b.scan.DepsLog().RecordDeps(o, depsMtime, depsNodes); err != nil {
				return false, fmt.Errorf("Error writing to deps log: %s", err)
			}
		}
	}
	return true, nil
}

func (b *Builder) ExtractDeps(result *spawn.Result, depsType, depsPrefix string) ([]*graph.Node, error) {
	defer span.Record(result.Context, "Load DynDeps")()
	depsNodes := make([]*graph.Node, 0)

	if depsType == "msvc" {
		// TODO(tylerw): CL PARSER LOGIC GOES HERE
		return nil, fmt.Errorf("windows support tbd")
	} else if depsType == "gcc" {
		depfile := result.Edge.GetUnescapedDepfile()
		if depfile == "" {
			return nil, fmt.Errorf("edge with deps=gcc but no depfile makes no sense")
		}
		// Read depfile content.  Treat a missing depfile as empty.
		content, err := b.diskInterface.ReadFile(depfile)
		if err != nil {
			if os.IsNotExist(err) {
				err = nil
			} else {
				return nil, err
			}
		}
		if len(content) == 0 {
			return nil, nil
		}

		deps := depfile_parser.New(b.config.DepfileParserOptions)
		if err := deps.Parse(string(content)); err != nil {
			return nil, err
		}

		// XXX check depfile matches expected output.
		for _, in := range deps.Ins() {
			path, _ := util.CanonicalizePath(in)
			depsNodes = append(depsNodes, b.state.GetNode(path))
		}
		if !debug_flags.KeepDepfile {
			if e := b.diskInterface.RemoveFile(depfile); e < 0 {
				return nil, fmt.Errorf("deleting depfile: %d", e)
			}
		}
	} else {
		util.Fatalf("unknown deps type: '%s'", depsType)
	}

	return depsNodes, nil
}

func (b *Builder) LoadDyndeps(node *graph.Node) error {
	ddf := dyndep_parser.NewDyndepFile()
	if err := b.scan.LoadDyndepsInto(node, ddf); err != nil {
		return err
	}

	_, err := b.plan.DyndepsLoaded(b.scan, node, ddf)
	if err != nil {
		// We don't care about ok here, just error.
		return err
	}
	return nil
}

func (b *Builder) TestOnlyPlan() *Plan {
	return b.plan
}

func (b *Builder) TestOnlySetCommandRunner(runner CommandRunner) {
	b.commandRunner = runner
}

func (b *Builder) GetExitCode() exit_status.ExitStatusType {
	return b.exitCode
}

func (b *Builder) SetFailureCode(code exit_status.ExitStatusType) {
	if code != exit_status.ExitSuccess {
		b.exitCode = code
	}
}

func (b *Builder) Dump() {
	b.plan.Dump()
}
