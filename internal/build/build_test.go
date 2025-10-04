package build_test

import (
	"fmt"
	"math"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/buildbuddy-io/gin/internal/build"
	"github.com/buildbuddy-io/gin/internal/build_config"
	"github.com/buildbuddy-io/gin/internal/build_log"
	"github.com/buildbuddy-io/gin/internal/deps_log"
	"github.com/buildbuddy-io/gin/internal/disk"
	"github.com/buildbuddy-io/gin/internal/exit_status"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/state"
	"github.com/buildbuddy-io/gin/internal/status"
	"github.com/buildbuddy-io/gin/internal/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stateTestWithBuiltinRulesHelper struct {
	t     *testing.T
	state *state.State
	plan  *build.Plan
}

func newStateTestWithBuiltinRulesHelper(t *testing.T) *stateTestWithBuiltinRulesHelper {
	t.Helper()
	s := state.New()
	test.AddCatRule(t, s)

	return &stateTestWithBuiltinRulesHelper{
		t:     t,
		state: s,
		plan:  build.NewPlan(nil /*=builder*/),
	}
}

func CompareEdgesByOutput(a, b *graph.Edge) int {
	aOut0Path := a.Outputs()[0].Path()
	bOut0Path := b.Outputs()[0].Path()
	return strings.Compare(aOut0Path, bOut0Path)
}

func (h *stateTestWithBuiltinRulesHelper) FindWorkSorted(count int) []*graph.Edge {
	h.t.Helper()
	ret := make([]*graph.Edge, 0)
	for i := 0; i < count; i++ {
		assert.True(h.t, h.plan.MoreToDo())
		edge := h.plan.FindWork()
		assert.NotNil(h.t, edge)
		ret = append(ret, edge)
	}
	assert.Nil(h.t, h.plan.FindWork())
	slices.SortFunc(ret, CompareEdgesByOutput)
	return ret
}

func (h *stateTestWithBuiltinRulesHelper) PrepareForTarget(nodeName string) {
	h.t.Helper()
	ok, err := h.plan.AddTarget(h.state.GetNode(nodeName))
	require.NoError(h.t, err)
	require.True(h.t, ok)

	h.plan.PrepareQueue()

	assert.True(h.t, h.plan.MoreToDo())
}

func TestBasic(t *testing.T) {
	th := newStateTestWithBuiltinRulesHelper(t)
	test.AssertParse(t, `
build out: cat mid
build mid: cat in
`, th.state)
	th.state.GetNode("mid").MarkDirty()
	th.state.GetNode("out").MarkDirty()
	th.PrepareForTarget("out")

	edge := th.plan.FindWork()
	assert.NotNil(t, edge)
	assert.Equal(t, "in", edge.Inputs()[0].Path())
	assert.Equal(t, "mid", edge.Outputs()[0].Path())
	assert.Nil(t, th.plan.FindWork())

	ok, err := th.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	require.NotNil(t, edge)
	assert.Equal(t, "mid", edge.Inputs()[0].Path())
	assert.Equal(t, "out", edge.Outputs()[0].Path())

	ok, err = th.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.Nil(t, edge)
}

func TestDoubleOutputDirect(t *testing.T) {
	th := newStateTestWithBuiltinRulesHelper(t)
	test.AssertParse(t, `
build out: cat mid1 mid2
build mid1 mid2: cat in
`, th.state)
	th.state.GetNode("mid1").MarkDirty()
	th.state.GetNode("mid2").MarkDirty()
	th.state.GetNode("out").MarkDirty()
	th.PrepareForTarget("out")

	edge := th.plan.FindWork()
	assert.NotNil(t, edge) // cat in

	ok, err := th.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.NotNil(t, edge) // cat mid1 mid2

	ok, err = th.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.Nil(t, edge) // done
}

func TestDoubleOutputIndirect(t *testing.T) {
	th := newStateTestWithBuiltinRulesHelper(t)
	test.AssertParse(t, `
build out: cat b1 b2
build b1: cat a1
build b2: cat a2
build a1 a2: cat in
`, th.state)
	th.state.GetNode("a1").MarkDirty()
	th.state.GetNode("a2").MarkDirty()
	th.state.GetNode("b1").MarkDirty()
	th.state.GetNode("b2").MarkDirty()
	th.state.GetNode("out").MarkDirty()
	th.PrepareForTarget("out")

	edge := th.plan.FindWork()
	assert.NotNil(t, edge) // cat in

	ok, err := th.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.NotNil(t, edge) // cat a1

	ok, err = th.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.NotNil(t, edge) // cat a2

	ok, err = th.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.NotNil(t, edge) // cat b1 b2

	ok, err = th.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.Nil(t, edge) // done
}

