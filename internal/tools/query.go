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

// QueryTool provides information about build targets
type QueryTool struct {
	state *state.State
}

// NewQueryTool creates a new QueryTool
func NewQueryTool(s *state.State) *QueryTool {
	return &QueryTool{state: s}
}

// Run queries information about targets
func (q *QueryTool) Run(targets []string) error {
	if len(targets) == 0 {
		fmt.Fprintf(os.Stderr, "ninja: error: query requires a target\n")
		return fmt.Errorf("no target specified")
	}
	
	for _, target := range targets {
		if err := q.queryTarget(target); err != nil {
			fmt.Fprintf(os.Stderr, "ninja: error: %v\n", err)
			return err
		}
		if len(targets) > 1 {
			fmt.Println() // Separate multiple targets
		}
	}
	
	return nil
}

// queryTarget queries information about a single target
func (q *QueryTool) queryTarget(target string) error {
	node := q.state.LookupNode(target)
	if node == nil {
		return fmt.Errorf("unknown target '%s'", target)
	}
	
	fmt.Printf("%s:\n", node.Path())
	
	edge := node.InEdge()
	if edge == nil {
		fmt.Println("  source file")
		return nil
	}
	
	// Show rule
	if edge.Rule() != nil {
		fmt.Printf("  rule: %s\n", edge.Rule().Name())
	} else if edge.IsPhony() {
		fmt.Println("  phony edge")
	}
	
	// Show inputs
	if len(edge.Inputs()) > 0 {
		fmt.Println("  inputs:")
		for _, input := range edge.Inputs() {
			status := ""
			if input.InEdge() != nil {
				status = " (generated)"
			}
			fmt.Printf("    %s%s\n", input.Path(), status)
		}
	}
	
	// Show implicit dependencies
	if len(edge.ImplicitInputs()) > 0 {
		fmt.Println("  implicit inputs:")
		for _, dep := range edge.ImplicitInputs() {
			fmt.Printf("    %s\n", dep.Path())
		}
	}
	
	// Show order-only dependencies
	if len(edge.OrderOnlyInputs()) > 0 {
		fmt.Println("  order-only inputs:")
		for _, dep := range edge.OrderOnlyInputs() {
			fmt.Printf("    %s\n", dep.Path())
		}
	}
	
	// Show outputs (for edges with multiple outputs)
	outputs := edge.Outputs()
	if len(outputs) > 1 {
		fmt.Println("  outputs:")
		for _, output := range outputs {
			if output != node {
				fmt.Printf("    %s\n", output.Path())
			}
		}
	}
	
	// Show command
	if !edge.IsPhony() {
		command := edge.EvaluateCommand(false)
		if command != "" {
			fmt.Printf("  command: %s\n", command)
		}
		
		// Show description if available
		desc := edge.GetBinding("description")
		if desc != "" {
			fmt.Printf("  description: %s\n", desc)
		}
	}
	
	// Show what depends on this target
	dependents := q.findDependents(node)
	if len(dependents) > 0 {
		fmt.Println("  dependents:")
		for _, dep := range dependents {
			outputs := dep.Outputs()
			if len(outputs) > 0 {
				fmt.Printf("    %s\n", outputs[0].Path())
			}
		}
	}
	
	return nil
}

// findDependents finds all edges that depend on a node
func (q *QueryTool) findDependents(target *graph.Node) []*graph.Edge {
	var dependents []*graph.Edge
	
	for _, edge := range q.state.Edges() {
		// Check explicit inputs
		for _, input := range edge.Inputs() {
			if input == target {
				dependents = append(dependents, edge)
				break
			}
		}
		
		// Check implicit dependencies
		for _, dep := range edge.ImplicitInputs() {
			if dep == target {
				dependents = append(dependents, edge)
				break
			}
		}
		
		// Check order-only dependencies
		for _, dep := range edge.OrderOnlyInputs() {
			if dep == target {
				dependents = append(dependents, edge)
				break
			}
		}
	}
	
	return dependents
}

// TargetsTool lists all targets
type TargetsTool struct {
	state *state.State
}

// NewTargetsTool creates a new TargetsTool
func NewTargetsTool(s *state.State) *TargetsTool {
	return &TargetsTool{state: s}
}

// Run lists all targets
func (t *TargetsTool) Run(args []string) error {
	// Parse options
	showAll := false
	showRules := false
	showDepth := 1
	
	for _, arg := range args {
		switch arg {
		case "all":
			showAll = true
		case "rules":
			showRules = true
		case "depth":
			showDepth = 1000 // Show full depth
		}
	}
	
	if showRules {
		// List all rules
		rules := t.state.Rules()
		for name := range rules {
			if name != "phony" {
				fmt.Println(name)
			}
		}
		return nil
	}
	
	// List targets
	targets := make(map[string]bool)
	
	if showAll {
		// Show all nodes with edges
		for _, edge := range t.state.Edges() {
			for _, output := range edge.Outputs() {
				targets[output.Path()] = true
			}
		}
	} else {
		// Show default targets and their dependencies
		defaults := t.state.Defaults()
		if len(defaults) == 0 {
			// No defaults, show all top-level targets
			for _, edge := range t.state.Edges() {
				for _, output := range edge.Outputs() {
					// Check if this is a top-level target (not used by others)
					if !t.isIntermediateTarget(output) {
						targets[output.Path()] = true
					}
				}
			}
		} else {
			// Show defaults
			for _, node := range defaults {
				t.collectTargets(node, targets, showDepth)
			}
		}
	}
	
	// Sort and print targets
	for target := range targets {
		fmt.Println(strings.TrimPrefix(target, "./"))
	}
	
	return nil
}

// isIntermediateTarget checks if a node is used as input by other edges
func (t *TargetsTool) isIntermediateTarget(node *graph.Node) bool {
	for _, edge := range t.state.Edges() {
		for _, input := range edge.Inputs() {
			if input == node {
				return true
			}
		}
		for _, dep := range edge.ImplicitInputs() {
			if dep == node {
				return true
			}
		}
	}
	return false
}

// collectTargets recursively collects targets
func (t *TargetsTool) collectTargets(node *graph.Node, targets map[string]bool, depth int) {
	if depth <= 0 {
		return
	}
	
	targets[node.Path()] = true
	
	edge := node.InEdge()
	if edge != nil {
		for _, input := range edge.Inputs() {
			t.collectTargets(input, targets, depth-1)
		}
	}
}

// CommandsTool lists all commands
type CommandsTool struct {
	state *state.State
}

// NewCommandsTool creates a new CommandsTool
func NewCommandsTool(s *state.State) *CommandsTool {
	return &CommandsTool{state: s}
}

// Run lists all commands
func (c *CommandsTool) Run(targets []string) error {
	var edges []*graph.Edge
	
	if len(targets) > 0 {
		// Show commands for specific targets
		for _, target := range targets {
			node := c.state.LookupNode(target)
			if node == nil {
				fmt.Fprintf(os.Stderr, "warning: unknown target '%s'\n", target)
				continue
			}
			
			if edge := node.InEdge(); edge != nil && !edge.IsPhony() {
				edges = append(edges, edge)
			}
		}
	} else {
		// Show all commands
		for _, edge := range c.state.Edges() {
			if !edge.IsPhony() {
				edges = append(edges, edge)
			}
		}
	}
	
	// Print commands
	for _, edge := range edges {
		command := edge.EvaluateCommand(false)
		if command != "" {
			fmt.Println(command)
		}
	}
	
	return nil
}