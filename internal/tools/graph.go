// Copyright 2024 The Ninja-Go Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tools

import (
	"fmt"
	"os"
	"strings"

	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/state"
)

// GraphTool outputs the build graph in Graphviz format
type GraphTool struct {
	state *state.State
}

// NewGraphTool creates a new GraphTool
func NewGraphTool(s *state.State) *GraphTool {
	return &GraphTool{state: s}
}

// Run generates the Graphviz output
func (g *GraphTool) Run(targets []string) error {
	fmt.Println("digraph ninja {")
	fmt.Println("  rankdir=\"LR\"")
	fmt.Println("  node [fontsize=10, shape=box, height=0.25]")
	fmt.Println("  edge [fontsize=10]")

	visited := make(map[*graph.Node]bool)

	if len(targets) > 0 {
		// Generate graph for specific targets
		for _, target := range targets {
			node := g.state.LookupNode(target)
			if node == nil {
				fmt.Fprintf(os.Stderr, "warning: unknown target '%s'\n", target)
				continue
			}
			g.visitNode(node, visited)
		}
	} else {
		// Generate graph for all default targets
		defaults := g.state.Defaults()
		if len(defaults) == 0 {
			// No defaults, show all edges
			for _, edge := range g.state.Edges() {
				g.visitEdge(edge, visited)
			}
		} else {
			for _, node := range defaults {
				g.visitNode(node, visited)
			}
		}
	}

	fmt.Println("}")
	return nil
}

// visitNode recursively visits a node and its dependencies
func (g *GraphTool) visitNode(node *graph.Node, visited map[*graph.Node]bool) {
	if visited[node] {
		return
	}
	visited[node] = true

	edge := node.InEdge()
	if edge != nil {
		g.visitEdge(edge, visited)
	}
}

// visitEdge outputs an edge and visits its inputs
func (g *GraphTool) visitEdge(edge *graph.Edge, visited map[*graph.Node]bool) {
	// Create edge ID
	edgeID := fmt.Sprintf("edge_%p", edge)

	// Output edge node
	ruleName := "phony"
	if edge.Rule() != nil {
		ruleName = edge.Rule().Name()
	}

	label := ruleName
	if edge.IsPhony() {
		fmt.Printf("  \"%s\" [label=\"%s\", shape=ellipse]\n", edgeID, escapeLabel(label))
	} else {
		fmt.Printf("  \"%s\" [label=\"%s\"]\n", edgeID, escapeLabel(label))
	}

	// Connect inputs to edge
	for _, input := range edge.Inputs() {
		inputID := getNodeID(input)
		fmt.Printf("  \"%s\" -> \"%s\"\n", inputID, edgeID)

		// Visit input recursively
		g.visitNode(input, visited)
	}

	// Connect edge to outputs
	for _, output := range edge.Outputs() {
		outputID := getNodeID(output)
		fmt.Printf("  \"%s\" -> \"%s\"\n", edgeID, outputID)

		// Mark output as visited
		visited[output] = true
	}

	// Handle implicit dependencies
	for _, implicit := range edge.ImplicitInputs() {
		implicitID := getNodeID(implicit)
		fmt.Printf("  \"%s\" -> \"%s\" [style=dotted]\n", implicitID, edgeID)

		// Visit implicit dependency
		g.visitNode(implicit, visited)
	}

	// Handle order-only dependencies
	for _, orderOnly := range edge.OrderOnlyInputs() {
		orderOnlyID := getNodeID(orderOnly)
		fmt.Printf("  \"%s\" -> \"%s\" [style=dashed]\n", orderOnlyID, edgeID)

		// Visit order-only dependency
		g.visitNode(orderOnly, visited)
	}
}

// getNodeID returns a unique ID for a node
func getNodeID(node *graph.Node) string {
	return fmt.Sprintf("node_%s", escapeID(node.Path()))
}

// escapeID escapes a string for use as a Graphviz ID
func escapeID(s string) string {
	// Replace problematic characters
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, ".", "_")
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, ":", "_")
	return s
}

// escapeLabel escapes a string for use as a Graphviz label
func escapeLabel(s string) string {
	// Escape quotes and backslashes
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return s
}
