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
	"os"
	"sync"
	"time"

	"github.com/buildbuddy-io/gin/internal/disk"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/log"
	"github.com/buildbuddy-io/gin/internal/state"
)

// Config represents build configuration
type Config struct {
	Parallelism     int
	KeepGoing       int
	DryRun          bool
	Verbose         bool
	FailuresAllowed int
}

// Builder orchestrates the build process
type Builder struct {
	state         *state.State
	config        *Config
	plan          *Plan
	status        *StatusPrinter
	commandRunner CommandRunner
	diskInterface disk.Interface
	scan          *DependencyScan
	buildLog      *log.BuildLog
	depsLog       *log.DepsLog

	mu           sync.Mutex
	runningEdges map[*graph.Edge]*RunningEdge
	failedEdges  []*graph.Edge
	totalEdges   int
	builtEdges   int
	startTime    time.Time
}

// RunningEdge tracks a running edge's start time
type RunningEdge struct {
	edge      *graph.Edge
	startTime int64
}

// New creates a new Builder
func New(s *state.State, config *Config) *Builder {
	var commandRunner CommandRunner
	if config.DryRun {
		commandRunner = NewDryCommandRunner(config.Verbose)
	} else {
		commandRunner = NewRealCommandRunner(config.Parallelism, config.Verbose, false)
		if runner, ok := commandRunner.(*RealCommandRunner); ok {
			runner.SetKeepGoing(config.KeepGoing > 1, config.KeepGoing)
		}
	}

	diskInterface := disk.NewRealDiskInterface()

	// Initialize logs
	buildLog := log.NewBuildLog()
	depsLog := log.NewDepsLog()

	// Open logs for writing
	buildLog.OpenForWrite(".ninja_log", "")
	depsLog.OpenForWrite(".ninja_deps", "")

	return &Builder{
		state:         s,
		config:        config,
		plan:          NewPlan(),
		status:        NewStatusPrinter(),
		commandRunner: commandRunner,
		diskInterface: diskInterface,
		scan:          NewDependencyScan(s, buildLog, depsLog, diskInterface),
		runningEdges:  make(map[*graph.Edge]*RunningEdge),
		failedEdges:   make([]*graph.Edge, 0),
		buildLog:      buildLog,
		depsLog:       depsLog,
	}
}

// Build builds the specified targets
func (b *Builder) Build(targets []string) error {
	b.startTime = time.Now()

	// Resolve target nodes
	var nodes []*graph.Node
	for _, target := range targets {
		node := b.state.LookupNode(target)
		if node == nil {
			return fmt.Errorf("unknown target '%s'", target)
		}
		nodes = append(nodes, node)
	}

	// Add targets to plan
	for _, node := range nodes {
		if err := b.plan.AddTarget(node); err != nil {
			return err
		}
	}

	// Compute what needs to be built
	for _, node := range nodes {
		if err := b.scan.RecomputeDirty(node); err != nil {
			return err
		}
	}

	// Check if there's work to do
	b.totalEdges = b.plan.EdgeCount()
	if b.totalEdges == 0 {
		fmt.Println("ninja: no work to do.")
		return nil
	}

	// Start the build
	if !b.config.Verbose && !b.config.DryRun {
		b.status.Info("Building %d targets...", b.totalEdges)
	}

	// Build loop
	failureCount := 0
	for {
		// Start as many edges as possible
		for b.commandRunner.CanRunMore() && b.plan.MoreToStart() {
			edge := b.plan.PopReadyEdge()
			if edge == nil {
				break
			}

			// Handle phony edges immediately without running commands
			if edge.IsPhony() {
				// Mark as complete immediately
				b.plan.EdgeFinished(edge)
				// Update status
				if !b.config.Verbose && !b.config.DryRun {
					b.status.BuildEdgeFinished(edge, true, "")
				}
				// Mark outputs as clean
				for _, output := range edge.Outputs() {
					output.SetDirty(false)
				}
				continue
			}

			if !b.startEdge(edge) {
				failureCount++
				if failureCount >= b.config.KeepGoing {
					break
				}
			}
		}

		// Check if we're done
		if len(b.runningEdges) == 0 {
			break
		}

		// Wait for a command to finish
		result := &Result{}
		edge := b.commandRunner.WaitForCommand(result)
		if edge != nil {
			b.finishEdge(edge, result)
			if !result.Success {
				failureCount++
				if failureCount >= b.config.KeepGoing {
					// Stop starting new commands
					if !b.config.Verbose {
						b.status.Error("Build stopped: %s", result.Output)
					}
					// Wait for running commands to finish
					b.waitForRunningCommands()
					break
				}
			}
		}
	}

	// Save logs
	if err := b.buildLog.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to save build log: %v\n", err)
	}
	if err := b.depsLog.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to save deps log: %v\n", err)
	}

	// Final status
	elapsed := time.Since(b.startTime)
	if failureCount > 0 {
		if !b.config.Verbose {
			b.status.Error("FAILED: %d/%d targets failed in %.2fs",
				failureCount, b.totalEdges, elapsed.Seconds())
		}
		return fmt.Errorf("build stopped: %d targets failed", failureCount)
	}

	if !b.config.Verbose && !b.config.DryRun {
		b.status.Info("Built %d targets in %.2fs", b.builtEdges, elapsed.Seconds())
	}

	return nil
}