func TestDoubleDependent(t *testing.T) {
	th := newStateTestWithBuiltinRulesHelper(t)
	test.AssertParse(t, `
build out: cat a1 a2
build a1: cat mid
build a2: cat mid
build mid: cat in
`, th.state)
	th.state.GetNode("mid").MarkDirty()
	th.state.GetNode("a1").MarkDirty()
	th.state.GetNode("a2").MarkDirty()
	th.state.GetNode("out").MarkDirty()
	th.PrepareForTarget("out")

	edge := th.plan.FindWork()
	assert.NotNil(t, edge) // cat in

	ok, err := th.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.NotNil(t, edge) // cat mid

	ok, err = th.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.NotNil(t, edge) // cat mid

	ok, err = th.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.NotNil(t, edge) // cat a1 a2

	ok, err = th.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.Nil(t, edge) // done
}

func (h *stateTestWithBuiltinRulesHelper) TestPoolWithDepthOne(testCase string) {
	h.t.Helper()

	test.AssertParse(h.t, testCase, h.state)
	h.state.GetNode("out1").MarkDirty()
	h.state.GetNode("out2").MarkDirty()
	ok, err := h.plan.AddTarget(h.state.GetNode("out1"))
	require.NoError(h.t, err)
	require.True(h.t, ok)
	ok, err = h.plan.AddTarget(h.state.GetNode("out2"))
	require.NoError(h.t, err)
	require.True(h.t, ok)
	h.plan.PrepareQueue()
	require.True(h.t, h.plan.MoreToDo())

	edge := h.plan.FindWork()
	require.NotNil(h.t, edge)
	require.Equal(h.t, "in", edge.Inputs()[0].Path())
	require.Equal(h.t, "out1", edge.Outputs()[0].Path())

	// This will be false since poolcat is serialized
	require.Nil(h.t, h.plan.FindWork())

	ok, err = h.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(h.t, err)
	require.True(h.t, ok)

	edge = h.plan.FindWork()
	require.NotNil(h.t, edge)

	require.Equal(h.t, "in", edge.Inputs()[0].Path())
	require.Equal(h.t, "out2", edge.Outputs()[0].Path())

	require.Nil(h.t, h.plan.FindWork())

	ok, err = h.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(h.t, err)
	require.True(h.t, ok)

	require.False(h.t, h.plan.MoreToDo())
	edge = h.plan.FindWork()
	require.Nil(h.t, edge)
}

func TestPoolWithDepthOne(t *testing.T) {
	th := newStateTestWithBuiltinRulesHelper(t)
	th.TestPoolWithDepthOne(`
pool foobar
  depth = 1
rule poolcat
  command = cat $in > $out
  pool = foobar
build out1: poolcat in
build out2: poolcat in
`)
}

func TestConsolePool(t *testing.T) {
	th := newStateTestWithBuiltinRulesHelper(t)
	th.TestPoolWithDepthOne(`
rule poolcat
  command = cat $in > $out
  pool = console
build out1: poolcat in
build out2: poolcat in
`)
}

