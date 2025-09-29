package build

import (
	"fmt"
	"sort"

	"github.com/buildbuddy-io/gin/internal/build_config"
	"github.com/buildbuddy-io/gin/internal/build_log"
	"github.com/buildbuddy-io/gin/internal/debug_flags"
	"github.com/buildbuddy-io/gin/internal/dependency_scan"
	"github.com/buildbuddy-io/gin/internal/deps_log"
	"github.com/buildbuddy-io/gin/internal/disk"
	"github.com/buildbuddy-io/gin/internal/dyndep"
	"github.com/buildbuddy-io/gin/internal/dyndep_parser"
	"github.com/buildbuddy-io/gin/internal/exit_status"
	"github.com/buildbuddy-io/gin/internal/explanations"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/jobserver"
	"github.com/buildbuddy-io/gin/internal/priority_queue"
	"github.com/buildbuddy-io/gin/internal/state"
	"github.com/buildbuddy-io/gin/internal/status"
)

type Result struct {
	Edge   *graph.Edge
	Status exit_status.ExitStatusType
	Output string
}

func (r Result) Success() bool {
	return r.Status == exit_status.ExitSuccess
}

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

func (p *Plan) DyndepsLoaded(scan *dependency_scan.DependencyScan, node *graph.Node, ddf dyndep.DyndepFile) error {
	return nil
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
		if edge == nil || !edge.OutputsReady() {
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

type RunningEdgeMap = map[*graph.Edge]int

type Builder struct {
	state         *state.State
	config        build_config.Config
	plan          *Plan
	jobserver     jobserver.Client
	commandRunner *CommandRunner
	status        status.Status

	runningEdges    RunningEdgeMap
	startTimeMillis int64
	lockFilePath    string
	diskInterface   disk.Interface
	explanations    *explanations.OptionalExplanations

	scan     *dependency_scan.DependencyScan
	exitCode exit_status.ExitStatusType
}

func NewBuilder(state *state.State, config build_config.Config, buildLog *build_log.BuildLog,
	depsLog *deps_log.DepsLog, diskInterface disk.Interface, status status.Status, startTimeMillis int64) *Builder {
	b := &Builder{
		state:           state,
		config:          config,
		status:          status,
		startTimeMillis: startTimeMillis,
		diskInterface:   diskInterface,
		runningEdges:    make(RunningEdgeMap, 0),
	}
	b.plan = NewPlan(b)

	var realExp *explanations.Explanations
	if debug_flags.Explaining {
		realExp = explanations.New()
	}
	b.explanations = explanations.NewOptional(realExp)

	lockFilePath := ".ninja_lock"
	if buildDir := state.Bindings().LookupVariable("builddir"); buildDir != "" {
		lockFilePath = buildDir + "/" + lockFilePath
	}
	b.lockFilePath = lockFilePath

	b.scan = dependency_scan.New(state, buildLog, depsLog, diskInterface, config.DepfileParserOptions, realExp)
	return b

}
func (b *Builder) LoadDyndeps(node *graph.Node) error {
	ddf := dyndep_parser.NewDyndepFile()
	if err := b.scan.LoadDyndepsInto(node, ddf); err != nil {
		return err
	}

	if err := b.plan.DyndepsLoaded(b.scan, node, ddf); err != nil {
		return err
	}

	return nil
}
