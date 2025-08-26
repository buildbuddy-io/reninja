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
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/buildbuddy-io/gin/internal/graph"
)

// CommandRunner interface for executing build commands
type CommandRunner interface {
	CanRunMore() bool
	StartCommand(edge *graph.Edge) bool
	WaitForCommand(result *Result) *graph.Edge
	GetActiveEdges() []*graph.Edge
	Abort()
}

// Result represents the result of a command execution
type Result struct {
	Edge      *graph.Edge
	ExitCode  int
	Output    string
	StartTime time.Time
	EndTime   time.Time
	Success   bool
}

// RealCommandRunner executes actual commands
type RealCommandRunner struct {
	mu            sync.Mutex
	runningCmds   map[*graph.Edge]*runningCommand
	maxParallel   int
	useConsole    bool
	dryRun        bool
	verbose       bool
	keepGoing     bool
	failedCount   int
	maxFailures   int
	commandShells []string // Shell to use for commands
}

type runningCommand struct {
	cmd       *exec.Cmd
	edge      *graph.Edge
	output    bytes.Buffer
	startTime time.Time
	done      chan bool
	exitCode  int
}

// NewRealCommandRunner creates a new RealCommandRunner
func NewRealCommandRunner(maxParallel int, verbose, dryRun bool) *RealCommandRunner {
	shells := getCommandShells()
	return &RealCommandRunner{
		runningCmds:   make(map[*graph.Edge]*runningCommand),
		maxParallel:   maxParallel,
		verbose:       verbose,
		dryRun:        dryRun,
		commandShells: shells,
		maxFailures:   1,
	}
}

// getCommandShells returns the shell command to use for the current platform
func getCommandShells() []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd.exe", "/c"}
	}
	// Unix-like systems
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return []string{shell, "-c"}
}

// CanRunMore returns true if more commands can be started
func (r *RealCommandRunner) CanRunMore() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	
	if r.failedCount >= r.maxFailures && !r.keepGoing {
		return false
	}
	
	return len(r.runningCmds) < r.maxParallel
}

// StartCommand starts executing a command for an edge
func (r *RealCommandRunner) StartCommand(edge *graph.Edge) bool {
	command := edge.EvaluateCommand(false)
	if command == "" {
		// No command to run
		return false
	}
	
	if r.dryRun {
		if r.verbose {
			fmt.Println(command)
		}
		return true
	}
	
	// Check if this should use the console pool
	useConsole := edge.Pool() != nil && edge.Pool().Name() == "console"
	
	// Create the command
	shellCmd := append(r.commandShells, command)
	cmd := exec.Command(shellCmd[0], shellCmd[1:]...)
	
	// Set up the command environment
	cmd.Env = os.Environ()
	
	// Create running command
	running := &runningCommand{
		cmd:       cmd,
		edge:      edge,
		startTime: time.Now(),
		done:      make(chan bool, 1),
	}
	
	// Set up output capture or direct output for console pool
	if useConsole {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
	} else {
		cmd.Stdout = &running.output
		cmd.Stderr = &running.output
	}
	
	if r.verbose || useConsole {
		fmt.Println(command)
	}
	
	// Lock before modifying runningCmds
	r.mu.Lock()
	if len(r.runningCmds) >= r.maxParallel {
		r.mu.Unlock()
		return false
	}
	
	// Add to running commands BEFORE starting
	r.runningCmds[edge] = running
	r.mu.Unlock()
	
	// Start the command
	if err := cmd.Start(); err != nil {
		// Remove from running commands on failure
		r.mu.Lock()
		delete(r.runningCmds, edge)
		r.failedCount++
		r.mu.Unlock()
		
		if r.verbose {
			fmt.Fprintf(os.Stderr, "FAILED: %s\n", command)
			fmt.Fprintf(os.Stderr, "%v\n", err)
		}
		return false
	}
	
	// Start goroutine to wait for command completion
	go func() {
		err := cmd.Wait()
		running.exitCode = 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				running.exitCode = exitErr.ExitCode()
			} else {
				running.exitCode = 1
			}
		}
		running.done <- true
	}()
	
	return true
}

// WaitForCommand waits for any command to complete
func (r *RealCommandRunner) WaitForCommand(result *Result) *graph.Edge {
	r.mu.Lock()
	
	if len(r.runningCmds) == 0 {
		r.mu.Unlock()
		return nil
	}
	
	// Create a slice of done channels to wait on
	cases := make([]chan bool, 0, len(r.runningCmds))
	edges := make([]*graph.Edge, 0, len(r.runningCmds))
	for edge, running := range r.runningCmds {
		cases = append(cases, running.done)
		edges = append(edges, edge)
	}
	r.mu.Unlock()
	
	// Wait for any command to complete
	for i, done := range cases {
		select {
		case <-done:
			edge := edges[i]
			
			r.mu.Lock()
			running := r.runningCmds[edge]
			delete(r.runningCmds, edge)
			
			result.Edge = edge
			result.ExitCode = running.exitCode
			result.Output = running.output.String()
			result.StartTime = running.startTime
			result.EndTime = time.Now()
			result.Success = running.exitCode == 0
			
			if !result.Success {
				r.failedCount++
				if !r.keepGoing && r.verbose {
					fmt.Fprintf(os.Stderr, "FAILED: %s\n", edge.EvaluateCommand(false))
					if result.Output != "" {
						fmt.Fprint(os.Stderr, result.Output)
					}
				}
			}
			
			r.mu.Unlock()
			return edge
			
		default:
			continue
		}
	}
	
	// If nothing ready yet, wait on the first one
	<-cases[0]
	edge := edges[0]
	
	r.mu.Lock()
	running := r.runningCmds[edge]
	delete(r.runningCmds, edge)
	
	result.Edge = edge
	result.ExitCode = running.exitCode
	result.Output = running.output.String()
	result.StartTime = running.startTime
	result.EndTime = time.Now()
	result.Success = running.exitCode == 0
	
	if !result.Success {
		r.failedCount++
		if !r.keepGoing && r.verbose {
			fmt.Fprintf(os.Stderr, "FAILED: %s\n", edge.EvaluateCommand(false))
			if result.Output != "" {
				fmt.Fprint(os.Stderr, result.Output)
			}
		}
	}
	
	r.mu.Unlock()
	return edge
}

