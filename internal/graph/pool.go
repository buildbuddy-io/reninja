package graph

import (
	"fmt"
	"sort"

	"github.com/buildbuddy-io/gin/internal/priority_queue"
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
	// Go does not have a std::set that can be used with a comparator,
	// so instead, keep delayed edges in a sorted slice, and do not insert
	// dupes.
	insertIndex := sort.Search(len(p.delayed), func(i int) bool {
		b := p.delayed[i]
		a := edge
		weightDiff := a.Weight() - b.Weight()
		if weightDiff != 0 {
			return weightDiff < 0
		}
		return EdgePriorityGreater(a, b)
	})
	existingElementIndex := insertIndex - 1
	if len(p.delayed) > 0 && existingElementIndex >= 0 {
		if p.delayed[existingElementIndex] == edge {
			return
		}
	}
	if insertIndex >= len(p.delayed) {
		p.delayed = append(p.delayed, edge)
	} else {
		p.delayed = append(p.delayed[:insertIndex+1], p.delayed[insertIndex:]...)
		p.delayed[insertIndex] = edge
	}
}

func (p *Pool) EdgeScheduled(edge *Edge) {
	if p.depth != 0 {
		p.currentUse += edge.Weight()
	}
}

func (p *Pool) EdgeFinished(edge *Edge) {
	if p.depth != 0 {
		p.currentUse -= edge.Weight()
	}
}

func (p *Pool) RetrieveReadyEdges(readyQueue *priority_queue.ThreadSafePriorityQueue[*Edge]) {
	i := 0
	for i < len(p.delayed) {
		edge := p.delayed[i]
		if p.currentUse+edge.Weight() > p.depth {
			break
		}
		readyQueue.Push(edge)
		p.EdgeScheduled(edge)
		i++
	}
	p.delayed = p.delayed[i:]
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

func (p *Pool) ShouldDelayEdge() bool {
	return p.depth != 0
}
