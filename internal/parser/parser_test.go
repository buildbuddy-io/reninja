package parser_test

import (
	"testing"

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
			require.Equal(t, edge, outNode.InEdge, "Output node should have this edge as its in-edge")
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
	manifestParser := parser.New(s)
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
  #comment`, s)

	rule, ok := s.Bindings().LookupRule("cat")
	require.True(t, ok)
	require.NotNil(t, rule)
	assert.Equal(t, "cat", rule.Name())

	edge := s.GetNode("result").InEdge()
	require.NotNil(t, edge)
	assert.True(t, edge.GetBindingBool("restat"))
	assert.False(t, edge.GetBindingBool("generator"))
}