// GetActiveEdges returns all currently running edges
func (r *RealCommandRunner) GetActiveEdges() []*graph.Edge {
	r.mu.Lock()
	defer r.mu.Unlock()
	
	edges := make([]*graph.Edge, 0, len(r.runningCmds))
	for edge := range r.runningCmds {
		edges = append(edges, edge)
	}
	return edges
}

// Abort stops all running commands
func (r *RealCommandRunner) Abort() {
	r.mu.Lock()
	defer r.mu.Unlock()
	
	for _, running := range r.runningCmds {
		if running.cmd.Process != nil {
			running.cmd.Process.Kill()
		}
	}
}

// SetKeepGoing sets whether to continue on failure
func (r *RealCommandRunner) SetKeepGoing(keepGoing bool, maxFailures int) {
	r.keepGoing = keepGoing
	r.maxFailures = maxFailures
}

// DryCommandRunner simulates command execution without actually running commands
type DryCommandRunner struct {
	verbose bool
}

// NewDryCommandRunner creates a new DryCommandRunner
func NewDryCommandRunner(verbose bool) *DryCommandRunner {
	return &DryCommandRunner{
		verbose: verbose,
	}
}

// CanRunMore always returns true for dry run
func (d *DryCommandRunner) CanRunMore() bool {
	return true
}

// StartCommand simulates starting a command
func (d *DryCommandRunner) StartCommand(edge *graph.Edge) bool {
	command := edge.EvaluateCommand(false)
	if command != "" && d.verbose {
		fmt.Println(command)
	}
	return true
}

// WaitForCommand returns immediately for dry run
func (d *DryCommandRunner) WaitForCommand(result *Result) *graph.Edge {
	return nil
}

// GetActiveEdges returns empty for dry run
func (d *DryCommandRunner) GetActiveEdges() []*graph.Edge {
	return nil
}

// Abort does nothing for dry run
func (d *DryCommandRunner) Abort() {}

// MockCommandRunner provides a mock implementation for testing
type MockCommandRunner struct {
	mu           sync.Mutex
	commands     []string
	shouldFail   map[string]bool
	failOutput   map[string]string
	runningEdges map[*graph.Edge]bool
	maxParallel  int
}

// NewMockCommandRunner creates a new MockCommandRunner
func NewMockCommandRunner(maxParallel int) *MockCommandRunner {
	return &MockCommandRunner{
		shouldFail:   make(map[string]bool),
		failOutput:   make(map[string]string),
		runningEdges: make(map[*graph.Edge]bool),
		maxParallel:  maxParallel,
	}
}

// CanRunMore returns true if more commands can be started
func (m *MockCommandRunner) CanRunMore() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.runningEdges) < m.maxParallel
}

// StartCommand records the command
func (m *MockCommandRunner) StartCommand(edge *graph.Edge) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if len(m.runningEdges) >= m.maxParallel {
		return false
	}
	
	command := edge.EvaluateCommand(false)
	m.commands = append(m.commands, command)
	m.runningEdges[edge] = true
	return true
}

// WaitForCommand returns a completed edge
func (m *MockCommandRunner) WaitForCommand(result *Result) *graph.Edge {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	for edge := range m.runningEdges {
		delete(m.runningEdges, edge)
		
		command := edge.EvaluateCommand(false)
		result.Edge = edge
		result.Success = !m.shouldFail[command]
		result.ExitCode = 0
		if !result.Success {
			result.ExitCode = 1
			result.Output = m.failOutput[command]
		}
		result.StartTime = time.Now()
		result.EndTime = time.Now()
		
		return edge
	}
	
	return nil
}

// GetActiveEdges returns currently running edges
func (m *MockCommandRunner) GetActiveEdges() []*graph.Edge {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	edges := make([]*graph.Edge, 0, len(m.runningEdges))
	for edge := range m.runningEdges {
		edges = append(edges, edge)
	}
	return edges
}

// Abort clears running edges
func (m *MockCommandRunner) Abort() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runningEdges = make(map[*graph.Edge]bool)
}

// GetCommands returns all commands that were run
func (m *MockCommandRunner) GetCommands() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.commands...)
}

// SetShouldFail marks a command to fail
func (m *MockCommandRunner) SetShouldFail(command string, output string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shouldFail[command] = true
	m.failOutput[command] = output
}

// makeDirectories creates all directories needed for outputs
func makeDirectories(outputs []*graph.Node) error {
	dirs := make(map[string]bool)
	
	for _, output := range outputs {
		dir := strings.TrimSuffix(output.Path(), "/")
		if idx := strings.LastIndex(dir, "/"); idx > 0 {
			dir = dir[:idx]
			dirs[dir] = true
		}
	}
	
	for dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}
	
	return nil
}