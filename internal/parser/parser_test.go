package parser_test

import (
	"testing"

	"github.com/buildbuddy-io/gin/internal/disk"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/parser"
	"github.com/buildbuddy-io/gin/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func VerifyGraph(t *testing.T, state *state.State) {
	t.Helper()
	for _, edge := range state.Edges() {
		// All edges need at least one output.
		require.NotEmpty(t, edge.Outputs, "Edge should have at least one output")

		// Check that the edge's inputs have the edge as out-edge.
		for _, inNode := range edge.Inputs() {
			found := false
			for _, outEdge := range inNode.OutEdges() {
				if outEdge == edge {
					found = true
					break
				}
			}
			require.True(t, found, "Input node should have this edge in its out-edges")
		}

		// Check that the edge's outputs have the edge as in-edge.
		for _, outNode := range edge.Outputs() {
			require.Equal(t, edge, outNode.InEdge(), "Output node %s should have this edge %d in its in-edges", outNode.Path(), edge.ID())
		}
	}

	// The union of all in- and out-edges of each nodes should be exactly edges_.
	nodeEdgeSet := make(map[*graph.Edge]bool)
	for _, node := range state.Paths() {
		if node.InEdge() != nil {
			nodeEdgeSet[node.InEdge()] = true
		}
		for _, outEdge := range node.OutEdges() {
			nodeEdgeSet[outEdge] = true
		}
	}

	edgeSet := make(map[*graph.Edge]bool)
	for _, edge := range state.Edges() {
		edgeSet[edge] = true
	}

	require.Equal(t, edgeSet, nodeEdgeSet, "Union of all node edges should equal state edges")
}

func AssertParse(t *testing.T, input string, s *state.State) {
	t.Helper()
	manifestParser := parser.New(s, disk.NewMockDiskInterface(), parser.DefaultOptions())
	assert.NoError(t, manifestParser.Parse("", input))
	VerifyGraph(t, s)
}

func TestEmpty(t *testing.T) {
	s := state.New()
	AssertParse(t, "", s)
}

func TestRuleAttributes(t *testing.T) {
	// Check that all of the allowed rule attributes are parsed ok.
	s := state.New()
	AssertParse(t, `rule cat
  command = a
  depfile = a
  deps = a
  description = a
  generator = a
  restat = a
  rspfile = a
  rspfile_content = a
`, s)
}

func TestIgnoreIndentedComments(t *testing.T) {
	s := state.New()
	AssertParse(t, `  #indented comment
rule cat
  command = cat $in > $out
  #generator = 1
  restat = 1 # comment
  #comment
build result: cat in_1.cc in-2.O
  #comment
`, s)

	rule, ok := s.Bindings().LookupRule("cat")
	require.True(t, ok)
	require.NotNil(t, rule)
	assert.Equal(t, "cat", rule.Name())

	edge := s.GetNode("result").InEdge()
	require.NotNil(t, edge)
	assert.True(t, edge.GetBindingBool("restat"))
	assert.False(t, edge.GetBindingBool("generator"))
}

func TestIgnoreIndentedBlankLines(t *testing.T) {
	s := state.New()
	AssertParse(t, `  #indented comment
  
rule cat
  command = cat $in > $out
  
build result: cat in_1.cc in-2.O
  
variable=1
`, s)

	assert.Equal(t, "1", s.Bindings().LookupVariable("variable"))
}

func TestResponseFiles(t *testing.T) {
	s := state.New()
	AssertParse(t,
		`rule cat_rsp
  command = cat $rspfile > $out
  rspfile = $rspfile
  rspfile_content = $in

build out: cat_rsp in
  rspfile=out.rsp
`, s)
	assert.Equal(t, 2, len(s.Bindings().GetRules()))
	rule, ok := s.Bindings().LookupRule("cat_rsp")
	require.True(t, ok)
	assert.Equal(t, "cat_rsp", rule.Name())
	eval, ok := rule.GetBinding("command")
	require.True(t, ok)
	assert.Equal(t, "[cat ][$rspfile][ > ][$out]", eval.Serialize())
}

