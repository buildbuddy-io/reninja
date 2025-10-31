package state

import (
	"fmt"

	"github.com/buildbuddy-io/gin/internal/edit_distance"
	"github.com/buildbuddy-io/gin/internal/eval_env"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/util"
)

var (
	defaultPool = graph.NewPool("", 0)
	consolePool = graph.NewPool("console", 1)
)

// State represents the global build state
type State struct {
	//  Mapping of path -> Node.
	paths map[string]*graph.Node

	// All the pools used in the graph.
	pools map[string]*graph.Pool

	// All the edges of the graph.
	edges []*graph.Edge

	// Global variable bindings
	bindings *eval_env.BindingEnv

	// Default build targets
	defaults []*graph.Node
}

// New creates a new State
func New() *State {
	s := &State{
		paths:    make(map[string]*graph.Node),
		pools:    make(map[string]*graph.Pool),
		edges:    make([]*graph.Edge, 0),
		bindings: eval_env.NewBindingEnv(nil),
		defaults: make([]*graph.Node, 0),
	}

	phonyRule := eval_env.NewRule("phony")
	phonyRule.SetPhony(true)
	s.bindings.AddRule(phonyRule)

	// Add built-in pools
	s.AddPool(defaultPool)
	s.AddPool(consolePool)

	return s
}

func (s *State) Reset() {
	for _, node := range s.paths {
		node.ResetState()
	}
	for _, e := range s.edges {
		e.SetOutputsReady(false)
		e.SetDepsLoaded(false)
		e.SetMark(graph.VisitNone)
	}
}

func (s *State) Dump() {
	for _, node := range s.paths {
		status := "unknown"
		if node.StatusKnown() {
			if node.Dirty() {
				status = "dirty"
			} else {
				status = "clean"
			}
		}
		fmt.Printf("%s %s [id:%d]\n", node.Path(), status, node.ID())
	}
	if len(s.pools) > 0 {
		fmt.Printf("resource_pools:\n")
		for _, pool := range s.pools {
			if pool.Name() != "" {
				pool.Dump()
			}
		}
	}

}

// GetNode returns a node by path, creating it if necessary
func (s *State) GetNode(canonicalPath string) *graph.Node {
	node := s.LookupNode(canonicalPath)
	if node != nil {
		return node
	}

	// TODO(tylerw): remove SlashBits everwhere?
	node = graph.NewNode(canonicalPath, 0)
	s.paths[canonicalPath] = node
	return node
}

// LookupNode returns a node by path without creating it
func (s *State) LookupNode(canonicalPath string) *graph.Node {
	return s.paths[canonicalPath]
}

func (s *State) SpellcheckNode(path string) *graph.Node {
	allowReplacements := true
	maxValidEditDistance := 3

	minDistance := maxValidEditDistance + 1
	var result *graph.Node
	for p, n := range s.paths {
		distance := edit_distance.EditDistance(p, path, allowReplacements, maxValidEditDistance)
		if distance < minDistance && n != nil {
			minDistance = distance
			result = n
		}
	}
	return result
}

func (s *State) Paths() map[string]*graph.Node {
	// Return a copy to prevent concurrent modification
	paths := make(map[string]*graph.Node, len(s.paths))
	for k, v := range s.paths {
		paths[k] = v
	}
	return paths
}

func (s *State) RemoveLastEdge() {
	if len(s.edges) > 0 {
		s.edges = s.edges[:len(s.edges)-1]
	}
}

// AddEdge adds an edge to the build graph
func (s *State) AddEdge(rule *eval_env.Rule) *graph.Edge {
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
	// Return a copy to prevent concurrent modification
	edges := make([]*graph.Edge, len(s.edges))
	copy(edges, s.edges)
	return edges
}

// AddPool adds a resource pool
func (s *State) AddPool(pool *graph.Pool) error {
	if _, exists := s.pools[pool.Name()]; exists {
		return fmt.Errorf("duplicate pool '%s'", pool.Name())
	}

	s.pools[pool.Name()] = pool
	return nil
}

// LookupPool returns a pool by name
func (s *State) LookupPool(name string) *graph.Pool {
	return s.pools[name]
}

// Pools returns all pools
func (s *State) Pools() map[string]*graph.Pool {
	// Return a copy to prevent concurrent modification
	pools := make(map[string]*graph.Pool, len(s.pools))
	for k, v := range s.pools {
		pools[k] = v
	}
	return pools
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
	if other := node.InEdge(); other != nil {
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
	canonPath, _ := util.CanonicalizePath(path)
	node := s.paths[canonPath]
	if node == nil {
		return fmt.Errorf("unknown target '%s'", path)
	}
	s.defaults = append(s.defaults, node)
	return nil
}

// Defaults returns the default build targets
func (s *State) Defaults() []*graph.Node {
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
