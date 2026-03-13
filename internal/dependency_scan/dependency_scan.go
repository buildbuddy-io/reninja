package dependency_scan

import (
	"fmt"
	"strings"

	"github.com/buildbuddy-io/reninja/internal/build_log"
	"github.com/buildbuddy-io/reninja/internal/depfile_parser"
	"github.com/buildbuddy-io/reninja/internal/deps_log"
	"github.com/buildbuddy-io/reninja/internal/disk"
	"github.com/buildbuddy-io/reninja/internal/dyndep"
	"github.com/buildbuddy-io/reninja/internal/dyndep_parser"
	"github.com/buildbuddy-io/reninja/internal/explanations"
	"github.com/buildbuddy-io/reninja/internal/graph"
	"github.com/buildbuddy-io/reninja/internal/implicit_dep_loader"
	"github.com/buildbuddy-io/reninja/internal/state"
)

type DependencyScan struct {
	buildLog             *build_log.BuildLog
	depsLog              *deps_log.DepsLog
	diskInterface        disk.Interface
	depLoader            *implicit_dep_loader.ImplicitDepLoader
	depfileParserOptions depfile_parser.DepfileParserOptions
	dyndepLoader         *dyndep.DyndepLoader
	explanations         *explanations.OptionalExplanations
}

func New(state *state.State, buildLog *build_log.BuildLog, depsLog *deps_log.DepsLog, diskInterface disk.Interface, depfileParserOptions depfile_parser.DepfileParserOptions, exp *explanations.Explanations) *DependencyScan {
	return &DependencyScan{
		buildLog:             buildLog,
		depsLog:              depsLog,
		diskInterface:        diskInterface,
		depLoader:            implicit_dep_loader.New(state, depsLog, diskInterface, depfileParserOptions, exp),
		depfileParserOptions: depfileParserOptions,
		dyndepLoader:         dyndep.NewDyndepLoader(state, diskInterface),
		explanations:         explanations.NewOptional(exp),
	}
}

func nodesToString(nodes []*graph.Node) string {
	if len(nodes) == 0 {
		return "<nil>"
	}
	paths := make([]string, 0)
	for _, n := range nodes {
		paths = append(paths, n.Path())
	}
	return strings.Join(paths, ", ")
}

func (s *DependencyScan) RecomputeDirty(initialNode *graph.Node, validationNodes []*graph.Node) ([]*graph.Node, error) {
	stack := make([]*graph.Node, 0)
	var newValidationNodes []*graph.Node
	var err error
	nodes := []*graph.Node{initialNode}

	for len(nodes) > 0 {
		node := nodes[0]
		nodes = nodes[1:]

		stack = stack[:0]
		newValidationNodes = newValidationNodes[:0]

		newValidationNodes, err = s.RecomputeNodeDirty(node, stack, newValidationNodes)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, newValidationNodes...)
		if len(newValidationNodes) > 0 {
			validationNodes = append(validationNodes, newValidationNodes...)
		}

	}
	return validationNodes, nil
}