func TestPoolsWithDepthTwo(t *testing.T) {
	th := newStateTestWithBuiltinRulesHelper(t)
	test.AssertParse(t, `
pool foobar
  depth = 2
pool bazbin
  depth = 2
rule foocat
  command = cat $in > $out
  pool = foobar
rule bazcat
  command = cat $in > $out
  pool = bazbin
build out1: foocat in
build out2: foocat in
build out3: foocat in
build outb1: bazcat in
build outb2: bazcat in
build outb3: bazcat in
  pool =
build allTheThings: cat out1 out2 out3 outb1 outb2 outb3
`, th.state)

	// Mark all the out* nodes dirty
	for i := 0; i < 3; i++ {
		th.state.GetNode("out" + string('1'+rune(i))).MarkDirty()
		th.state.GetNode("outb" + string('1'+rune(i))).MarkDirty()
	}
	th.state.GetNode("allTheThings").MarkDirty()
	th.PrepareForTarget("allTheThings")

	edges := th.FindWorkSorted(5)

	for i := 0; i < 4; i++ {
		edge := edges[i]
		assert.Equal(t, "in", edge.Inputs()[0].Path())
		baseName := "out"
		if i >= 2 {
			baseName = "outb"
		}
		assert.Equal(t, baseName+string('1'+rune(i%2)), edge.Outputs()[0].Path())
	}

	// outb3 is exempt because it has an empty pool
	edge := edges[4]
	assert.NotNil(t, edge)
	assert.Equal(t, "in", edge.Inputs()[0].Path())
	assert.Equal(t, "outb3", edge.Outputs()[0].Path())

	// finish out1
	ok, err := th.plan.EdgeFinished(edges[0], build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.NotNil(t, edge)
	assert.Equal(t, "out3", edge.Outputs()[0].Path())

	ok, err = th.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.Nil(t, edge)

	ok, err = th.plan.EdgeFinished(edges[1], build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.Nil(t, edge)

	ok, err = th.plan.EdgeFinished(edges[2], build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.Nil(t, edge)

	ok, err = th.plan.EdgeFinished(edges[3], build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.Nil(t, edge)

	ok, err = th.plan.EdgeFinished(edges[4], build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.NotNil(t, edge)
	assert.Equal(t, "allTheThings", edge.Outputs()[0].Path())
}

func TestPoolWithRedundantEdges(t *testing.T) {
	th := newStateTestWithBuiltinRulesHelper(t)
	test.AssertParse(t, `
pool compile
  depth = 1
rule gen_foo
  command = touch foo.cpp
rule gen_bar
  command = touch bar.cpp
rule echo
  command = echo $out > $out
build foo.cpp.obj: echo foo.cpp || foo.cpp
  pool = compile
build bar.cpp.obj: echo bar.cpp || bar.cpp
  pool = compile
build libfoo.a: echo foo.cpp.obj bar.cpp.obj
build foo.cpp: gen_foo
build bar.cpp: gen_bar
build all: phony libfoo.a
`, th.state)

	th.state.GetNode("foo.cpp").MarkDirty()
	th.state.GetNode("foo.cpp.obj").MarkDirty()
	th.state.GetNode("bar.cpp").MarkDirty()
	th.state.GetNode("bar.cpp.obj").MarkDirty()
	th.state.GetNode("libfoo.a").MarkDirty()
	th.state.GetNode("all").MarkDirty()
	th.PrepareForTarget("all")

	initialEdges := th.FindWorkSorted(2)

	edge := initialEdges[1] // Foo first
	assert.Equal(t, "foo.cpp", edge.Outputs()[0].Path())

	ok, err := th.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.NotNil(t, edge)
	assert.Nil(t, th.plan.FindWork())
	assert.Equal(t, "foo.cpp", edge.Inputs()[0].Path())
	assert.Equal(t, "foo.cpp", edge.Inputs()[1].Path())
	assert.Equal(t, "foo.cpp.obj", edge.Outputs()[0].Path())

	ok, err = th.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = initialEdges[0] // Now for bar
	assert.Equal(t, "bar.cpp", edge.Outputs()[0].Path())

	ok, err = th.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.NotNil(t, edge)
	assert.Nil(t, th.plan.FindWork())
	assert.Equal(t, "bar.cpp", edge.Inputs()[0].Path())
	assert.Equal(t, "bar.cpp", edge.Inputs()[1].Path())
	assert.Equal(t, "bar.cpp.obj", edge.Outputs()[0].Path())

	ok, err = th.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.NotNil(t, edge)
	assert.Nil(t, th.plan.FindWork())
	assert.Equal(t, "libfoo.a", edge.Outputs()[0].Path())

	ok, err = th.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.NotNil(t, edge)
	assert.Nil(t, th.plan.FindWork())
	assert.Equal(t, "all", edge.Outputs()[0].Path())

	ok, err = th.plan.EdgeFinished(edge, build.EdgeSucceeded)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.Nil(t, edge)
	assert.False(t, th.plan.MoreToDo())
}

func TestPoolWithFailingEdge(t *testing.T) {
	th := newStateTestWithBuiltinRulesHelper(t)
	test.AssertParse(t, `
pool foobar
  depth = 1
rule poolcat
  command = cat $in > $out
  pool = foobar
build out1: poolcat in
build out2: poolcat in
`, th.state)

	th.state.GetNode("out1").MarkDirty()
	th.state.GetNode("out2").MarkDirty()

	ok, err := th.plan.AddTarget(th.state.GetNode("out1"))
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = th.plan.AddTarget(th.state.GetNode("out2"))
	require.NoError(t, err)
	require.True(t, ok)

	th.plan.PrepareQueue()
	assert.True(t, th.plan.MoreToDo())

	edge := th.plan.FindWork()
	assert.NotNil(t, edge)
	assert.Equal(t, "in", edge.Inputs()[0].Path())
	assert.Equal(t, "out1", edge.Outputs()[0].Path())

	// This will be false since poolcat is serialized
	assert.Nil(t, th.plan.FindWork())

	ok, err = th.plan.EdgeFinished(edge, build.EdgeFailed)
	require.NoError(t, err)
	require.True(t, ok)

	edge = th.plan.FindWork()
	assert.NotNil(t, edge)
	assert.Equal(t, "in", edge.Inputs()[0].Path())
	assert.Equal(t, "out2", edge.Outputs()[0].Path())

	assert.Nil(t, th.plan.FindWork())

	ok, err = th.plan.EdgeFinished(edge, build.EdgeFailed)
	require.NoError(t, err)
	require.True(t, ok)

	assert.True(t, th.plan.MoreToDo()) // Jobs have failed
	edge = th.plan.FindWork()
	assert.Nil(t, edge)
}

func TestPriorityWithoutBuildLog(t *testing.T) {
	th := newStateTestWithBuiltinRulesHelper(t)
	// Without a build log, the critical time is equivalent to graph
	// depth. Test with the following graph:
	//   a2
	//   |
	//   a1  b1
	//   |  |  |
	//   a0 b0 c0
	//    \ | /
	//     out
	test.AssertParse(t, `
rule r
  command = unused
build out: r a0 b0 c0
build a0: r a1
build a1: r a2
build b0: r b1
build c0: r b1
`, th.state)

	th.state.GetNode("a1").MarkDirty()
	th.state.GetNode("a0").MarkDirty()
	th.state.GetNode("b0").MarkDirty()
	th.state.GetNode("c0").MarkDirty()
	th.state.GetNode("out").MarkDirty()

	// Note: Go version doesn't use BuildLog parameter in PrepareForTarget
	th.PrepareForTarget("out")

	assert.Equal(t, int64(1), th.state.GetNode("out").InEdge().CriticalPathWeight())
	assert.Equal(t, int64(2), th.state.GetNode("a0").InEdge().CriticalPathWeight())
	assert.Equal(t, int64(2), th.state.GetNode("b0").InEdge().CriticalPathWeight())
	assert.Equal(t, int64(2), th.state.GetNode("c0").InEdge().CriticalPathWeight())
	assert.Equal(t, int64(3), th.state.GetNode("a1").InEdge().CriticalPathWeight())

	expectedOrder := []string{"a1", "a0", "b0", "c0", "out"}

	for i := 0; i < len(expectedOrder); i++ {
		edge := th.plan.FindWork()
		require.NotNil(t, edge)
		assert.Equal(t, expectedOrder[i], edge.Outputs()[0].Path())

		ok, err := th.plan.EdgeFinished(edge, build.EdgeSucceeded)
		require.NoError(t, err)
		require.True(t, ok)
	}

	assert.Nil(t, th.plan.FindWork())
}

type FakeCommandRunner struct {
	t              *testing.T
	commandsRan    []string
	activeEdges    []*graph.Edge
	maxActiveEdges int
	fs             *disk.MockDiskInterface
}

func newFakeCommandRunner(t *testing.T, vfs *disk.MockDiskInterface) *FakeCommandRunner {
	return &FakeCommandRunner{
		t:              t,
		commandsRan:    make([]string, 0),
		activeEdges:    make([]*graph.Edge, 0),
		maxActiveEdges: 1,
		fs:             vfs,
	}
}

func (d *FakeCommandRunner) ClearJobTokens() {}

func (d *FakeCommandRunner) CanRunMore() int {
	if len(d.activeEdges) < d.maxActiveEdges {
		return math.MaxInt
	}
	return 0
}

func (d *FakeCommandRunner) StartCommand(edge *graph.Edge) error {
	require.True(d.t, len(d.activeEdges) < d.maxActiveEdges)
	require.NotContains(d.t, d.activeEdges, edge)
	d.commandsRan = append(d.commandsRan, edge.EvaluateCommand(false))
	switch edge.Rule().Name() {
	case "cat", "cat_rsp", "cat_rsp_out", "cc", "cp_multi_msvc", "cp_multi_gcc", "touch", "touch-interrupt", "touch-fail-tick2":
		for _, out := range edge.Outputs() {
			d.fs.WriteFile(out.Path(), []byte{})
		}
	case "true", "fail", "interrupt", "console":
		// don't do anything
	case "cp":
		require.NotEmpty(d.t, edge.Inputs())
		require.Len(d.t, edge.Outputs(), 1)
		if buf, err := d.fs.ReadFile(edge.Inputs()[0].Path()); err == nil {
			d.fs.WriteFile(edge.Outputs()[0].Path(), buf)
		}
	case "touch-implicit-dep-out":
		dep := edge.GetBinding("test_dependency")
		d.fs.Tick()
		d.fs.WriteFile(dep, []byte{})
		d.fs.Tick()
		for _, out := range edge.Outputs() {
			d.fs.WriteFile(out.Path(), []byte{})
		}
	case "touch-out-implicit-dep":
		dep := edge.GetBinding("test_dependency")
		for _, out := range edge.Outputs() {
			d.fs.WriteFile(out.Path(), []byte{})
		}
		d.fs.Tick()
		d.fs.WriteFile(dep, []byte{})
	case "generate-depfile":
		dep := edge.GetBinding("test_dependency")
		touchDep := edge.GetBindingBool("touch_dependency")
		depfile := edge.GetUnescapedDepfile()
		if touchDep {
			d.fs.Tick()
			d.fs.WriteFile(dep, []byte{})
		}
		var contents string
		for _, out := range edge.Outputs() {
			contents += out.Path() + ": " + dep + "\n"
			d.fs.WriteFile(out.Path(), []byte{})
		}
		d.fs.WriteFile(depfile, []byte(contents))
	case "long-cc":
		dep := edge.GetBinding("test_dependency")
		depfile := edge.GetUnescapedDepfile()
		var contents string
		for _, out := range edge.Outputs() {
			d.fs.Tick()
			d.fs.Tick()
			d.fs.Tick()
			d.fs.WriteFile(out.Path(), []byte{})
			contents += out.Path() + ": " + dep + "\n"
		}
		if dep != "" && depfile != "" {
			d.fs.WriteFile(depfile, []byte(contents))
		}
	default:
		fmt.Printf("unknown command\n")
		return fmt.Errorf("unknown command\n")
	}

	d.activeEdges = append(d.activeEdges, edge)

	// Allow tests to control the order by the name of the first output.
	slices.SortFunc(d.activeEdges, CompareEdgesByOutput)
	return nil
}

func (d *FakeCommandRunner) WaitForCommand() *build.Result {
	if len(d.activeEdges) == 0 {
		return nil
	}

	// All active edges were already completed immediately when started,
	// so we can pick any edge here.  Pick the last edge.  Tests can
	// control the order of edges by the name of the first output.
	edge := d.activeEdges[len(d.activeEdges)-1]
	r := &build.Result{
		Edge: edge,
	}

	if edge.Rule().Name() == "interrupt" || edge.Rule().Name() == "touch-interrupt" {
		r.Status = exit_status.ExitInterrupted
		return r
	}

	if edge.Rule().Name() == "console" {
		if edge.UseConsole() {
			r.Status = exit_status.ExitSuccess
		} else {
			r.Status = exit_status.ExitFailure
		}
		d.activeEdges = slices.DeleteFunc(d.activeEdges, func(e *graph.Edge) bool {
			return e == edge
		})
		return r
	}

	if edge.Rule().Name() == "cp_multi_msvc" {
		prefix := edge.GetBinding("msvc_deps_prefix")
		for _, in := range edge.Inputs() {
			r.Output += prefix + in.Path() + "\n"
		}
	}

	if edge.Rule().Name() == "fail" || (edge.Rule().Name() == "touch-fail-tick2" && d.fs.Now() == 2) {
		r.Status = exit_status.ExitFailure
	} else {
		r.Status = exit_status.ExitSuccess
	}

	// This rule simulates an external process modifying files while the build command runs.
	// See TestInputMtimeRaceCondition and TestInputMtimeRaceConditionWithDepFile.
	// Note: only the first and third time the rule is run per test is the file modified, so
	// the test can verify that subsequent runs without the race have no work to do.
	if edge.Rule().Name() == "long-cc" {
		dep := edge.GetBinding("test_dependency")
		if d.fs.Now() == 4 {
			d.fs.SetMtime(dep, 3)
		}
		if d.fs.Now() == 10 {
			d.fs.SetMtime(dep, 9)
		}
	}

	// Provide a way for test cases to verify when an edge finishes that
	// some other edge is still active.  This is useful for test cases
	// covering behavior involving multiple active edges.
	verifyActiveEdge := edge.GetBinding("verify_active_edge")
	if verifyActiveEdge != "" {
		verifyActiveEdgeFound := false
		for _, edge := range d.activeEdges {
			if len(edge.Outputs()) > 0 && edge.Outputs()[0].Path() == verifyActiveEdge {
				verifyActiveEdgeFound = true
			}
		}
		require.True(d.t, verifyActiveEdgeFound)
	}

	d.activeEdges = slices.DeleteFunc(d.activeEdges, func(e *graph.Edge) bool {
		return e == edge
	})

	return r
}

func (d *FakeCommandRunner) GetActiveEdges() []*graph.Edge {
	return d.activeEdges
}

func (d *FakeCommandRunner) Abort() {
	d.activeEdges = d.activeEdges[:0]
}

type buildTestHelper struct {
	*stateTestWithBuiltinRulesHelper

	config        build_config.Config
	commandRunner *FakeCommandRunner
	fs            *disk.MockDiskInterface
	status        *status.StatusPrinter
	builder       *build.Builder
}

func makeConfig() build_config.Config {
	config := build_config.Create()
	config.Verbosity = build_config.Quiet
	return config
}

func newBuildTestHelper(t *testing.T) *buildTestHelper {
	return newBuildTestHelperWithDepsLog(t, nil)
}

func newBuildTestHelperWithDepsLog(t *testing.T, log *deps_log.DepsLog) *buildTestHelper {
	t.Helper()

	planHelper := newStateTestWithBuiltinRulesHelper(t)
	conf := makeConfig()
	fs := disk.NewMockDiskInterface()
	st := status.NewPrinter(conf)
	th := &buildTestHelper{
		stateTestWithBuiltinRulesHelper: planHelper,
		config:                          conf,
		commandRunner:                   newFakeCommandRunner(t, fs),
		fs:                              fs,
		status:                          st,
		builder:                         build.NewBuilder(planHelper.state, conf, nil, log, fs, st, 0),
	}
	th.SetUp()
	return th
}

func (h *buildTestHelper) SetUp() {
	h.builder.TestOnlySetCommandRunner(h.commandRunner)
	test.AssertParse(h.t, `
build cat1: cat in1
build cat2: cat in1 in2
build cat12: cat cat1 cat2
`, h.stateTestWithBuiltinRulesHelper.state)

	h.fs.WriteFile("in1", []byte{})
	h.fs.WriteFile("in2", []byte{})
}

func (h *buildTestHelper) IsPathDead(_ string) bool {
	return false
}

func (h *buildTestHelper) RebuildTarget(target, manifest, logPath, depsPath string, st *state.State) {
	t := h.stateTestWithBuiltinRulesHelper.t
	pstate := state.New()
	if st != nil {
		pstate = st
	}

	test.AddCatRule(t, pstate)
	test.AssertParse(t, manifest, pstate)

	var pbuildLog *build_log.BuildLog
	if logPath != "" {
		buildLog := build_log.NewBuildLog()
		assert.NoError(t, buildLog.Load(logPath))
		assert.NoError(t, buildLog.OpenForWrite(logPath, h))
		pbuildLog = buildLog
	}

	var pdepsLog *deps_log.DepsLog
	if depsPath != "" {
		depsLog := deps_log.NewDepsLog()
		assert.NoError(t, depsLog.Load(depsPath, pstate))
		assert.NoError(t, depsLog.OpenForWrite(depsPath))
		pdepsLog = depsLog
	}

	builder := build.NewBuilder(pstate, h.config, pbuildLog, pdepsLog, h.fs, h.status, 0)
	_, err := builder.AddTargetByName(target)
	require.NoError(t, err)

	h.commandRunner.commandsRan = h.commandRunner.commandsRan[:0]
	builder.TestOnlySetCommandRunner(h.commandRunner)
	if !builder.AlreadyUpToDate() {
		buildRes, err := builder.Build()
		require.NoError(t, err)
		require.Equal(t, exit_status.ExitSuccess, buildRes)
	}
}

func (h *buildTestHelper) Dirty(path string) {
	node := h.stateTestWithBuiltinRulesHelper.state.GetNode(path)
	node.MarkDirty()

	// If it's an input file, mark that we've already stat()ed it and
	// it's missing.
	if node.InEdge() == nil {
		node.MarkMissing()
	}
}

func TestNoWork(t *testing.T) {
	th := newBuildTestHelper(t)
	require.True(t, th.builder.AlreadyUpToDate())
}

func TestOneStep(t *testing.T) {
	// Given a dirty target with one ready input,
	// we should rebuild the target.
	th := newBuildTestHelper(t)
	th.Dirty("cat1")

	_, err := th.builder.AddTargetByName("cat1")
	require.NoError(t, err)

	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)

	assert.Equal(t, 1, len(th.commandRunner.commandsRan))
	require.Equal(t, "cat in1 > cat1", th.commandRunner.commandsRan[0])
}

func TestOneStep2(t *testing.T) {
	// Given a target with one dirty input,
	// we should rebuild the target.
	th := newBuildTestHelper(t)
	th.Dirty("cat1")

	_, err := th.builder.AddTargetByName("cat1")
	require.NoError(t, err)

	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)

	assert.Equal(t, 1, len(th.commandRunner.commandsRan))
	require.Equal(t, "cat in1 > cat1", th.commandRunner.commandsRan[0])
}

func TestTwoStep(t *testing.T) {
	th := newBuildTestHelper(t)
	_, err := th.builder.AddTargetByName("cat12")
	require.NoError(t, err)
	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)

	assert.Equal(t, 3, len(th.commandRunner.commandsRan))

	// Depending on how the pointers work out, we could've ran
	// the first two commands in either order.
	require.Contains(t, th.commandRunner.commandsRan[:2], "cat in1 > cat1")
	require.Contains(t, th.commandRunner.commandsRan[:2], "cat in1 in2 > cat2")

	require.Equal(t, th.commandRunner.commandsRan[2], "cat cat1 cat2 > cat12")

	th.fs.Tick()

	// Modifying in2 requires rebuilding one intermediate file
	// and the final file.
	th.fs.WriteFile("in2", []byte{})
	th.state.Reset()

	_, err = th.builder.AddTargetByName("cat12")
	require.NoError(t, err)
	buildRes, err = th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	assert.Equal(t, 5, len(th.commandRunner.commandsRan))
	require.Equal(t, th.commandRunner.commandsRan[3], "cat in1 in2 > cat2")
	require.Equal(t, th.commandRunner.commandsRan[4], "cat cat1 cat2 > cat12")
}

func TestTwoOutputs(t *testing.T) {
	th := newBuildTestHelper(t)
	test.AssertParse(t, `
rule touch
  command = touch $out
build out1 out2: touch in.txt
`, th.state)
	th.fs.WriteFile("in.txt", []byte{})

	_, err := th.builder.AddTargetByName("out1")
	require.NoError(t, err)
	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	assert.Equal(t, 1, len(th.commandRunner.commandsRan))
	require.Equal(t, th.commandRunner.commandsRan[0], "touch out1 out2")
}

func TestImplicitOutput(t *testing.T) {
	th := newBuildTestHelper(t)
	test.AssertParse(t, `
rule touch
  command = touch $out $out.imp
build out | out.imp: touch in.txt
`, th.state)
	th.fs.WriteFile("in.txt", []byte{})

	_, err := th.builder.AddTargetByName("out.imp")
	require.NoError(t, err)
	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	assert.Equal(t, 1, len(th.commandRunner.commandsRan))
	require.Equal(t, "touch out out.imp", th.commandRunner.commandsRan[0])
}

func TestMultiOutIn(t *testing.T) {
	th := newBuildTestHelper(t)
	test.AssertParse(t, `
rule touch
  command = touch $out
build in1 otherfile: touch in
build out: touch in | in1
`, th.state)

	th.fs.WriteFile("in", []byte{})
	th.fs.Tick()
	th.fs.WriteFile("in1", []byte{})

	_, err := th.builder.AddTargetByName("out")
	require.NoError(t, err)
	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
}

func TestChain(t *testing.T) {
	th := newBuildTestHelper(t)
	test.AssertParse(t, `
build c2: cat c1
build c3: cat c2
build c4: cat c3
build c5: cat c4
`, th.state)

	th.fs.WriteFile("c1", []byte{})

	_, err := th.builder.AddTargetByName("c5")
	require.NoError(t, err)
	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	assert.Equal(t, 4, len(th.commandRunner.commandsRan))

	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()
	_, err = th.builder.AddTargetByName("c5")
	require.NoError(t, err)
	assert.True(t, th.builder.AlreadyUpToDate())

	th.fs.Tick()

	th.fs.WriteFile("c3", []byte{})
	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()
	_, err = th.builder.AddTargetByName("c5")
	require.NoError(t, err)
	assert.False(t, th.builder.AlreadyUpToDate())
	buildRes, err = th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	assert.Equal(t, 2, len(th.commandRunner.commandsRan)) // 3->4, 4->5
}

func TestMissingInput(t *testing.T) {
	// Input is referenced by build file, but no rule for it.
	th := newBuildTestHelper(t)
	th.Dirty("in1")
	_, err := th.builder.AddTargetByName("cat1")
	require.Error(t, err)
	assert.Equal(t, "'in1', needed by 'cat1', missing and no known rule to make it", err.Error())
}

func TestMissingTarget(t *testing.T) {
	// Target is not referenced by build file.
	th := newBuildTestHelper(t)
	_, err := th.builder.AddTargetByName("meow")
	require.Error(t, err)
	assert.Equal(t, "unknown target: 'meow'", err.Error())
}

func TestMakeDirs(t *testing.T) {
	th := newBuildTestHelper(t)
	if runtime.GOOS == "windows" {
		test.AssertParse(t, "build subdir\\dir2\\file: cat in1\n", th.state)
	} else {
		test.AssertParse(t, "build subdir/dir2/file: cat in1\n", th.state)
	}

	_, err := th.builder.AddTargetByName("subdir/dir2/file")
	require.NoError(t, err)
	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.Equal(t, 2, len(th.fs.DirectoriesMade()))
	assert.Equal(t, "subdir", th.fs.DirectoriesMade()[0])
	assert.Equal(t, "subdir/dir2", th.fs.DirectoriesMade()[1])
}

func TestDepFileMissing(t *testing.T) {
	th := newBuildTestHelper(t)
	test.AssertParse(t, `
rule cc
  command = cc $in
  depfile = $out.d
build fo$ o.o: cc foo.c
`, th.state)
	th.fs.WriteFile("foo.c", []byte{})

	_, err := th.builder.AddTargetByName("fo o.o")
	require.NoError(t, err)
	assert.Equal(t, 1, len(th.fs.FilesRead()))
	assert.Equal(t, "fo o.o.d", th.fs.FilesRead()[0])
}

func TestDepFileOK(t *testing.T) {
	th := newBuildTestHelper(t)
	origEdges := len(th.state.Edges())
	test.AssertParse(t, `
rule cc
  command = cc $in
  depfile = $out.d
build foo.o: cc foo.c
`, th.state)
	edge := th.state.Edges()[len(th.state.Edges())-1]

	th.fs.WriteFile("foo.c", []byte{})
	th.state.GetNode("bar.h").MarkDirty() // Mark bar.h as missing.
	th.fs.WriteFile("foo.o.d", []byte("foo.o: blah.h bar.h\n"))
	_, err := th.builder.AddTargetByName("foo.o")
	require.NoError(t, err)
	assert.Equal(t, 1, len(th.fs.FilesRead()))
	assert.Equal(t, "foo.o.d", th.fs.FilesRead()[0])

	// Expect one new edge generating foo.o. Loading the depfile should have
	// added nodes, but not phony edges to the graph.
	assert.Equal(t, origEdges+1, len(th.state.Edges()))

	// Verify that nodes for blah.h and bar.h were added and that they
	// are marked as generated by a dep loader.
	assert.False(t, th.state.LookupNode("foo.o").GeneratedByDepLoader())
	assert.False(t, th.state.LookupNode("foo.c").GeneratedByDepLoader())
	assert.NotNil(t, th.state.LookupNode("blah.h"))
	assert.True(t, th.state.LookupNode("blah.h").GeneratedByDepLoader())
	assert.NotNil(t, th.state.LookupNode("bar.h"))
	assert.True(t, th.state.LookupNode("bar.h").GeneratedByDepLoader())

	// Expect our edge to now have three inputs: foo.c and two headers.
	assert.Equal(t, 3, len(edge.Inputs()))

	edge.Dump("test")

	// Expect the command line we generate to only use the original input.
	assert.Equal(t, "cc foo.c", edge.EvaluateCommand(false))
}

func TestDepFileParseError(t *testing.T) {
	th := newBuildTestHelper(t)
	test.AssertParse(t, `
rule cc
  command = cc $in
  depfile = $out.d
build foo.o: cc foo.c
`, th.state)
	th.fs.WriteFile("foo.c", []byte{})
	th.fs.WriteFile("foo.o.d", []byte("randomtext\n"))
	_, err := th.builder.AddTargetByName("foo.o")
	require.Error(t, err)
	assert.Equal(t, "foo.o.d: expected ':' in depfile", err.Error())
}
