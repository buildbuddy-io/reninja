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

package state

import (
	"fmt"
	"sync"

	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/eval_env"
)

// State represents the global build state
type State struct {
	mu sync.RWMutex

	// All nodes indexed by path
	paths map[string]*graph.Node

	// All edges in the build graph
	edges []*graph.Edge

	// All pools for resource management
	pools map[string]*graph.Pool

	// All rules
	rules map[string]*eval_env.Rule

	// Global variable bindings
	bindings *eval_env.BindingEnv

	// Default build targets
	defaults []*graph.Node

	// Root nodes (nodes with no output edges)
	roots []*graph.Node
}

// New creates a new State
func New() *State {
	s := &State{
		paths:    make(map[string]*graph.Node),
		edges:    make([]*graph.Edge, 0),
		pools:    make(map[string]*graph.Pool),
		rules:    make(map[string]*eval_env.Rule),
		bindings: eval_env.NewBindingEnv(nil),
		defaults: make([]*graph.Node, 0),
		roots:    make([]*graph.Node, 0),
	}

	// Add built-in pools
	s.AddPool(graph.NewPool("console", 1))

	// Add phony rule
	phonyRule := eval_env.NewRule("phony")
	phonyRule.SetPhony(true)
	s.AddRule(phonyRule)

	return s
}

// GetNode returns a node by path, creating it if necessary
func (s *State) GetNode(path string) *graph.Node {
	s.mu.Lock()
	defer s.mu.Unlock()

	canonPath, slashBits := graph.CanonicalizePath(path)

	if node, ok := s.paths[canonPath]; ok {
		return node
	}

	node := graph.NewNode(canonPath, slashBits)
	s.paths[canonPath] = node
	return node
}

// LookupNode returns a node by path without creating it
func (s *State) LookupNode(path string) *graph.Node {
	s.mu.RLock()
	defer s.mu.RUnlock()

	canonPath, _ := graph.CanonicalizePath(path)
	return s.paths[canonPath]
}

// Paths returns all paths
func (s *State) Paths() map[string]*graph.Node {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Return a copy to prevent concurrent modification
	paths := make(map[string]*graph.Node, len(s.paths))
	for k, v := range s.paths {
		paths[k] = v
	}
	return paths
}

// AddEdge adds an edge to the build graph
func (s *State) AddEdge(edge *graph.Edge) {
	s.mu.Lock()
	defer s.mu.Unlock()

	edge.SetID(len(s.edges))
	s.edges = append(s.edges, edge)

	// Set up node relationships
	for _, out := range edge.Outputs() {
		if out.InEdge() != nil {
			// Output is already produced by another edge
			// This would be an error in a real build
			continue
		}
		out.SetInEdge(edge)
	}

	for _, in := range edge.Inputs() {
		in.AddOutEdge(edge)
	}

	for _, validation := range edge.Validations() {
		validation.AddValidationOutEdge(edge)
	}
}

// Edges returns all edges
func (s *State) Edges() []*graph.Edge {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Return a copy to prevent concurrent modification
	edges := make([]*graph.Edge, len(s.edges))
	copy(edges, s.edges)
	return edges
}

// AddPool adds a resource pool
func (s *State) AddPool(pool *graph.Pool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.pools[pool.Name()]; exists {
		return fmt.Errorf("duplicate pool '%s'", pool.Name())
	}

	s.pools[pool.Name()] = pool
	return nil
}

// LookupPool returns a pool by name
func (s *State) LookupPool(name string) *graph.Pool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.pools[name]
}

// Pools returns all pools
func (s *State) Pools() map[string]*graph.Pool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Return a copy to prevent concurrent modification
	pools := make(map[string]*graph.Pool, len(s.pools))
	for k, v := range s.pools {
		pools[k] = v
	}
	return pools
}

// AddRule adds a build rule
func (s *State) AddRule(rule *eval_env.Rule) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.rules[rule.Name()]; exists {
		return fmt.Errorf("duplicate rule '%s'", rule.Name())
	}

	s.rules[rule.Name()] = rule
	s.bindings.AddRule(rule)
	return nil
}

// LookupRule returns a rule by name
func (s *State) LookupRule(name string) *eval_env.Rule {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.rules[name]
}

// Rules returns all rules
func (s *State) Rules() map[string]*eval_env.Rule {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Return a copy to prevent concurrent modification
	rules := make(map[string]*eval_env.Rule, len(s.rules))
	for k, v := range s.rules {
		rules[k] = v
	}
	return rules
}

// Bindings returns the global binding environment
func (s *State) Bindings() *eval_env.BindingEnv {
	return s.bindings
}

// AddDefault adds a default build target
func (s *State) AddDefault(node *graph.Node) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.defaults = append(s.defaults, node)
}

// Defaults returns the default build targets
func (s *State) Defaults() []*graph.Node {
	s.mu.RLock()
	defer s.mu.RUnlock()

	defaults := make([]*graph.Node, len(s.defaults))
	copy(defaults, s.defaults)
	return defaults
}

// RootNodes returns all root nodes (nodes with no output edges)
func (s *State) RootNodes() []*graph.Node {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.roots) > 0 {
		roots := make([]*graph.Node, len(s.roots))
		copy(roots, s.roots)
		return roots
	}

	// Calculate root nodes if not cached
	var roots []*graph.Node
	for _, node := range s.paths {
		if len(node.OutEdges()) == 0 && node.InEdge() == nil {
			roots = append(roots, node)
		}
	}

	return roots
}

// Reset clears the state
func (s *State) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.paths = make(map[string]*graph.Node)
	s.edges = make([]*graph.Edge, 0)
	s.pools = make(map[string]*graph.Pool)
	s.rules = make(map[string]*eval_env.Rule)
	s.bindings = eval_env.NewBindingEnv(nil)
	s.defaults = make([]*graph.Node, 0)
	s.roots = make([]*graph.Node, 0)

	// Re-add built-in pools and rules
	s.pools["console"] = graph.NewPool("console", 1)

	phonyRule := eval_env.NewRule("phony")
	phonyRule.SetPhony(true)
	s.rules["phony"] = phonyRule
	s.bindings.AddRule(phonyRule)
}

// AssignNodeIDs assigns sequential IDs to all nodes
func (s *State) AssignNodeIDs() {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := 0
	for _, node := range s.paths {
		node.SetID(id)
		id++
	}
}

// Dump prints the state for debugging
func (s *State) Dump() {
	s.mu.RLock()
	defer s.mu.RUnlock()

	fmt.Printf("State dump:\n")
	fmt.Printf("  %d nodes\n", len(s.paths))
	fmt.Printf("  %d edges\n", len(s.edges))
	fmt.Printf("  %d pools\n", len(s.pools))
	fmt.Printf("  %d rules\n", len(s.rules))
	fmt.Printf("  %d defaults\n", len(s.defaults))

	if len(s.defaults) > 0 {
		fmt.Printf("\nDefault targets:\n")
		for _, node := range s.defaults {
			fmt.Printf("  %s\n", node.Path())
		}
	}
}