func (s *DependencyScan) RecomputeNodeDirty(node *graph.Node, stack, validationNodes []*graph.Node) ([]*graph.Node, error) {
	edge := node.InEdge()
	if edge == nil {
		// If we already visited this leaf node then we are done.
		if node.StatusKnown() {
			return validationNodes, nil
		}
		// This node has no in-edge; it is dirty if it is missing.
		if err := node.StatIfNecessary(s.diskInterface); err != nil {
			return nil, err
		}
		if !node.Exists() {
			s.explanations.Record(node, "%s has no in-edge and is missing", node.Path())
		}
		node.SetDirty(!node.Exists())
		return validationNodes, nil
	}

	// If we already finished this edge then we are done.
	if edge.Mark() == graph.VisitDone {
		return validationNodes, nil
	}

	// If we encountered this edge earlier in the call stack we have a cycle.
	if err := s.VerifyDAG(node, stack); err != nil {
		return nil, err
	}
	edge.SetMark(graph.VisitInStack)
	stack = append(stack, node)

	dirty := false
	edge.SetOutputsReady(true)
	edge.SetDepsMissing(false)

	if !edge.DepsLoaded() {
		// This is our first encounter with this edge.
		// If there is a pending dyndep file, visit it now:
		// * If the dyndep file is ready then load it now to get any
		//   additional inputs and outputs for this and other edges.
		//   Once the dyndep file is loaded it will no longer be pending
		//   if any other edges encounter it, but they will already have
		//   been updated.
		// * If the dyndep file is not ready then since is known to be an
		//   input to this edge, the edge will not be considered ready below.
		//   Later during the build the dyndep file will become ready and be
		//   loaded to update this edge before it can possibly be scheduled.
		if edge.Dyndep() != nil && edge.Dyndep().DyndepPending() {
			if vNodes, err := s.RecomputeNodeDirty(edge.Dyndep(), stack, validationNodes); err != nil {
				return nil, err
			} else {
				validationNodes = vNodes
			}

			if edge.Dyndep().InEdge() == nil || edge.Dyndep().InEdge().OutputsReady() {
				// The dyndep file is ready, so load it now.
				if err := s.LoadDyndeps(edge.Dyndep()); err != nil {
					return nil, err
				}
			}
		}
	}

	// Load output mtimes so we can compare them to the most recent input below.
	for _, o := range edge.Outputs() {
		if err := o.StatIfNecessary(s.diskInterface); err != nil {
			return nil, err
		}
	}

	if !edge.DepsLoaded() {
		edge.SetDepsLoaded(true)
		loaded, err := s.depLoader.LoadDeps(edge)
		if err != nil {
			return nil, err
		}
		// Failed to load dependency info: rebuild to regenerate it.
		// LoadDeps() did explanations_->Record() already, no need to do it here.
		if !loaded {
			edge.SetDepsMissing(true)
			dirty = true
		}
	}

	// Store any validation nodes from the edge for adding to the initial
	// nodes.  Don't recurse into them, that would trigger the dependency
	// cycle detector if the validation node depends on this node.
	// RecomputeDirty will add the validation nodes to the initial nodes
	// and recurse into them.
	validationNodes = append(validationNodes, edge.Validations()...)

	// Visit all inputs; we're dirty if any of the inputs are dirty.
	var mostRecentInput *graph.Node

	// Helper to visit an input and check for readiness
	visitInput := func(i *graph.Node) error {
		if vNodes, err := s.RecomputeNodeDirty(i, stack, validationNodes); err != nil {
			return err
		} else {
			validationNodes = vNodes
		}
		// If an input is not ready, neither are our outputs.
		if inEdge := i.InEdge(); inEdge != nil {
			if !inEdge.OutputsReady() {
				edge.SetOutputsReady(false)
			}
		}
		return nil
	}

	// Helper to check if a non-order-only input makes us dirty
	checkDirty := func(i *graph.Node) {
		if i.Dirty() {
			s.explanations.Record(node, "%s is dirty", i.Path())
			dirty = true
		} else {
			if mostRecentInput == nil || i.Mtime() > mostRecentInput.Mtime() {
				mostRecentInput = i
			}
		}
	}

	// Visit explicit inputs (dirty check applies)
	for _, i := range edge.ExplicitInputs() {
		if err := visitInput(i); err != nil {
			return nil, err
		}
		checkDirty(i)
	}

	// Visit implicit inputs (dirty check applies)
	for _, i := range edge.ImplicitInputs() {
		if err := visitInput(i); err != nil {
			return nil, err
		}
		checkDirty(i)
	}

	// Visit order-only inputs (no dirty check)
	for _, i := range edge.OrderOnlyInputs() {
		if err := visitInput(i); err != nil {
			return nil, err
		}
	}

	// We may also be dirty due to output state: missing outputs, out of
	// date outputs, etc.  Visit all outputs and determine whether they're dirty.
	if !dirty {
		if !s.RecomputeOutputsDirty(edge, mostRecentInput, &dirty) {
			return nil, fmt.Errorf("RecomputeOutputsDirty returned false")
		}
	}

	// Finally, visit each output and update their dirty state if necessary.
	for _, o := range edge.Outputs() {
		if dirty {
			o.MarkDirty()
		}
	}

	// If an edge is dirty, its outputs are normally not ready.  (It's
	// possible to be clean but still not be ready in the presence of
	// order-only inputs.)
	// But phony edges with no inputs have nothing to do, so are always
	// ready.
	if dirty && !(edge.IsPhony() && len(edge.Inputs()) == 0) {
		edge.SetOutputsReady(false)
	}

	// Mark the edge as finished during this walk now that it will no longer
	// be in the call stack.
	edge.SetMark(graph.VisitDone)
	if stack[len(stack)-1] != node {
		panic("last item in stack is not node")
	}
	stack = stack[:len(stack)-1]
	return validationNodes, nil
}

func (s *DependencyScan) VerifyDAG(node *graph.Node, stack []*graph.Node) error {
	edge := node.InEdge()
	if edge == nil {
		panic("edge is nil")
	}

	// If we have no temporary mark on the edge then we do not yet have a cycle.
	if edge.Mark() != graph.VisitInStack {
		return nil
	}

	// We have this edge earlier in the call stack.  Find it.
	start := 0
	for i, s := range stack {
		if s.InEdge() != edge {
			continue
		}
		start = i
		break
	}
	if start == len(stack) {
		panic("did not find edge in stack")
	}

	// Make the cycle clear by reporting its start as the node at its end
	// instead of some other output of the starting edge.  For example,
	// running 'ninja b' on
	//   build a b: cat c
	//   build c: cat a
	// should report a -> c -> a instead of b -> c -> a.
	stack = stack[start:]
	stack[0] = node

	errMsg := "dependency cycle: "
	for _, s := range stack {
		errMsg += s.Path() + " -> "
	}
	errMsg += stack[start].Path()

	if start+1 == len(stack) && edge.MaybePhonycycleDiagnostic() {
		errMsg += " [-w phonycycle=err]"
	}

	return fmt.Errorf("%s", errMsg)
}

