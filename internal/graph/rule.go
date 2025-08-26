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

package graph

// Rule represents a build rule template
type Rule struct {
	name     string
	bindings map[string]EvalString
	phony    bool
}

// NewRule creates a new Rule
func NewRule(name string) *Rule {
	return &Rule{
		name:     name,
		bindings: make(map[string]EvalString),
		phony:    false,
	}
}

// Name returns the rule name
func (r *Rule) Name() string {
	return r.name
}

// IsPhony returns whether this is a phony rule
func (r *Rule) IsPhony() bool {
	return r.phony
}

// SetPhony sets whether this is a phony rule
func (r *Rule) SetPhony(phony bool) {
	r.phony = phony
}

// AddBinding adds a variable binding to the rule
func (r *Rule) AddBinding(key string, value EvalString) {
	r.bindings[key] = value
}

// GetBinding returns a binding value
func (r *Rule) GetBinding(key string) (EvalString, bool) {
	val, ok := r.bindings[key]
	return val, ok
}

// Bindings returns all bindings
func (r *Rule) Bindings() map[string]EvalString {
	return r.bindings
}

// Pool represents a resource pool for limiting parallel execution
type Pool struct {
	name       string
	depth      int // Maximum concurrent jobs (0 = infinite)
	currentUse int // Currently running jobs
	delayed    []*Edge // Queue of delayed edges
}

// NewPool creates a new Pool
func NewPool(name string, depth int) *Pool {
	return &Pool{
		name:       name,
		depth:      depth,
		currentUse: 0,
		delayed:    make([]*Edge, 0),
	}
}

// Name returns the pool name
func (p *Pool) Name() string {
	return p.name
}

// Depth returns the maximum concurrent jobs
func (p *Pool) Depth() int {
	return p.depth
}

// SetDepth sets the maximum concurrent jobs
func (p *Pool) SetDepth(depth int) {
	p.depth = depth
}

// CurrentUse returns the number of currently running jobs
func (p *Pool) CurrentUse() int {
	return p.currentUse
}

// InUse returns whether the pool is currently in use
func (p *Pool) InUse() bool {
	return p.currentUse > 0
}

// Available returns whether the pool can accept another job
func (p *Pool) Available() bool {
	return p.depth == 0 || p.currentUse < p.depth
}

// Acquire tries to acquire a slot in the pool
func (p *Pool) Acquire() bool {
	if !p.Available() {
		return false
	}
	p.currentUse++
	return true
}

// Release releases a slot in the pool
func (p *Pool) Release() {
	if p.currentUse > 0 {
		p.currentUse--
	}
}

// DelayEdge adds an edge to the delayed queue
func (p *Pool) DelayEdge(edge *Edge) {
	p.delayed = append(p.delayed, edge)
}

// PopDelayedEdge removes and returns the first delayed edge
func (p *Pool) PopDelayedEdge() *Edge {
	if len(p.delayed) == 0 {
		return nil
	}
	edge := p.delayed[0]
	p.delayed = p.delayed[1:]
	return edge
}

// HasDelayedEdges returns whether there are delayed edges
func (p *Pool) HasDelayedEdges() bool {
	return len(p.delayed) > 0
}

// ClearDelayed clears all delayed edges
func (p *Pool) ClearDelayed() {
	p.delayed = p.delayed[:0]
}