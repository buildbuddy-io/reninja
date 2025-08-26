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

package build

import (
	"fmt"

	"github.com/buildbuddy-io/gin/internal/disk"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/log"
	"github.com/buildbuddy-io/gin/internal/state"
)

// DependencyScan manages dependency scanning
type DependencyScan struct {
	state         *state.State
	diskInterface disk.Interface
	buildLog      *log.BuildLog
	depsLog       *log.DepsLog
}

// NewDependencyScan creates a new DependencyScan
func NewDependencyScan(s *state.State, buildLog *log.BuildLog, depsLog *log.DepsLog, diskInterface disk.Interface) *DependencyScan {
	if diskInterface == nil {
		diskInterface = disk.NewRealDiskInterface()
	}
	return &DependencyScan{
		state:         s,
		diskInterface: diskInterface,
		buildLog:      buildLog,
		depsLog:       depsLog,
	}
}

// RecomputeDirty recomputes dirty state for a node and its dependencies
func (d *DependencyScan) RecomputeDirty(node *graph.Node) error {
	visited := make(map[*graph.Node]bool)
	return d.recomputeDirtyInternal(node, visited)
}

func (d *DependencyScan) recomputeDirtyInternal(node *graph.Node, visited map[*graph.Node]bool) error {
	// Check for cycles
	if visited[node] {
		return nil // Already visited, skip to avoid cycles
	}
	visited[node] = true
	// First, stat the node if needed
	if !node.StatusKnown() {
		if err := node.StatIfNecessary(d.diskInterface); err != nil {
			// File doesn't exist - that's OK if it's generated
			if !disk.IsNotExist(err) {
				return fmt.Errorf("stat '%s': %w", node.Path(), err)
			}
		}
	}
	
	// If the node has no in-edge, it's a source file
	edge := node.InEdge()
	if edge == nil {
		// Source files are dirty if they don't exist
		if !node.Exists() && !node.GeneratedByDepLoader() {
			return fmt.Errorf("'%s' missing and no known rule to make it", node.Path())
		}
		return nil
	}
	
	// Check all inputs recursively
	for _, input := range edge.Inputs() {
		if err := d.recomputeDirtyInternal(input, visited); err != nil {
			return err
		}
	}
	
	// Now determine if this edge needs to be rebuilt
	dirty := d.recomputeOutputDirty(edge, node)
	node.SetDirty(dirty)
	
	// If any output is dirty, mark the edge as needing to build
	if dirty {
		edge.SetOutputsReady(false)
		// Mark all outputs as dirty
		for _, output := range edge.Outputs() {
			output.SetDirty(true)
		}
	} else {
		edge.SetOutputsReady(true)
	}
	
	return nil
}

// recomputeOutputDirty checks if an output needs to be rebuilt
func (d *DependencyScan) recomputeOutputDirty(edge *graph.Edge, output *graph.Node) bool {
	// Phony edges are always dirty
	if edge.IsPhony() {
		return true
	}
	
	// Stat the output if needed
	if !output.StatusKnown() {
		output.StatIfNecessary(d.diskInterface)
	}
	
	// Missing outputs are dirty
	if !output.Exists() {
		return true
	}
	
	// Check if any input is newer than the output
	outputMtime := output.Mtime()
	for _, input := range edge.Inputs() {
		// Ensure input is statted
		if !input.StatusKnown() {
			input.StatIfNecessary(d.diskInterface)
		}
		
		// Check if input is dirty
		if input.Dirty() {
			return true
		}
		
		// Check if input is newer than output
		if input.Exists() && input.Mtime() > outputMtime {
			return true
		}
		
		// Check if input is missing (and not generated)
		if !input.Exists() && !input.GeneratedByDepLoader() && input.InEdge() == nil {
			// Missing source file
			return true
		}
	}
	
	// Check if the command changed using build log
	if d.buildLog != nil {
		logEntry := d.buildLog.GetEntry(output.Path())
		if logEntry != nil {
			// Calculate current command hash
			currentCommand := edge.EvaluateCommand(false)
			currentHash := hashCommand(currentCommand)
			
			// If command changed, rebuild
			if logEntry.CommandHash != currentHash {
				return true
			}
			
			// If output mtime doesn't match log, rebuild
			if logEntry.RestatMtime != output.Mtime() && logEntry.RestatMtime != 0 {
				return true
			}
		} else {
			// No log entry means we haven't built this before
			return true
		}
	}
	
	// Check dependencies from deps log
	if d.depsLog != nil {
		depsRecord := d.depsLog.GetDeps(output)
		if depsRecord != nil {
			// Check if any recorded dependency is newer
			for _, dep := range depsRecord.Dependencies {
				if !dep.StatusKnown() {
					dep.StatIfNecessary(d.diskInterface)
				}
				if dep.Exists() && dep.Mtime() > outputMtime {
					return true
				}
			}
		}
	}
	
	return false
}

// hashCommand computes a hash of a command string
func hashCommand(command string) uint64 {
	// Simple FNV-1a hash
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	
	hash := uint64(offset64)
	for i := 0; i < len(command); i++ {
		hash ^= uint64(command[i])
		hash *= prime64
	}
	return hash
}

// LoadDynamicDeps loads dynamic dependencies for an edge
func (d *DependencyScan) LoadDynamicDeps(edge *graph.Edge) error {
	// TODO: Implement dynamic dependency loading from depfiles
	// For now, just mark as loaded
	edge.SetDepsLoaded(true)
	return nil
}

