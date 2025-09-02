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

package clean

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/buildbuddy-io/gin/internal/disk"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/state"
)

// Cleaner removes built files
type Cleaner struct {
	state         *state.State
	diskInterface disk.Interface
	verbose       bool
	dryRun        bool
	generator     bool // Clean files marked as generator outputs
	cleanRules    bool // Clean outputs from specific rules
}

// Config represents cleaner configuration
type Config struct {
	Verbose   bool
	DryRun    bool
	Generator bool
	Rules     bool
}

// New creates a new Cleaner
func New(s *state.State, config *Config) *Cleaner {
	diskInt := disk.NewRealDiskInterface()

	return &Cleaner{
		state:         s,
		diskInterface: diskInt,
		verbose:       config.Verbose,
		dryRun:        config.DryRun,
		generator:     config.Generator,
		cleanRules:    config.Rules,
	}
}

// CleanAll removes all built files
func (c *Cleaner) CleanAll() (int, error) {
	fmt.Println("Cleaning all built files...")

	cleaned := 0
	errors := []string{}

	// Clean all edges
	for _, edge := range c.state.Edges() {
		if c.shouldCleanEdge(edge) {
			for _, output := range edge.Outputs() {
				if err := c.removeFile(output.Path()); err != nil {
					errors = append(errors, err.Error())
				} else {
					cleaned++
				}
			}
		}
	}

	// Clean build log and deps log
	if !c.generator {
		c.cleanLog(".ninja_log")
		c.cleanLog(".ninja_deps")
	}

	if len(errors) > 0 {
		return cleaned, fmt.Errorf("failed to clean some files:\n%s", strings.Join(errors, "\n"))
	}

	return cleaned, nil
}

// CleanTargets removes specified targets and their outputs
func (c *Cleaner) CleanTargets(targets []string) (int, error) {
	if len(targets) == 0 {
		return 0, fmt.Errorf("no targets specified")
	}

	fmt.Printf("Cleaning %d targets...\n", len(targets))

	cleaned := 0
	errors := []string{}

	for _, target := range targets {
		node := c.state.LookupNode(target)
		if node == nil {
			errors = append(errors, fmt.Sprintf("unknown target '%s'", target))
			continue
		}

		count, err := c.cleanNode(node)
		cleaned += count
		if err != nil {
			errors = append(errors, err.Error())
		}
	}

	if len(errors) > 0 {
		return cleaned, fmt.Errorf("failed to clean some targets:\n%s", strings.Join(errors, "\n"))
	}

	return cleaned, nil
}

// CleanRule removes all outputs from a specific rule
func (c *Cleaner) CleanRule(ruleName string) (int, error) {
	rule := c.state.LookupRule(ruleName)
	if rule == nil {
		return 0, fmt.Errorf("unknown rule '%s'", ruleName)
	}

	fmt.Printf("Cleaning outputs from rule '%s'...\n", ruleName)

	cleaned := 0
	errors := []string{}

	for _, edge := range c.state.Edges() {
		if edge.Rule() == rule {
			for _, output := range edge.Outputs() {
				if err := c.removeFile(output.Path()); err != nil {
					errors = append(errors, err.Error())
				} else {
					cleaned++
				}
			}
		}
	}

	if len(errors) > 0 {
		return cleaned, fmt.Errorf("failed to clean some files:\n%s", strings.Join(errors, "\n"))
	}

	return cleaned, nil
}

// cleanNode removes a node's outputs and recursively cleans dependencies if needed
func (c *Cleaner) cleanNode(node *graph.Node) (int, error) {
	cleaned := 0

	// If node has an in-edge, it's a generated file
	if edge := node.InEdge(); edge != nil {
		// Clean the output
		if err := c.removeFile(node.Path()); err == nil {
			cleaned++
		}

		// Clean response file if present
		if rspfile := edge.GetUnescapedRspfile(); rspfile != "" {
			if err := c.removeFile(rspfile); err == nil {
				cleaned++
			}
		}

		// Recursively clean inputs if they're generated
		if c.generator {
			for _, input := range edge.Inputs() {
				if input.InEdge() != nil {
					count, _ := c.cleanNode(input)
					cleaned += count
				}
			}
		}
	} else {
		// Source file, try to remove if it's in the build directory
		if c.isInBuildDir(node.Path()) {
			if err := c.removeFile(node.Path()); err == nil {
				cleaned++
			}
		}
	}

	return cleaned, nil
}