func TestInNewline(t *testing.T) {
	s := state.New()
	AssertParse(t,
		`rule cat_rsp
  command = cat $in_newline > $out

build out: cat_rsp in in2
  rspfile=out.rsp
`, s)
	assert.Equal(t, 2, len(s.Bindings().GetRules()))
	rule, ok := s.Bindings().LookupRule("cat_rsp")
	require.True(t, ok)
	assert.Equal(t, "cat_rsp", rule.Name())
	eval, ok := rule.GetBinding("command")
	require.True(t, ok)
	assert.Equal(t, "[cat ][$in_newline][ > ][$out]", eval.Serialize())

	edge := s.Edges()[0]
	assert.Equal(t, "cat in\nin2 > out", edge.EvaluateCommand(false))
}

func TestVariables(t *testing.T) {
	s := state.New()
	AssertParse(t,
		`l = one-letter-test
rule link
  command = ld $l $extra $with_under -o $out $in

extra = -pthread
with_under = -under
build a: link b c
nested1 = 1
nested2 = $nested1/2
build supernested: link x
  extra = $nested2/3
`, s)
	assert.Equal(t, 2, len(s.Edges()))
	edge := s.Edges()[0]
	assert.Equal(t, "ld one-letter-test -pthread -under -o a b c", edge.EvaluateCommand(false))
	assert.Equal(t, "1/2", s.Bindings().LookupVariable("nested2"))

	edge = s.Edges()[1]
	assert.Equal(t, "ld one-letter-test 1/2/3 -under -o supernested x", edge.EvaluateCommand(false))
}

func TestVariableScope(t *testing.T) {
	s := state.New()
	AssertParse(t,
		`foo = bar
rule cmd
  command = cmd $foo $in $out

build inner: cmd a
  foo = baz
build outer: cmd b

`, s)
	assert.Equal(t, 2, len(s.Edges()))
	edge := s.Edges()[0]
	assert.Equal(t, "cmd baz a inner", edge.EvaluateCommand(false))

	edge = s.Edges()[1]
	assert.Equal(t, "cmd bar b outer", edge.EvaluateCommand(false))
}

func TestContinuation(t *testing.T) {
	s := state.New()
	AssertParse(t,
		`rule link
  command = foo bar $
    baz

build a: link c $
 d e f
`, s)
	assert.Equal(t, 2, len(s.Bindings().GetRules()))
	rule, ok := s.Bindings().LookupRule("link")
	require.True(t, ok)
	assert.Equal(t, "link", rule.Name())
	eval, ok := rule.GetBinding("command")
	require.True(t, ok)
	assert.Equal(t, "[foo bar baz]", eval.Serialize())
}

func TestBackslash(t *testing.T) {
	s := state.New()
	AssertParse(t,
		`foo = bar\\baz
foo2 = bar\\ baz
`, s)
	assert.Equal(t, `bar\\baz`, s.Bindings().LookupVariable("foo"))
	assert.Equal(t, `bar\\ baz`, s.Bindings().LookupVariable("foo2"))
}

func TestComment(t *testing.T) {
	s := state.New()
	AssertParse(t,
		`# this is a comment
foo = not # a comment
`, s)
	assert.Equal(t, `not # a comment`, s.Bindings().LookupVariable("foo"))
}

func TestDollars(t *testing.T) {
	s := state.New()
	AssertParse(t,
		`rule foo
  command = ${out}bar$$baz$$$
blah
x = $$dollar
build $x: foo y
`, s)
	assert.Equal(t, `$dollar`, s.Bindings().LookupVariable("x"))
	edge := s.Edges()[0]
	assert.Equal(t, `$dollarbar$baz$blah`, edge.EvaluateCommand(false))
}