func (s *DependencyScan) RecomputeOutputsDirty(edge *graph.Edge, mostRecentInput *graph.Node, outputsDirty *bool) bool {
	command := edge.EvaluateCommand( /*incl_rsp_file=*/ true)
	for _, o := range edge.Outputs() {
		if s.RecomputeOutputDirty(edge, mostRecentInput, command, o) {
			*outputsDirty = true
			return true
		}
	}
	return true
}

func (s *DependencyScan) RecomputeOutputDirty(edge *graph.Edge, mostRecentInput *graph.Node, command string, output *graph.Node) bool {
	if edge.IsPhony() {
		// Phony edges don't write any output.  Outputs are only dirty if
		// there are no inputs and we're missing the output.
		if len(edge.Inputs()) == 0 && len(edge.Validations()) == 0 && !output.Exists() {
			s.explanations.Record(output, "output %s of phony edge with no inputs doesn't exist", output.Path())
			return true
		}

		// Update the mtime with the newest input. Dependents can thus call mtime()
		// on the fake node and get the latest mtime of the dependencies
		if mostRecentInput != nil {
			output.UpdatePhonyMtime(mostRecentInput.Mtime())
		}

		// Phony edges are clean, nothing to do
		return false
	}

	// Dirty if we're missing the output.
	if !output.Exists() {
		s.explanations.Record(output, "output %s doesn't exist", output.Path())
		return true
	}

	var entry *build_log.LogEntry

	// If this is a restat rule, we may have cleaned the output in a
	// previous run and stored the command start time in the build log.
	// We don't want to consider a restat rule's outputs as dirty unless
	// an input changed since the last run, so we'll skip checking the
	// output file's actual mtime and simply check the recorded mtime from
	// the log against the most recent input's mtime (see below)
	usedRestat := false
	if edge.GetBinding("restat") != "" && s.buildLog != nil {
		entry = s.buildLog.LookupByOutput(output.Path())
		if entry != nil {
			usedRestat = true
		}
	}

	// Dirty if the output is older than the input.
	if !usedRestat && mostRecentInput != nil && output.Mtime() < mostRecentInput.Mtime() {
		s.explanations.Record(output, "output %s older than most recent input %s (%d vs %d)",
			output.Path(), mostRecentInput.Path(), output.Mtime(), mostRecentInput.Mtime())
		return true
	}

	if s.buildLog != nil {
		generator := edge.GetBindingBool("generator")
		if entry == nil {
			entry = s.buildLog.LookupByOutput(output.Path())
		}
		if entry != nil {
			if !generator && build_log.HashCommand(command) != entry.CommandHash {
				// May also be dirty due to the command changing since the last build.
				// But if this is a generator rule, the command changing does not make us
				// dirty.
				s.explanations.Record(output, "command line changed for %s", output.Path())
				return true
			}
			if mostRecentInput != nil && entry.Mtime < mostRecentInput.Mtime() {
				// May also be dirty due to the mtime in the log being older than the
				// mtime of the most recent input.  This can occur even when the mtime
				// on disk is newer if a previous run wrote to the output file but
				// exited with an error or was interrupted. If this was a restat rule,
				// then we only check the recorded mtime against the most recent input
				// mtime and ignore the actual output's mtime above.
				s.explanations.Record(output, "recorded mtime of %s older than most recent input %s (%d vs %d", output.Path(), mostRecentInput.Path(), entry.Mtime, mostRecentInput.Mtime())
				return true
			}
		}
		if entry == nil && !generator {
			s.explanations.Record(output, "command line not found in log for %s", output.Path())
			return true
		}
	}
	return false
}

func (s *DependencyScan) LoadDyndeps(node *graph.Node) error {
	ddf := dyndep_parser.NewDyndepFile()
	return s.dyndepLoader.LoadDyndeps(node, ddf)
}

func (s *DependencyScan) LoadDyndepsInto(node *graph.Node, ddf dyndep_parser.DyndepFile) error {
	return s.dyndepLoader.LoadDyndeps(node, ddf)
}

func (s *DependencyScan) BuildLog() *build_log.BuildLog {
	return s.buildLog
}

func (s *DependencyScan) DepsLog() *deps_log.DepsLog {
	return s.depsLog
}
