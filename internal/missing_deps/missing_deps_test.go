package missing_deps_test

import (
	"os"
	"slices"
	"testing"

	"github.com/buildbuddy-io/gin/internal/deps_log"
	"github.com/buildbuddy-io/gin/internal/disk"
	"github.com/buildbuddy-io/gin/internal/eval_env"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/missing_deps"
	"github.com/buildbuddy-io/gin/internal/state"
	"github.com/buildbuddy-io/gin/internal/timestamp"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testDepsLogFilename = "MissingDepTest-tempdepslog"
)

type MissingDependencyTestDelegate struct{}

func (d *MissingDependencyTestDelegate) OnMissingDep(node *graph.Node, path string, generator *eval_env.Rule) {
}

type testHelper struct {
	t             *testing.T
	delegate      *MissingDependencyTestDelegate
	generatorRule *eval_env.Rule
	compileRule   *eval_env.Rule
	depsLog       *deps_log.DepsLog
	state         *state.State
	diskInterface disk.Interface
	scanner       *missing_deps.MissingDependencyScanner
}

func newTestHelper(t *testing.T) *testHelper {
	th := &testHelper{
		t:             t,
		delegate:      &MissingDependencyTestDelegate{},
		generatorRule: eval_env.NewRule("generator_rule"),
		compileRule:   eval_env.NewRule("compile_rule"),
		depsLog:       deps_log.NewDepsLog(),
		state:         state.New(),
		diskInterface: disk.NewMockDiskInterface(),
	}
	os.Remove(testDepsLogFilename)
	require.NoError(t, th.depsLog.OpenForWrite(testDepsLogFilename))
	th.scanner = missing_deps.NewScanner(th.delegate, th.depsLog, th.state, th.diskInterface)
	t.Cleanup(func() {
		os.Remove(testDepsLogFilename)
	})
	return th
}

func (h *testHelper) RecordDepsLogDep(from, to string) {
	require.NoError(h.t, h.depsLog.RecordDeps(h.state.LookupNode(from), timestamp.TimeStamp(0), []*graph.Node{h.state.LookupNode(to)}))
}

func (h *testHelper) ProcessAllNodes() {
	nodes, err := h.state.RootNodes()
	require.NoError(h.t, err)
	for _, node := range nodes {
		h.scanner.ProcessNode(node)
	}
}

func (h *testHelper) CreateInitialState() {
	depsType := &eval_env.EvalString{}
	depsType.AddText("gcc")
	h.compileRule.AddBinding("deps", depsType)
	h.generatorRule.AddBinding("deps", depsType)
	headerEdge := h.state.AddEdge(h.generatorRule)
	require.NoError(h.t, h.state.AddOut("generated_header", headerEdge))
	compileEdge := h.state.AddEdge(h.compileRule)
	require.NoError(h.t, h.state.AddOut("compiled_object", compileEdge))
}

func (h *testHelper) CreateGraphDependencyBetween(from, to string) {
	fromNode := h.state.LookupNode(from)
	fromEdge := fromNode.InEdge()
	h.state.AddIn(to, fromEdge)
}

func (h *testHelper) AssertMissingDependencyBetween(flaky, generated string, rule *eval_env.Rule) {
	flakyNode := h.state.LookupNode(flaky)
	require.True(h.t, slices.Contains(h.scanner.NodesMissingDeps(), flakyNode))
	generatedNode := h.state.LookupNode(generated)
	require.True(h.t, slices.Contains(h.scanner.GeneratedNodes(), generatedNode))
	require.True(h.t, slices.Contains(h.scanner.GeneratorRules(), rule))
}

func TestEmptyGraph(t *testing.T) {
	th := newTestHelper(t)
	th.ProcessAllNodes()
	require.False(t, th.scanner.HadMissingDeps())
}

func TestNoMissingDep(t *testing.T) {
	th := newTestHelper(t)
	th.CreateInitialState()
	th.ProcessAllNodes()
	require.False(t, th.scanner.HadMissingDeps())
}

func TestMissingDepPresent(t *testing.T) {
	th := newTestHelper(t)
	th.CreateInitialState()
	// compiled_object uses generated_header, without a proper dependency
	th.RecordDepsLogDep("compiled_object", "generated_header")
	th.ProcessAllNodes()
	require.True(t, th.scanner.HadMissingDeps())
	assert.Equal(t, 1, len(th.scanner.NodesMissingDeps()))
	assert.Equal(t, 1, th.scanner.MissingDepPathCount())
	th.AssertMissingDependencyBetween("compiled_object", "generated_header", th.generatorRule)
}

func TestMissingDepFixedDirect(t *testing.T) {
	th := newTestHelper(t)
	th.CreateInitialState()
	// Adding the direct dependency fixes the missing dep
	th.CreateGraphDependencyBetween("compiled_object", "generated_header")
	th.RecordDepsLogDep("compiled_object", "generated_header")
	th.ProcessAllNodes()
	require.False(t, th.scanner.HadMissingDeps())
}

func TestMissingDepFixedIndirect(t *testing.T) {
	th := newTestHelper(t)
	th.CreateInitialState()
	// Adding an indirect dependency also fixes the issue
	intermediateEdge := th.state.AddEdge(th.generatorRule)
	require.NoError(t, th.state.AddOut("intermediate", intermediateEdge))
	th.CreateGraphDependencyBetween("compiled_object", "intermediate")
	th.CreateGraphDependencyBetween("intermediate", "generated_header")
	th.RecordDepsLogDep("compiled_object", "generated_header")
	th.ProcessAllNodes()
	require.False(t, th.scanner.HadMissingDeps())
}

func TestCyclicMissingDep(t *testing.T) {
	th := newTestHelper(t)
	th.CreateInitialState()
	th.RecordDepsLogDep("generated_header", "compiled_object")
	th.RecordDepsLogDep("compiled_object", "generated_header")
	// In case of a cycle, both paths are reported (and there is
	// no way to fix the issue by adding deps).
	th.ProcessAllNodes()
	require.True(t, th.scanner.HadMissingDeps())
	require.Equal(t, 2, len(th.scanner.NodesMissingDeps()))
	require.Equal(t, 2, th.scanner.MissingDepPathCount())

	th.AssertMissingDependencyBetween("compiled_object", "generated_header", th.generatorRule)
	th.AssertMissingDependencyBetween("generated_header", "compiled_object", th.compileRule)
}

func TestCycleInGraph(t *testing.T) {
	th := newTestHelper(t)
	th.CreateInitialState()
	th.CreateGraphDependencyBetween("compiled_object", "generated_header")
	th.CreateGraphDependencyBetween("generated_header", "compiled_object")
	// The missing-deps tool doesn't deal with cycles in the graph, because
	// there will be an error loading the graph before we get to the tool.
	// This test is to illustrate that.
	_, err := th.state.RootNodes()
	require.Error(t, err)
}