// EdgeFinished is called when an edge finishes building
func (d *DependencyScan) EdgeFinished(edge *graph.Edge, success bool) {
	if !success {
		return
	}
	
	// Update output mtimes
	for _, output := range edge.Outputs() {
		if mtime, err := d.diskInterface.Stat(output.Path()); err == nil {
			output.SetMtime(mtime)
		}
	}
	
	// TODO: Update build log
	// TODO: Update deps log if we loaded deps from depfile
}

// Plan manages the build execution plan
type Plan struct {
	wantEdges  map[*graph.Edge]Want
	readyQueue []*graph.Edge
}

type Want int

const (
	WantNothing Want = iota
	WantToStart
	WantToFinish
)

// NewPlan creates a new Plan
func NewPlan() *Plan {
	return &Plan{
		wantEdges:  make(map[*graph.Edge]Want),
		readyQueue: make([]*graph.Edge, 0),
	}
}

// AddTarget adds a target to the plan
func (p *Plan) AddTarget(node *graph.Node) error {
	return p.addSubTarget(node)
}

func (p *Plan) addSubTarget(node *graph.Node) error {
	edge := node.InEdge()
	if edge == nil {
		// Source file or generated file with no rule
		return nil
	}
	
	want := p.wantEdges[edge]
	if want != WantNothing {
		// Already in plan
		return nil
	}
	
	// Mark edge as wanted
	p.wantEdges[edge] = WantToStart
	
	// Add dependencies first
	for _, input := range edge.Inputs() {
		if err := p.addSubTarget(input); err != nil {
			return err
		}
	}
	
	// Check if this edge is ready to build
	if p.edgeReady(edge) {
		p.scheduleEdge(edge)
	}
	
	return nil
}

func (p *Plan) edgeReady(edge *graph.Edge) bool {
	// Check if all inputs are ready
	for _, input := range edge.Inputs() {
		if input.InEdge() != nil {
			inputWant := p.wantEdges[input.InEdge()]
			// Edge is NOT ready if input is still WantToStart or WantToFinish
			if inputWant == WantToStart || inputWant == WantToFinish {
				// Input edge hasn't finished yet
				return false
			}
			// WantNothing means it's not in the plan (already done or not needed)
		}
	}
	return true
}

func (p *Plan) scheduleEdge(edge *graph.Edge) {
	// Check if edge has a pool that might delay it
	pool := edge.Pool()
	if pool != nil && pool.Depth() != 0 {
		// Check if pool is full
		if !pool.Available() {
			// Pool is full, delay the edge
			pool.DelayEdge(edge)
			return
		}
	}
	
	p.readyQueue = append(p.readyQueue, edge)
	// Don't change the want state here - it's still WantToStart until actually executed
}

// PopReadyEdge returns the next edge to build
func (p *Plan) PopReadyEdge() *graph.Edge {
	for len(p.readyQueue) > 0 {
		edge := p.readyQueue[0]
		p.readyQueue = p.readyQueue[1:]
		
		// Double-check the edge is still wanted
		if want := p.wantEdges[edge]; want == WantToStart {
			// Check if it's actually dirty
			dirty := false
			for _, output := range edge.Outputs() {
				if output.Dirty() {
					dirty = true
					break
				}
			}
			
			if !dirty {
				// Not actually dirty, mark as done
				delete(p.wantEdges, edge)
				p.checkNewlyReady(edge)
				continue
			}
			
			// Try to acquire pool resources if needed
			pool := edge.Pool()
			if pool != nil && pool.Depth() != 0 {
				if !pool.Acquire() {
					// Pool is full, delay the edge
					pool.DelayEdge(edge)
					continue
				}
			}
			
			// Mark as running
			p.wantEdges[edge] = WantToFinish
			return edge
		}
	}
	return nil
}

// EdgeFinished marks an edge as finished
func (p *Plan) EdgeFinished(edge *graph.Edge) {
	// Release pool resources if needed
	pool := edge.Pool()
	if pool != nil && pool.Depth() != 0 {
		pool.Release()
		
		// Check if any delayed edges can now run
		for pool.HasDelayedEdges() && pool.Available() {
			delayedEdge := pool.PopDelayedEdge()
			if delayedEdge != nil {
				// Re-add to ready queue
				p.readyQueue = append(p.readyQueue, delayedEdge)
			}
		}
	}
	
	delete(p.wantEdges, edge)
	p.checkNewlyReady(edge)
}

func (p *Plan) checkNewlyReady(finishedEdge *graph.Edge) {
	// Check if any edges depending on this one are now ready
	for _, output := range finishedEdge.Outputs() {
		for _, outEdge := range output.OutEdges() {
			if want := p.wantEdges[outEdge]; want == WantToStart {
				if p.edgeReady(outEdge) {
					p.scheduleEdge(outEdge)
				}
			}
		}
	}
}

// MoreToStart returns whether there are more edges to start
func (p *Plan) MoreToStart() bool {
	return len(p.readyQueue) > 0 || p.hasWaitingEdges()
}

func (p *Plan) hasWaitingEdges() bool {
	for _, want := range p.wantEdges {
		if want == WantToStart {
			return true
		}
	}
	return false
}

// EdgeCount returns the number of edges in the plan
func (p *Plan) EdgeCount() int {
	count := 0
	for edge := range p.wantEdges {
		// Only count edges that actually need to build
		for _, output := range edge.Outputs() {
			if output.Dirty() {
				count++
				break
			}
		}
	}
	return count
}

// GetWantedEdges returns all edges in the plan
func (p *Plan) GetWantedEdges() []*graph.Edge {
	edges := make([]*graph.Edge, 0, len(p.wantEdges))
	for edge := range p.wantEdges {
		edges = append(edges, edge)
	}
	return edges
}