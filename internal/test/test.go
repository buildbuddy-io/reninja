package test

import (
	"testing"

	"github.com/buildbuddy-io/gin/internal/disk"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/manifest_parser"
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
	manifestParser := manifest_parser.New(s, disk.NewMockDiskInterface(), manifest_parser.DefaultOptions())
	assert.NoError(t, manifestParser.Parse("", input))
	VerifyGraph(t, s)
}

func AddCatRule(t *testing.T, s *state.State) {
	t.Helper()
	manifestParser := manifest_parser.New(s, disk.NewMockDiskInterface(), manifest_parser.DefaultOptions())
	assert.NoError(t, manifestParser.Parse("",
		`rule cat
  command = cat $in > $out
`))
	VerifyGraph(t, s)
}