func (b *Builder) startEdge(edge *graph.Edge) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Create output directories if needed
	for _, output := range edge.Outputs() {
		dir := disk.GetDirName(output.Path())
		if dir != "." && dir != "/" {
			if err := b.diskInterface.MakeDir(dir); err != nil {
				if b.config.Verbose {
					fmt.Fprintf(os.Stderr, "failed to create directory: %v\n", err)
				}
				return false
			}
		}
	}

	// Create response file if needed
	rspfile := edge.GetUnescapedRspfile()
	if rspfile != "" && !b.config.DryRun {
		content := edge.GetBinding("rspfile_content")
		if err := b.diskInterface.WriteFile(rspfile, []byte(content)); err != nil {
			if b.config.Verbose {
				fmt.Fprintf(os.Stderr, "failed to write response file %s: %v\n", rspfile, err)
			}
			return false
		}
	}

	// Track as running with start time
	b.runningEdges[edge] = &RunningEdge{
		edge:      edge,
		startTime: time.Now().UnixMilli(),
	}

	// Update status
	if !b.config.Verbose && !b.config.DryRun {
		b.status.BuildEdgeStarted(edge, b.builtEdges+len(b.runningEdges), b.totalEdges)
	}

	// Start the command
	if !b.commandRunner.StartCommand(edge) {
		delete(b.runningEdges, edge)
		return false
	}

	return true
}

func (b *Builder) finishEdge(edge *graph.Edge, result *Result) {
	b.mu.Lock()
	defer b.mu.Unlock()

	runningEdge := b.runningEdges[edge]
	delete(b.runningEdges, edge)
	b.builtEdges++

	if result.Success {
		// Mark outputs as clean
		var restatMtime graph.TimeStamp
		for _, output := range edge.Outputs() {
			output.SetDirty(false)
			// Update mtime
			if mtime, err := b.diskInterface.Stat(output.Path()); err == nil {
				output.SetMtime(mtime)
				if restatMtime == 0 {
					restatMtime = mtime
				}
			}
		}

		// Record command in build log
		if b.buildLog != nil && runningEdge != nil {
			endTime := time.Now().UnixMilli()
			b.buildLog.RecordCommand(edge, runningEdge.startTime, endTime, restatMtime)
		}

		// TODO: Handle deps log for dependency types
	} else {
		b.failedEdges = append(b.failedEdges, edge)
	}

	// Notify plan that edge is done
	b.plan.EdgeFinished(edge)

	// Update status
	if !b.config.Verbose && !b.config.DryRun {
		b.status.BuildEdgeFinished(edge, result.Success, result.Output)
	}
}

func (b *Builder) waitForRunningCommands() {
	for len(b.runningEdges) > 0 {
		result := &Result{}
		edge := b.commandRunner.WaitForCommand(result)
		if edge != nil {
			b.finishEdge(edge, result)
		}
	}
}

// StatusPrinter handles build status output
type StatusPrinter struct {
	mu        sync.Mutex
	lastLine  string
	verbose   bool
	startTime time.Time
}

// NewStatusPrinter creates a new StatusPrinter
func NewStatusPrinter() *StatusPrinter {
	return &StatusPrinter{
		startTime: time.Now(),
	}
}

// Info prints an info message
func (s *StatusPrinter) Info(format string, args ...interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.clearLine()
	fmt.Printf(format+"\n", args...)
}

// Error prints an error message
func (s *StatusPrinter) Error(format string, args ...interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.clearLine()
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// BuildEdgeStarted notifies that an edge has started
func (s *StatusPrinter) BuildEdgeStarted(edge *graph.Edge, running, total int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.verbose {
		return
	}

	// Get first output name for display
	outputName := ""
	if len(edge.Outputs()) > 0 {
		outputName = edge.Outputs()[0].Path()
		if len(outputName) > 30 {
			outputName = "..." + outputName[len(outputName)-27:]
		}
	}

	elapsed := time.Since(s.startTime)
	status := fmt.Sprintf("[%d/%d] %.1fs | %s", running, total, elapsed.Seconds(), outputName)

	s.printStatus(status)
}

// BuildEdgeFinished notifies that an edge has finished
func (s *StatusPrinter) BuildEdgeFinished(edge *graph.Edge, success bool, output string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !success && output != "" {
		s.clearLine()
		fmt.Fprint(os.Stderr, output)
	}
}

func (s *StatusPrinter) printStatus(status string) {
	// Clear previous line
	s.clearLine()

	// Print new status (without newline for updating in place)
	fmt.Print(status)
	s.lastLine = status
}

func (s *StatusPrinter) clearLine() {
	if s.lastLine != "" {
		// Move cursor to beginning of line and clear it
		fmt.Printf("\r%*s\r", len(s.lastLine), "")
		s.lastLine = ""
	}
}

// SetVerbose sets verbose mode
func (s *StatusPrinter) SetVerbose(verbose bool) {
	s.verbose = verbose
}
