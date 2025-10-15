package implicit_dep_loader

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/buildbuddy-io/gin/internal/depfile_parser"
	"github.com/buildbuddy-io/gin/internal/deps_log"
	"github.com/buildbuddy-io/gin/internal/disk"
	"github.com/buildbuddy-io/gin/internal/explanations"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/state"
	"github.com/buildbuddy-io/gin/internal/util"
)

type ImplicitDepLoader struct {
	state                *state.State
	depsLog              *deps_log.DepsLog
	diskInterface        disk.Interface
	depfileParserOptions depfile_parser.DepfileParserOptions
	explanations         *explanations.OptionalExplanations
	processDepfileDepsFn ProcessDepfileDepsFn
}

func New(state *state.State, depsLog *deps_log.DepsLog, diskInterface disk.Interface, depfileParserOptions depfile_parser.DepfileParserOptions, exp *explanations.Explanations) *ImplicitDepLoader {
	return &ImplicitDepLoader{
		state:                state,
		depsLog:              depsLog,
		diskInterface:        diskInterface,
		depfileParserOptions: depfileParserOptions,
		explanations:         explanations.NewOptional(exp),
	}
}

// LoadDeps returns a boolean indicating if load succeeded and an error.
// If error is set, it's a failure, if loading failed, it's up to the
// caller to decide what to do.
func (l *ImplicitDepLoader) LoadDeps(edge *graph.Edge) (bool, error) {
	depsType := edge.GetBinding("deps")
	if depsType != "" {
		return l.LoadDepsFromLog(edge)
	}
	depfile := edge.GetUnescapedDepfile()
	if depfile != "" {
		return l.LoadDepFile(edge, depfile)
	}
	// No deps to load.
	return true, nil
}

// Returns loaded, error
func (l *ImplicitDepLoader) LoadDepFile(edge *graph.Edge, path string) (bool, error) {
	content, err := l.diskInterface.ReadFile(path)
	if err != nil {
		// file not found is fine, ignore it.
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		} else {
			return false, err
		}
	}
	firstOutput := edge.Outputs()[0]
	if len(content) == 0 {
		l.explanations.Record(firstOutput, "depfile '%s' is missing", path)
		return false, nil
	}

	depfile := depfile_parser.New(l.depfileParserOptions)
	if err := depfile.Parse(string(content)); err != nil {
		return false, fmt.Errorf("%s: %s", path, err)
	}
	primaryOut := depfile.Outs()[0]
	primaryOut, _ = util.CanonicalizePath(primaryOut)

	// Check that this depfile matches the edge's output, if not return false to
	// mark the edge as dirty.
	opath := firstOutput.Path()
	if opath != primaryOut {
		l.explanations.Record(firstOutput, "expected depfile '%s' to mention '%s', got '%s'",
			path, firstOutput.Path(), primaryOut)
		return false, nil
	}

	// Ensure that all mentioned outputs are outputs of the edge.
	for _, o := range depfile.Outs() {
		found := false
		for _, edgeOut := range edge.Outputs() {
			if o == edgeOut.Path() {
				found = true
			}
			break
		}
		if !found {
			return false, fmt.Errorf("%s: depfile mentions '%s' as an output, but no such output was declared", path, o)
		}
	}
	return true, l.ProcessDepfileDeps(edge, depfile.Ins())
}

type ProcessDepfileDepsFn func(edge *graph.Edge, ins []string) error

func (l *ImplicitDepLoader) SetProcessDepfileDepsFn(fn ProcessDepfileDepsFn) {
	l.processDepfileDepsFn = fn
}

func (l *ImplicitDepLoader) ProcessDepfileDeps(edge *graph.Edge, ins []string) error {
	if l.processDepfileDepsFn != nil {
		return l.processDepfileDepsFn(edge, ins)
	}
	// TODO(tylerw): preallocate space in edge.inputs?
	nodes := make([]*graph.Node, len(ins))
	for i, in := range ins {
		in, _ = util.CanonicalizePath(in)
		node := l.state.GetNode(in)
		nodes[i] = node
		node.AddOutEdge(edge)
	}
	edge.PrependInputs(nodes)
	edge.SetImplicitDeps(edge.GetImplicitDeps() + len(ins))
	return nil
}

func (l *ImplicitDepLoader) LoadDepsFromLog(edge *graph.Edge) (bool, error) {
	// NOTE: deps are only supported for single-target edges.
	output := edge.Outputs()[0]
	var deps *deps_log.Deps
	if l.depsLog != nil {
		deps = l.depsLog.GetDeps(output)
	}
	if deps == nil {
		l.explanations.Record(output, "deps for '%s' are missing", output.Path())
		return false, nil
	}

	// Deps are invalid if the output is newer than the deps.
	if output.Mtime() > deps.Mtime {
		l.explanations.Record(output, "stored deps info out of date for '%s' (%d vs %d)", output.Path(), deps.Mtime, output.Mtime())
		return false, nil
	}

	nodes := deps.Nodes
	nodeCount := len(deps.Nodes)
	for _, node := range nodes {
		edge.AddInput(node)
	}
	edge.SetImplicitDeps(edge.GetImplicitDeps() + nodeCount)
	for _, node := range nodes {
		node.AddOutEdge(edge)
	}
	return true, nil
}

type NodeStoringImplicitDepLoader struct {
	*ImplicitDepLoader
	depNodesOutput []*graph.Node
}

func (l *NodeStoringImplicitDepLoader) DepNodesOutput() []*graph.Node {
	return l.depNodesOutput
}

func NewNodeStoringImplicitDepLoader(state *state.State, depsLog *deps_log.DepsLog, diskInterface disk.Interface, depfileParserOptions depfile_parser.DepfileParserOptions, explanations *explanations.Explanations) *NodeStoringImplicitDepLoader {
	l := &NodeStoringImplicitDepLoader{
		depNodesOutput: make([]*graph.Node, 0),
	}
	idl := New(state, depsLog, diskInterface, depfileParserOptions, explanations)

	idl.SetProcessDepfileDepsFn(func(edge *graph.Edge, depfileIns []string) error {
		for _, in := range depfileIns {
			in, _ = util.CanonicalizePath(in)
			node := idl.state.GetNode(in)
			l.depNodesOutput = append(l.depNodesOutput, node)
		}
		return nil
	})
	l.ImplicitDepLoader = idl
	return l
}
