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
	"github.com/buildbuddy-io/gin/internal/timestamp"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type planTestHelper struct {
	t     *testing.T
	state *state.State
	plan  *build.Plan
}

func newPlanTestHelper(t *testing.T) *planTestHelper {
	t.Helper()
	s := state.New()
	test.AddCatRule(t, s)

	return &planTestHelper{
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

func (h *planTestHelper) FindWorkSorted(count int) []*graph.Edge {
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

func (h *planTestHelper) PrepareForTarget(nodeName string) {
	h.t.Helper()
	ok, err := h.plan.AddTarget(h.state.GetNode(nodeName))
	require.NoError(h.t, err)
	require.True(h.t, ok)

	h.plan.PrepareQueue()

	assert.True(h.t, h.plan.MoreToDo())
}

func TestBasic(t *testing.T) {
	th := newPlanTestHelper(t)
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
	th := newPlanTestHelper(t)
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
	th := newPlanTestHelper(t)
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
	th := newPlanTestHelper(t)
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

func (h *planTestHelper) TestPoolWithDepthOne(testCase string) {
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
	th := newPlanTestHelper(t)
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
	th := newPlanTestHelper(t)
	th.TestPoolWithDepthOne(`
rule poolcat
  command = cat $in > $out
  pool = console
build out1: poolcat in
build out2: poolcat in
`)
}

func TestPoolsWithDepthTwo(t *testing.T) {
	th := newPlanTestHelper(t)
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
	th := newPlanTestHelper(t)
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
	th := newPlanTestHelper(t)
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
	th := newPlanTestHelper(t)
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
			d.fs.Create(out.Path(), []byte{})
		}
	case "true", "fail", "interrupt", "console":
		// don't do anything
	case "cp":
		require.NotEmpty(d.t, edge.Inputs())
		require.Len(d.t, edge.Outputs(), 1)
		if buf, err := d.fs.ReadFile(edge.Inputs()[0].Path()); err == nil {
			d.fs.Create(edge.Outputs()[0].Path(), buf)
		}
	case "touch-implicit-dep-out":
		dep := edge.GetBinding("test_dependency")
		d.fs.Tick()
		d.fs.Create(dep, []byte{})
		d.fs.Tick()
		for _, out := range edge.Outputs() {
			d.fs.Create(out.Path(), []byte{})
		}
	case "touch-out-implicit-dep":
		dep := edge.GetBinding("test_dependency")
		for _, out := range edge.Outputs() {
			d.fs.Create(out.Path(), []byte{})
		}
		d.fs.Tick()
		d.fs.Create(dep, []byte{})
	case "generate-depfile":
		dep := edge.GetBinding("test_dependency")
		touchDep := edge.GetBindingBool("touch_dependency")
		depfile := edge.GetUnescapedDepfile()
		if touchDep {
			d.fs.Tick()
			d.fs.Create(dep, []byte{})
		}
		var contents string
		for _, out := range edge.Outputs() {
			contents += out.Path() + ": " + dep + "\n"
			d.fs.Create(out.Path(), []byte{})
		}
		d.fs.Create(depfile, []byte(contents))
	case "long-cc":
		dep := edge.GetBinding("test_dependency")
		depfile := edge.GetUnescapedDepfile()
		var contents string
		for _, out := range edge.Outputs() {
			d.fs.Tick()
			d.fs.Tick()
			d.fs.Tick()
			d.fs.Create(out.Path(), []byte{})
			contents += out.Path() + ": " + dep + "\n"
		}
		if dep != "" && depfile != "" {
			d.fs.Create(depfile, []byte(contents))
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
	planTestHelper *planTestHelper

	t             *testing.T
	state         *state.State
	config        *build_config.Config
	commandRunner *FakeCommandRunner
	fs            *disk.MockDiskInterface
	status        *status.StatusPrinter
	builder       *build.Builder
	buildLog      *build_log.BuildLog
}

func makeConfig() *build_config.Config {
	config := build_config.Create()
	config.Verbosity = build_config.Quiet
	return config
}

func newBuildTestHelper(t *testing.T) *buildTestHelper {
	return newBuildTestHelperWithLogs(t, nil, nil)
}

func newBuildTestHelperWithBuildLog(t *testing.T) *buildTestHelper {
	buildLog := build_log.NewBuildLog()
	return newBuildTestHelperWithLogs(t, buildLog, nil)
}

func newBuildTestHelperWithLogs(t *testing.T, buildLog *build_log.BuildLog, depsLog *deps_log.DepsLog) *buildTestHelper {
	t.Helper()

	planHelper := newPlanTestHelper(t)
	conf := makeConfig()
	fs := disk.NewMockDiskInterface()
	st := status.NewPrinter(conf)
	th := &buildTestHelper{
		t:              t,
		planTestHelper: planHelper,
		state:          planHelper.state,
		config:         conf,
		commandRunner:  newFakeCommandRunner(t, fs),
		fs:             fs,
		status:         st,
		builder:        build.NewBuilder(planHelper.state, conf, buildLog, depsLog, fs, st, 0),
		buildLog:       buildLog,
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
`, h.planTestHelper.state)

	h.fs.Create("in1", []byte{})
	h.fs.Create("in2", []byte{})
}

func (h *buildTestHelper) IsPathDead(_ string) bool {
	return false
}

func (h *buildTestHelper) RebuildTarget(target, manifest, logPath, depsPath string, st *state.State) {
	t := h.planTestHelper.t
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
	node := h.planTestHelper.state.GetNode(path)
	node.MarkDirty()

	// If it's an input file, mark that we've already stat()ed it and
	// it's missing.
	if node.InEdge() == nil {
		node.MarkMissing()
	}
}

func assertHash(t *testing.T, expectedCommand string, actualHash uint64) {
	t.Helper()
	expectedHash := build_log.HashCommand(expectedCommand)
	require.Equal(t, expectedHash, actualHash,
		"Command hash mismatch: expected hash of %q (%x) but got %x",
		expectedCommand, expectedHash, actualHash)
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
	th.fs.Create("in2", []byte{})
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
	th.fs.Create("in.txt", []byte{})

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
	th.fs.Create("in.txt", []byte{})

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

	th.fs.Create("in", []byte{})
	th.fs.Tick()
	th.fs.Create("in1", []byte{})

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

	th.fs.Create("c1", []byte{})

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

	th.fs.Create("c3", []byte{})
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
	th.fs.Create("foo.c", []byte{})

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

	th.fs.Create("foo.c", []byte{})
	th.state.GetNode("bar.h").MarkDirty() // Mark bar.h as missing.
	th.fs.Create("foo.o.d", []byte("foo.o: blah.h bar.h\n"))
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
	th.fs.Create("foo.c", []byte{})
	th.fs.Create("foo.o.d", []byte("randomtext\n"))
	_, err := th.builder.AddTargetByName("foo.o")
	require.Error(t, err)
	assert.Equal(t, "foo.o.d: expected ':' in depfile", err.Error())
}

func TestEncounterReadyTwice(t *testing.T) {
	th := newBuildTestHelper(t)
	test.AssertParse(t, `
rule touch
  command = touch $out
build c: touch
build b: touch || c
build a: touch | b || c
`, th.state)

	cOut := th.state.GetNode("c").OutEdges()
	require.Equal(t, 2, len(cOut))
	assert.Equal(t, "b", cOut[0].Outputs()[0].Path())
	assert.Equal(t, "a", cOut[1].Outputs()[0].Path())

	th.fs.Create("b", []byte{})
	_, err := th.builder.AddTargetByName("a")
	require.NoError(t, err)

	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	assert.Equal(t, 2, len(th.commandRunner.commandsRan))
}

func TestRebuildOrderOnlyDeps(t *testing.T) {
	th := newBuildTestHelper(t)
	test.AssertParse(t, `
rule cc
  command = cc $in
rule true
  command = true
build oo.h: cc oo.h.in
build foo.o: cc foo.c || oo.h
`, th.state)

	th.fs.Create("foo.c", []byte{})
	th.fs.Create("oo.h.in", []byte{})

	// foo.o and order-only dep dirty, build both.
	_, err := th.builder.AddTargetByName("foo.o")
	require.NoError(t, err)
	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	assert.Equal(t, 2, len(th.commandRunner.commandsRan))

	// all clean, no rebuild.
	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()
	_, err = th.builder.AddTargetByName("foo.o")
	require.NoError(t, err)
	assert.True(t, th.builder.AlreadyUpToDate())

	// order-only dep missing, build it only.
	th.fs.RemoveFile("oo.h")
	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()
	_, err = th.builder.AddTargetByName("foo.o")
	require.NoError(t, err)
	buildRes, err = th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	assert.Equal(t, 1, len(th.commandRunner.commandsRan))
	assert.Equal(t, "cc oo.h.in", th.commandRunner.commandsRan[0])

	th.fs.Tick()

	// order-only dep dirty, build it only.
	th.fs.Create("oo.h.in", []byte{})
	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()
	_, err = th.builder.AddTargetByName("foo.o")
	require.NoError(t, err)
	buildRes, err = th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	assert.Equal(t, 1, len(th.commandRunner.commandsRan))
	assert.Equal(t, "cc oo.h.in", th.commandRunner.commandsRan[0])
}

func TestPhony(t *testing.T) {
	th := newBuildTestHelper(t)
	test.AssertParse(t, `
build out: cat bar.cc
build all: phony out
`, th.state)
	th.fs.Create("bar.cc", []byte{})

	_, err := th.builder.AddTargetByName("all")
	require.NoError(t, err)

	// Only one command to run, because phony runs no command.
	assert.False(t, th.builder.AlreadyUpToDate())
	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	assert.Equal(t, 1, len(th.commandRunner.commandsRan))
}

func TestPhonyNoWork(t *testing.T) {
	th := newBuildTestHelper(t)
	test.AssertParse(t, `
build out: cat bar.cc
build all: phony out
`, th.state)
	th.fs.Create("bar.cc", []byte{})
	th.fs.Create("out", []byte{})

	_, err := th.builder.AddTargetByName("all")
	require.NoError(t, err)
	assert.True(t, th.builder.AlreadyUpToDate())
}

// Test a self-referencing phony. Ideally this should not work, but
// ninja 1.7 and below tolerated and CMake 2.8.12.x and 3.0.x both
// incorrectly produce it. We tolerate it for compatibility.
func TestPhonySelfReference(t *testing.T) {
	th := newBuildTestHelper(t)
	test.AssertParse(t, `
build a: phony a
`, th.state)

	_, err := th.builder.AddTargetByName("a")
	require.NoError(t, err)
	assert.True(t, th.builder.AlreadyUpToDate())
}

// There are 6 different cases for phony rules:
//
// 1. output edge does not exist, inputs are not real
// 2. output edge does not exist, no inputs
// 3. output edge does not exist, inputs are real, newest mtime is M
// 4. output edge is real, inputs are not real
// 5. output edge is real, no inputs
// 6. output edge is real, inputs are real, newest mtime is M
//
// Expected results :
// 1. Edge is marked as clean, mtime is newest mtime of dependents.
//     Touching inputs will cause dependents to rebuild.
// 2. Edge is marked as dirty, causing dependent edges to always rebuild
// 3. Edge is marked as clean, mtime is newest mtime of dependents.
//     Touching inputs will cause dependents to rebuild.
// 4. Edge is marked as clean, mtime is newest mtime of dependents.
//     Touching inputs will cause dependents to rebuild.
// 5. Edge is marked as dirty, causing dependent edges to always rebuild
// 6. Edge is marked as clean, mtime is newest mtime of dependents.
//     Touching inputs will cause dependents to rebuild.

func (h *buildTestHelper) TestPhonyUseCase(i int) {
	h.t.Helper()

	test.AssertParse(h.t, `
rule touch
  command = touch $out
build notreal: phony blank
build phony1: phony notreal
build phony2: phony
build phony3: phony blank
build phony4: phony notreal
build phony5: phony
build phony6: phony blank

build test1: touch phony1
build test2: touch phony2
build test3: touch phony3
build test4: touch phony4
build test5: touch phony5
build test6: touch phony6
`, h.state)

	// Set up test.
	h.fs.Create("blank", []byte{}) // a "real" file
	_, err := h.builder.AddTargetByName("test1")
	require.NoError(h.t, err)
	_, err = h.builder.AddTargetByName("test2")
	require.NoError(h.t, err)
	_, err = h.builder.AddTargetByName("test3")
	require.NoError(h.t, err)
	_, err = h.builder.AddTargetByName("test4")
	require.NoError(h.t, err)
	_, err = h.builder.AddTargetByName("test5")
	require.NoError(h.t, err)
	_, err = h.builder.AddTargetByName("test6")
	require.NoError(h.t, err)

	buildRes, err := h.builder.Build()
	require.NoError(h.t, err)
	require.Equal(h.t, exit_status.ExitSuccess, buildRes)

	ci := fmt.Sprintf("%d", i)

	// Tests 1, 3, 4, and 6 should rebuild when the input is updated.
	if i != 2 && i != 5 {
		testNode := h.state.GetNode("test" + ci)
		phonyNode := h.state.GetNode("phony" + ci)

		startTime := h.fs.Now() - 1

		var inputNode *graph.Node
		if i == 1 || i == 4 {
			inputNode = h.state.GetNode("notreal")
		} else {
			inputNode = h.state.GetNode("blank")
		}

		h.state.Reset()
		h.commandRunner.commandsRan = h.commandRunner.commandsRan[:0]
		h.fs.Tick()
		h.commandRunner.commandsRan = h.commandRunner.commandsRan[:0]
		h.fs.Create("blank", []byte{}) // a "real" file
		_, err = h.builder.AddTargetByName("test" + ci)
		require.NoError(h.t, err)

		// Second build, expect testN edge to be rebuilt
		// and phonyN node's mtime to be updated.
		require.False(h.t, h.builder.AlreadyUpToDate())
		buildRes, err := h.builder.Build()
		require.NoError(h.t, err)
		require.Equal(h.t, exit_status.ExitSuccess, buildRes)
		require.Len(h.t, h.commandRunner.commandsRan, 1)
		assert.Equal(h.t, "touch test"+ci, h.commandRunner.commandsRan[0])
		require.True(h.t, h.builder.AlreadyUpToDate())

		inputTime := inputNode.Mtime()

		assert.False(h.t, phonyNode.Exists())
		assert.False(h.t, phonyNode.Dirty())

		assert.Greater(h.t, phonyNode.Mtime(), startTime)
		assert.Equal(h.t, phonyNode.Mtime(), inputTime)
		err = testNode.Stat(h.fs)
		require.NoError(h.t, err)
		assert.True(h.t, testNode.Exists())
		assert.Greater(h.t, testNode.Mtime(), startTime)
	} else {
		// Tests 2 and 5: Expect dependents to always rebuild.

		h.state.Reset()
		h.commandRunner.commandsRan = h.commandRunner.commandsRan[:0]
		h.fs.Tick()
		h.commandRunner.commandsRan = h.commandRunner.commandsRan[:0]
		_, err = h.builder.AddTargetByName("test" + ci)
		require.NoError(h.t, err)
		require.False(h.t, h.builder.AlreadyUpToDate())
		buildRes, err := h.builder.Build()
		require.NoError(h.t, err)
		require.Equal(h.t, exit_status.ExitSuccess, buildRes)
		require.Len(h.t, h.commandRunner.commandsRan, 1)
		assert.Equal(h.t, "touch test"+ci, h.commandRunner.commandsRan[0])

		h.state.Reset()
		h.commandRunner.commandsRan = h.commandRunner.commandsRan[:0]
		_, err = h.builder.AddTargetByName("test" + ci)
		require.NoError(h.t, err)
		require.False(h.t, h.builder.AlreadyUpToDate())
		buildRes, err = h.builder.Build()
		require.NoError(h.t, err)
		require.Equal(h.t, exit_status.ExitSuccess, buildRes)
		require.Len(h.t, h.commandRunner.commandsRan, 1)
		assert.Equal(h.t, "touch test"+ci, h.commandRunner.commandsRan[0])
	}
}

func TestPhonyUseCase1(t *testing.T) {
	th := newBuildTestHelper(t)
	th.TestPhonyUseCase(1)
}

func TestPhonyUseCase2(t *testing.T) {
	th := newBuildTestHelper(t)
	th.TestPhonyUseCase(2)
}

func TestPhonyUseCase3(t *testing.T) {
	th := newBuildTestHelper(t)
	th.TestPhonyUseCase(3)
}

func TestPhonyUseCase4(t *testing.T) {
	th := newBuildTestHelper(t)
	th.TestPhonyUseCase(4)
}

func TestPhonyUseCase5(t *testing.T) {
	th := newBuildTestHelper(t)
	th.TestPhonyUseCase(5)
}

func TestPhonyUseCase6(t *testing.T) {
	th := newBuildTestHelper(t)
	th.TestPhonyUseCase(6)
}

func TestFail(t *testing.T) {
	th := newBuildTestHelper(t)
	test.AssertParse(t, `
rule fail
  command = fail
build out1: fail
`, th.state)

	_, err := th.builder.AddTargetByName("out1")
	require.NoError(t, err)

	buildRes, err := th.builder.Build()
	require.Error(t, err)
	require.Equal(t, exit_status.ExitFailure, buildRes)
	require.Len(t, th.commandRunner.commandsRan, 1)
	assert.Equal(t, "subcommand failed", err.Error())
}

func TestSwallowFailures(t *testing.T) {
	th := newBuildTestHelper(t)
	test.AssertParse(t, `
rule fail
  command = fail
build out1: fail
build out2: fail
build out3: fail
build all: phony out1 out2 out3
`, th.state)

	// Swallow two failures, die on the third.
	th.config.FailuresAllowed = 3

	_, err := th.builder.AddTargetByName("all")
	require.NoError(t, err)

	buildRes, err := th.builder.Build()
	require.Error(t, err)
	require.Equal(t, exit_status.ExitFailure, buildRes)
	require.Len(t, th.commandRunner.commandsRan, 3)
	assert.Equal(t, "subcommands failed", err.Error())
}

func TestSwallowFailuresLimit(t *testing.T) {
	th := newBuildTestHelper(t)
	test.AssertParse(t, `
rule fail
  command = fail
build out1: fail
build out2: fail
build out3: fail
build final: cat out1 out2 out3
`, th.state)

	// Swallow ten failures; we should stop before building final.
	th.config.FailuresAllowed = 11

	_, err := th.builder.AddTargetByName("final")
	require.NoError(t, err)

	buildRes, err := th.builder.Build()
	require.Error(t, err)
	require.Equal(t, exit_status.ExitFailure, buildRes)
	require.Len(t, th.commandRunner.commandsRan, 3)
	assert.Equal(t, "cannot make progress due to previous errors", err.Error())
}

func TestSwallowFailuresPool(t *testing.T) {
	th := newBuildTestHelper(t)
	test.AssertParse(t, `
pool failpool
  depth = 1
rule fail
  command = fail
  pool = failpool
build out1: fail
build out2: fail
build out3: fail
build final: cat out1 out2 out3
`, th.state)

	// Swallow ten failures; we should stop before building final.
	th.config.FailuresAllowed = 11

	_, err := th.builder.AddTargetByName("final")
	require.NoError(t, err)

	buildRes, err := th.builder.Build()
	require.Error(t, err)
	require.Equal(t, exit_status.ExitFailure, buildRes)
	require.Len(t, th.commandRunner.commandsRan, 3)
	assert.Equal(t, "cannot make progress due to previous errors", err.Error())
}

func TestPoolEdgesReadyButNotWanted(t *testing.T) {
	th := newBuildTestHelper(t)
	th.fs.Create("x", []byte{})

	manifest := `
pool some_pool
  depth = 4
rule touch
  command = touch $out
  pool = some_pool
rule cc
  command = touch grit

build B.d.stamp: cc | x
build C.stamp: touch B.d.stamp
build final.stamp: touch || C.stamp
`

	th.RebuildTarget("final.stamp", manifest, "", "", nil)

	th.fs.RemoveFile("B.d.stamp")

	saveState := state.New()
	th.RebuildTarget("final.stamp", manifest, "", "", saveState)
	assert.GreaterOrEqual(t, saveState.LookupPool("some_pool").CurrentUse(), 0)
}

func TestImplicitGeneratedOutOfDate(t *testing.T) {
	th := newBuildTestHelperWithBuildLog(t)

	test.AssertParse(t, `
rule touch
  command = touch $out
  generator = 1
build out.imp: touch | in
`, th.state)
	th.fs.Create("out.imp", []byte{})
	th.fs.Tick()
	th.fs.Create("in", []byte{})

	_, err := th.builder.AddTargetByName("out.imp")
	require.NoError(t, err)

	require.False(t, th.builder.AlreadyUpToDate())

	require.True(t, th.state.GetNode("out.imp").Dirty())
}

func TestImplicitGeneratedOutOfDate2(t *testing.T) {
	th := newBuildTestHelperWithBuildLog(t)

	test.AssertParse(t, `
rule touch-implicit-dep-out
  command = touch-implicit-dep-out
  generator = 1
build out.imp: touch-implicit-dep-out | inimp inimp2
  test_dependency = inimp
`, th.state)
	th.fs.Create("inimp", []byte{})
	th.fs.Create("out.imp", []byte{})
	th.fs.Tick()
	th.fs.Create("inimp2", []byte{})
	th.fs.Tick()

	_, err := th.builder.AddTargetByName("out.imp")
	require.NoError(t, err)
	require.False(t, th.builder.AlreadyUpToDate())

	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.True(t, th.builder.AlreadyUpToDate())

	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()
	th.builder.Cleanup()
	th.builder.TestOnlyPlan().Reset()

	_, err = th.builder.AddTargetByName("out.imp")
	require.NoError(t, err)
	require.True(t, th.builder.AlreadyUpToDate())
	require.False(t, th.state.GetNode("out.imp").Dirty())

	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()
	th.builder.Cleanup()
	th.builder.TestOnlyPlan().Reset()

	th.fs.Tick()
	th.fs.Create("inimp", []byte{})

	_, err = th.builder.AddTargetByName("out.imp")
	require.NoError(t, err)
	require.False(t, th.builder.AlreadyUpToDate())

	buildRes, err = th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.True(t, th.builder.AlreadyUpToDate())

	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()
	th.builder.Cleanup()
	th.builder.TestOnlyPlan().Reset()

	_, err = th.builder.AddTargetByName("out.imp")
	require.NoError(t, err)
	require.True(t, th.builder.AlreadyUpToDate())
	require.False(t, th.state.GetNode("out.imp").Dirty())
}

func TestNotInLogButOnDisk(t *testing.T) {
	th := newBuildTestHelperWithBuildLog(t)
	test.AssertParse(t, `
rule cc
  command = cc
build out1: cc in
`, th.state)

	// Create input/output that would be considered up to date when
	// not considering the command line hash.
	th.fs.Create("in", []byte{})
	th.fs.Create("out1", []byte{})

	// Because it's not in the log, it should not be up-to-date until
	// we build again.
	_, err := th.builder.AddTargetByName("out1")
	require.NoError(t, err)
	require.False(t, th.builder.AlreadyUpToDate())

	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()

	_, err = th.builder.AddTargetByName("out1")
	require.NoError(t, err)
	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.True(t, th.builder.AlreadyUpToDate())
}

func TestRebuildAfterFailure(t *testing.T) {
	th := newBuildTestHelperWithBuildLog(t)

	test.AssertParse(t, `
rule touch-fail-tick2
  command = touch-fail-tick2
build out1: touch-fail-tick2 in
`, th.state)

	th.fs.Create("in", []byte{})

	// Run once successfully to get out1 in the log
	_, err := th.builder.AddTargetByName("out1")
	require.NoError(t, err)
	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.Len(t, th.commandRunner.commandsRan, 1)

	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()
	th.builder.Cleanup()
	th.builder.TestOnlyPlan().Reset()

	th.fs.Tick()
	th.fs.Create("in", []byte{})

	// Run again with a failure that updates the output file timestamp
	_, err = th.builder.AddTargetByName("out1")
	require.NoError(t, err)
	buildRes, err = th.builder.Build()
	require.Error(t, err)
	require.Equal(t, exit_status.ExitFailure, buildRes)
	require.Equal(t, "subcommand failed", err.Error())
	require.Len(t, th.commandRunner.commandsRan, 1)

	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()
	th.builder.Cleanup()
	th.builder.TestOnlyPlan().Reset()
	th.fs.Tick()

	// Run again, should rerun even though the output file is up to date on disk
	_, err = th.builder.AddTargetByName("out1")
	require.NoError(t, err)
	require.False(t, th.builder.AlreadyUpToDate())

	buildRes, err = th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.Len(t, th.commandRunner.commandsRan, 1)
}

// TYLER: ALL TESTS AFTER THIS ONE NEED TO BE AUDITED. The tests reference th.plan, which is NOT THE SAME AS builder.plan!!! MAybe make it the same???

func TestRebuildWithNoInputs(t *testing.T) {
	th := newBuildTestHelperWithBuildLog(t)

	test.AssertParse(t, `
rule touch
  command = touch
build out1: touch
build out2: touch in
`, th.state)

	th.fs.Create("in", []byte{})

	_, err := th.builder.AddTargetByName("out1")
	require.NoError(t, err)
	_, err = th.builder.AddTargetByName("out2")
	require.NoError(t, err)
	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.Len(t, th.commandRunner.commandsRan, 2)

	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()

	th.fs.Tick()

	th.fs.Create("in", []byte{})

	_, err = th.builder.AddTargetByName("out1")
	require.NoError(t, err)
	_, err = th.builder.AddTargetByName("out2")
	require.NoError(t, err)
	buildRes, err = th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.Len(t, th.commandRunner.commandsRan, 1)
}

func TestRestatTest(t *testing.T) {
	th := newBuildTestHelperWithBuildLog(t)

	test.AssertParse(t, `
rule true
  command = true
  restat = 1
rule cc
  command = cc
  restat = 1
build out1: cc in
build out2: true out1
build out3: cat out2
`, th.state)

	th.fs.Create("out1", []byte{})
	th.fs.Create("out2", []byte{})
	th.fs.Create("out3", []byte{})

	th.fs.Tick()

	th.fs.Create("in", []byte{})

	// Do a pre-build so that there's commands in the log for the outputs,
	// otherwise, the lack of an entry in the build log will cause out3 to rebuild
	// regardless of restat.
	_, err := th.builder.AddTargetByName("out3")
	require.NoError(t, err)
	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.Len(t, th.commandRunner.commandsRan, 3)
	require.Equal(t, 3, th.builder.TestOnlyPlan().CommandEdgeCount())
	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()

	th.fs.Tick()

	th.fs.Create("in", []byte{})
	// "cc" touches out1, so we should build out2.  But because "true" does not
	// touch out2, we should cancel the build of out3.
	_, err = th.builder.AddTargetByName("out3")
	require.NoError(t, err)
	buildRes, err = th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.Len(t, th.commandRunner.commandsRan, 2)

	// If we run again, it should be a no-op, because the build log has recorded
	// that we've already built out2 with an input timestamp of 2 (from out1).
	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()
	_, err = th.builder.AddTargetByName("out3")
	require.NoError(t, err)
	require.True(t, th.builder.AlreadyUpToDate())

	th.fs.Tick()

	th.fs.Create("in", []byte{})

	// The build log entry should not, however, prevent us from rebuilding out2
	// if out1 changes.
	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()
	_, err = th.builder.AddTargetByName("out3")
	require.NoError(t, err)
	buildRes, err = th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.Len(t, th.commandRunner.commandsRan, 2)
}

func TestRestatMissingFile(t *testing.T) {
	// If a restat rule doesn't create its output, and the output didn't
	// exist before the rule was run, consider that behavior equivalent
	// to a rule that doesn't modify its existent output file.
	th := newBuildTestHelperWithBuildLog(t)
	test.AssertParse(t, `
rule true
  command = true
  restat = 1
rule cc
  command = cc
build out1: true in
build out2: cc out1
`, th.state)

	th.fs.Create("in", []byte{})
	th.fs.Create("out2", []byte{})

	// Do a pre-build so that there's commands in the log for the outputs,
	// otherwise, the lack of an entry in the build log will cause out2 to rebuild
	// regardless of restat.
	_, err := th.builder.AddTargetByName("out2")
	require.NoError(t, err)

	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()

	th.fs.Tick()
	th.fs.Create("in", []byte{})
	th.fs.Create("out2", []byte{})

	// Run a build, expect only the first command to run.
	// It doesn't touch its output (due to being the "true" command), so
	// we shouldn't run the dependent build.
	_, err = th.builder.AddTargetByName("out2")
	require.NoError(t, err)
	buildRes, err = th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.Len(t, th.commandRunner.commandsRan, 1)
}

func TestRestatSingleDependentOutputDirty(t *testing.T) {
	th := newBuildTestHelperWithBuildLog(t)

	test.AssertParse(t, `
rule true
  command = true
  restat = 1
rule touch
  command = touch
build out1: true in
build out2 out3: touch out1
build out4: touch out2
`, th.state)

	// Create the necessary files
	th.fs.Create("in", []byte{})

	_, err := th.builder.AddTargetByName("out4")
	require.NoError(t, err)
	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.Len(t, th.commandRunner.commandsRan, 3)

	th.fs.Tick()
	th.fs.Create("in", []byte{})
	th.fs.RemoveFile("out3")

	// Since "in" is missing, out1 will be built. Since "out3" is missing,
	// out2 and out3 will be built even though "in" is not touched when built.
	// Then, since out2 is rebuilt, out4 should be rebuilt -- the restat on the
	// "true" rule should not lead to the "touch" edge writing out2 and out3 being
	// cleared.
	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()
	_, err = th.builder.AddTargetByName("out4")
	require.NoError(t, err)
	buildRes, err = th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.Len(t, th.commandRunner.commandsRan, 3)
}

func TestRestatMissingInput(t *testing.T) {
	// Test scenario, in which an input file is removed, but output isn't changed
	// https://github.com/ninja-build/ninja/issues/295
	th := newBuildTestHelperWithBuildLog(t)

	test.AssertParse(t, `
rule true
  command = true
  depfile = $out.d
  restat = 1
rule cc
  command = cc
build out1: true in
build out2: cc out1
`, th.state)

	// Create all necessary files
	th.fs.Create("in", []byte{})

	// The implicit dependencies and the depfile itself
	// are newer than the output
	restatMtime := th.fs.Tick()
	th.fs.Create("out1.d", []byte("out1: will.be.deleted restat.file\n"))
	th.fs.Create("will.be.deleted", []byte{})
	th.fs.Create("restat.file", []byte{})

	// Run the build, out1 and out2 get built
	_, err := th.builder.AddTargetByName("out2")
	require.NoError(t, err)
	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.Len(t, th.commandRunner.commandsRan, 2)

	// See that an entry in the logfile is created, capturing
	// the right mtime
	logEntry := th.buildLog.LookupByOutput("out1")
	require.NotNil(t, logEntry)
	require.Equal(t, restatMtime, logEntry.Mtime)

	// Now remove a file, referenced from depfile, so that target becomes
	// dirty, but the output does not change
	th.fs.RemoveFile("will.be.deleted")

	// Trigger the build again - only out1 gets built
	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()
	_, err = th.builder.AddTargetByName("out2")
	require.NoError(t, err)
	buildRes, err = th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.Len(t, th.commandRunner.commandsRan, 1)

	// Check that the logfile entry remains correctly set
	logEntry = th.buildLog.LookupByOutput("out1")
	require.NotNil(t, logEntry)
	require.Equal(t, restatMtime, logEntry.Mtime)
}

func TestRestatInputChangesDueToRule(t *testing.T) {
	th := newBuildTestHelperWithBuildLog(t)

	test.AssertParse(t, `
rule generate-depfile
  command = generate-depfile
build out1: generate-depfile || cat1
  test_dependency = in2
  touch_dependency = 1
  restat = 1
  depfile = out.d
`, th.state)

	// Perform the first build. out1 is a restat rule, so its recorded mtime in the build
	// log should be the time the command completes, not the time the command started. One
	// of out1's discovered dependencies will have a newer mtime than when out1 started
	// running, due to its command touching the dependency itself.
	_, err := th.builder.AddTargetByName("out1")
	require.NoError(t, err)
	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.Len(t, th.commandRunner.commandsRan, 2)
	require.Equal(t, 2, th.builder.TestOnlyPlan().CommandEdgeCount())
	logEntry := th.buildLog.LookupByOutput("out1")
	require.NotNil(t, logEntry)
	require.Equal(t, timestamp.TimeStamp(2), logEntry.Mtime)

	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()
	th.builder.Cleanup()
	th.builder.TestOnlyPlan().Reset()

	th.fs.Tick()
	th.fs.Create("in1", []byte{})

	// Touching a dependency of an order-only dependency of out1 should not cause out1 to
	// rebuild. If out1 were not a restat rule, then it would rebuild here because its
	// recorded mtime would have been an earlier mtime than its most recent input's (in2)
	// mtime
	_, err = th.builder.AddTargetByName("out1")
	require.NoError(t, err)
	require.False(t, th.state.GetNode("out1").Dirty())
	buildRes, err = th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.Len(t, th.commandRunner.commandsRan, 1)
	require.Equal(t, 1, th.builder.TestOnlyPlan().CommandEdgeCount())
}

func TestGeneratedPlainDepfileMtime(t *testing.T) {
	th := newBuildTestHelperWithBuildLog(t)

	test.AssertParse(t, `
rule generate-depfile
  command = generate-depfile
build out: generate-depfile
  test_dependency = inimp
  depfile = out.d
`, th.state)
	th.fs.Create("inimp", []byte{})
	th.fs.Tick()

	_, err := th.builder.AddTargetByName("out")
	require.NoError(t, err)
	require.False(t, th.builder.AlreadyUpToDate())

	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.True(t, th.builder.AlreadyUpToDate())

	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()
	th.builder.Cleanup()
	th.builder.TestOnlyPlan().Reset()

	_, err = th.builder.AddTargetByName("out")
	require.NoError(t, err)
	require.True(t, th.builder.AlreadyUpToDate())
}

func TestRspFileCmdLineChange(t *testing.T) {
	th := newBuildTestHelperWithBuildLog(t)

	test.AssertParse(t, `
rule cat_rsp
  command = cat $rspfile > $out
  rspfile = $rspfile
  rspfile_content = $long_command
build out: cat_rsp in
  rspfile = out.rsp
  long_command = Original very long command
`, th.state)

	th.fs.Create("out", []byte{})
	th.fs.Tick()
	th.fs.Create("in", []byte{})

	_, err := th.builder.AddTargetByName("out")
	require.NoError(t, err)

	// 1. Build for the 1st time (-> populate log)
	buildRes, err := th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.Len(t, th.commandRunner.commandsRan, 1)

	// 2. Build again (no change)
	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()
	_, err = th.builder.AddTargetByName("out")
	require.NoError(t, err)
	require.True(t, th.builder.AlreadyUpToDate())

	// 3. Alter the entry in the logfile
	// (to simulate a change in the command line between 2 builds)
	logEntry := th.buildLog.LookupByOutput("out")
	require.NotNil(t, logEntry)
	assertHash(t,
		"cat out.rsp > out;rspfile=Original very long command",
		logEntry.CommandHash)
	logEntry.CommandHash++ // Change the command hash to something else.
	// Now expect the target to be rebuilt
	th.commandRunner.commandsRan = th.commandRunner.commandsRan[:0]
	th.state.Reset()
	_, err = th.builder.AddTargetByName("out")
	require.NoError(t, err)
	buildRes, err = th.builder.Build()
	require.NoError(t, err)
	require.Equal(t, exit_status.ExitSuccess, buildRes)
	require.Len(t, th.commandRunner.commandsRan, 1)
}