// shouldCleanEdge determines if an edge's outputs should be cleaned
func (c *Cleaner) shouldCleanEdge(edge *graph.Edge) bool {
	// Don't clean phony targets
	if edge.IsPhony() {
		return false
	}

	// If generator mode, only clean generator outputs
	if c.generator {
		binding := edge.GetBinding("generator")
		if binding == "1" || binding == "true" {
			return true
		}
		return false
	}

	return true
}

// removeFile removes a file and prints status
func (c *Cleaner) removeFile(path string) error {
	if c.verbose {
		fmt.Printf("Remove %s\n", path)
	}

	if c.dryRun {
		return nil
	}

	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove %s: %w", path, err)
	}

	// Try to remove empty parent directories
	c.removeEmptyDirs(filepath.Dir(path))

	return nil
}

// removeEmptyDirs removes empty directories up the tree
func (c *Cleaner) removeEmptyDirs(dir string) {
	if dir == "." || dir == "/" || dir == "" {
		return
	}

	// Check if directory is empty
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > 0 {
		return
	}

	// Remove empty directory
	if err := os.Remove(dir); err == nil {
		if c.verbose {
			fmt.Printf("Removed empty directory %s\n", dir)
		}
		// Recursively check parent
		c.removeEmptyDirs(filepath.Dir(dir))
	}
}

// cleanLog removes a log file
func (c *Cleaner) cleanLog(filename string) {
	if c.verbose {
		fmt.Printf("Remove %s\n", filename)
	}

	if !c.dryRun {
		os.Remove(filename)
	}
}

// isInBuildDir checks if a path is in the build directory
func (c *Cleaner) isInBuildDir(path string) bool {
	// Simple heuristic: if path doesn't start with .. or /, it's in build dir
	if filepath.IsAbs(path) {
		return false
	}
	if strings.HasPrefix(path, "..") {
		return false
	}
	return true
}

// PrintWouldClean prints what would be cleaned without actually doing it
func (c *Cleaner) PrintWouldClean() error {
	count := 0

	for _, edge := range c.state.Edges() {
		if c.shouldCleanEdge(edge) {
			for _, output := range edge.Outputs() {
				fmt.Println(output.Path())
				count++
			}
		}
	}

	if !c.generator {
		if _, err := os.Stat(".ninja_log"); err == nil {
			fmt.Println(".ninja_log")
			count++
		}
		if _, err := os.Stat(".ninja_deps"); err == nil {
			fmt.Println(".ninja_deps")
			count++
		}
	}

	fmt.Printf("Would remove %d files.\n", count)
	return nil
}

// GetCleanableFiles returns a list of files that would be cleaned
func (c *Cleaner) GetCleanableFiles() []string {
	var files []string

	for _, edge := range c.state.Edges() {
		if c.shouldCleanEdge(edge) {
			for _, output := range edge.Outputs() {
				files = append(files, output.Path())
			}
		}
	}

	if !c.generator {
		if _, err := os.Stat(".ninja_log"); err == nil {
			files = append(files, ".ninja_log")
		}
		if _, err := os.Stat(".ninja_deps"); err == nil {
			files = append(files, ".ninja_deps")
		}
	}

	return files
}

// CleanDeadFiles removes files that are no longer generated by any edge
func (c *Cleaner) CleanDeadFiles() (int, error) {
	fmt.Println("Cleaning dead files...")

	// Build a map of all current outputs
	currentOutputs := make(map[string]bool)
	for _, edge := range c.state.Edges() {
		for _, output := range edge.Outputs() {
			currentOutputs[output.Path()] = true
		}
	}

	// Find and remove files in build directory that aren't current outputs
	cleaned := 0
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files we can't access
		}

		if info.IsDir() {
			// Skip hidden directories
			if strings.HasPrefix(info.Name(), ".") && path != "." {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip if it's a current output
		if currentOutputs[path] {
			return nil
		}

		// Skip source files (no in-edge)
		if node := c.state.LookupNode(path); node != nil && node.InEdge() == nil {
			return nil
		}

		// Skip files outside build directory
		if !c.isInBuildDir(path) {
			return nil
		}

		// This looks like a dead file, remove it
		if err := c.removeFile(path); err == nil {
			cleaned++
		}

		return nil
	})

	return cleaned, err
}
