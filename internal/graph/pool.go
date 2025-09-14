package graph

import (
	"fmt"
)

// Pool represents a resource pool for limiting parallel execution
type Pool struct {
	name       string
	depth      int     // Maximum concurrent jobs (0 = infinite)
	currentUse int     // Currently running jobs
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

func (p *Pool) Dump() {
	fmt.Printf("%s (%d/%d) ->\n", p.name, p.currentUse, p.depth)
	for _, edge := range p.delayed {
		fmt.Printf("\t")
		edge.Dump("")
	}
}
