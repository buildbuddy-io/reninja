package state

import (
	"fmt"
	"sync"

	"github.com/buildbuddy-io/gin/internal/eval_env"
	"github.com/buildbuddy-io/gin/internal/graph"
)

var (
	defaultPool = graph.NewPool("", 0)
	consolePool = graph.NewPool("console", 1)
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
func (s *State) GetNode(canonicalPath string) *graph.Node {
	s.mu.Lock()
	defer s.mu.Unlock()

	if node, ok := s.paths[canonicalPath]; ok {
		return node
	}

	// TODO(tylerw): remove SlashBits everwhere?
	node := graph.NewNode(canonicalPath, 0)
	s.paths[canonicalPath] = node
	return node
}

// LookupNode returns a node by path without creating it
func (s *State) LookupNode(canonicalPath string) *graph.Node {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.paths[canonicalPath]
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

func (s *State) RemoveLastEdge() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.edges) > 0 {
		s.edges = s.edges[:len(s.edges)-1]
	}
}

// AddEdge adds an edge to the build graph
func (s *State) AddEdge(rule *eval_env.Rule) *graph.Edge {
	s.mu.Lock()
	defer s.mu.Unlock()

	edge := graph.NewEdge()
	edge.SetRule(rule)
	edge.SetPool(defaultPool)
	edge.SetEnv(s.bindings)
	edge.SetID(len(s.edges))
	s.edges = append(s.edges, edge)

	return edge
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

func (s *State) AddIn(canonicalPath string, edge *graph.Edge) {
	node := s.GetNode(canonicalPath)
	node.SetGeneratedByDepLoader(false)
	edge.AddInput(node)
	node.AddOutEdge(edge)
}

func (s *State) AddOut(canonicalPath string, edge *graph.Edge) error {
	node := s.GetNode(canonicalPath)
	other := node.InEdge()
	if other != nil {
		if other == edge {
			return fmt.Errorf("%s is defined as an output multiple times", canonicalPath)
		} else {
			return fmt.Errorf("multiple rules generate %s", canonicalPath)
		}
	}
	edge.AddOutput(node)
	node.SetInEdge(edge)
	node.SetGeneratedByDepLoader(false)
	return nil
}

func (s *State) AddValidation(canonicalPath string, edge *graph.Edge) {
	node := s.GetNode(canonicalPath)
	edge.AddValidation(node)
	node.AddValidationOutEdge(edge)
	node.SetGeneratedByDepLoader(false)
}

// AddDefault adds a default build target
func (s *State) AddDefault(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	canonPath, _ := graph.CanonicalizePath(path)
	node := s.paths[canonPath]
	if node == nil {
		return fmt.Errorf("unknown target '%s'", path)
	}
	s.defaults = append(s.defaults, node)
	return nil
}

// Defaults returns the default build targets
func (s *State) Defaults() []*graph.Node {
	s.mu.RLock()
	defer s.mu.RUnlock()

	defaults := make([]*graph.Node, len(s.defaults))
	copy(defaults, s.defaults)
	return defaults
}

func (s *State) DefaultNodes() ([]*graph.Node, error) {
	if len(s.defaults) == 0 {
		return s.RootNodes()
	}
	return s.defaults, nil
}

// RootNodes returns all root nodes (nodes with no output edges)
func (s *State) RootNodes() ([]*graph.Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rootNodes := make([]*graph.Node, 0)
	for _, edge := range s.edges {
		for _, out := range edge.Outputs() {
			if len(out.OutEdges()) == 0 {
				rootNodes = append(rootNodes, out)
			}
		}
	}
	if len(s.edges) != 0 && len(rootNodes) == 0 {
		return nil, fmt.Errorf("could not determine root nodes of build graph")
	}
	return rootNodes, nil
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
