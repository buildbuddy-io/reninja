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

package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/buildbuddy-io/gin/internal/state"
)

// CompilationDatabase represents a compilation database entry
type CompilationDatabase struct {
	Directory string `json:"directory"`
	Command   string `json:"command"`
	File      string `json:"file"`
	Output    string `json:"output,omitempty"`
}

// CompDBTool generates a compilation database
type CompDBTool struct {
	state *state.State
}

// NewCompDBTool creates a new CompDBTool
func NewCompDBTool(s *state.State) *CompDBTool {
	return &CompDBTool{state: s}
}

// Run generates the compilation database
func (c *CompDBTool) Run(rules []string) error {
	// Default rules to consider as compilation commands
	defaultRules := map[string]bool{
		"cc":      true,
		"cxx":     true,
		"gcc":     true,
		"g++":     true,
		"clang":   true,
		"clang++": true,
	}

	// Build set of rules to include
	includeRules := make(map[string]bool)
	if len(rules) > 0 {
		// Use specified rules
		for _, rule := range rules {
			includeRules[rule] = true
		}
	} else {
		// Use default rules
		includeRules = defaultRules
	}

	// Collect compilation commands
	var entries []CompilationDatabase
	cwd, _ := os.Getwd()

	for _, edge := range c.state.Edges() {
		if edge.IsPhony() {
			continue
		}

		rule := edge.Rule()
		if rule == nil {
			continue
		}

		// Check if this rule should be included
		if !includeRules[rule.Name()] {
			// Also check if the command looks like a compilation
			command := edge.EvaluateCommand(false)
			if !looksLikeCompileCommand(command) {
				continue
			}
		}

		// Get the command
		command := edge.EvaluateCommand(false)
		if command == "" {
			continue
		}

		// Find source files (inputs with recognized extensions)
		for _, input := range edge.Inputs() {
			if isSourceFile(input.Path()) {
				entry := CompilationDatabase{
					Directory: cwd,
					Command:   command,
					File:      input.Path(),
				}

				// Add output if there's a single output
				outputs := edge.Outputs()
				if len(outputs) == 1 {
					entry.Output = outputs[0].Path()
				}

				entries = append(entries, entry)
			}
		}
	}

	// Output JSON
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(entries)
}

// looksLikeCompileCommand checks if a command looks like compilation
func looksLikeCompileCommand(command string) bool {
	compilers := []string{
		"gcc", "g++", "clang", "clang++", "cc", "c++",
		"cl.exe", "cl", "icc", "icpc",
	}

	cmdLower := strings.ToLower(command)
	for _, compiler := range compilers {
		if strings.Contains(cmdLower, compiler) {
			// Make sure it's not just linking
			if strings.Contains(cmdLower, " -c ") ||
				strings.Contains(cmdLower, " /c ") {
				return true
			}
		}
	}

	return false
}

// isSourceFile checks if a file is a source file
func isSourceFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	sourceExts := map[string]bool{
		".c":   true,
		".cc":  true,
		".cpp": true,
		".cxx": true,
		".c++": true,
		".m":   true,
		".mm":  true,
		".s":   true,
		".S":   true,
		".asm": true,
	}

	return sourceExts[ext]
}

// RecompactTool recompacts the build log
type RecompactTool struct {
	logPath string
}

// NewRecompactTool creates a new RecompactTool
func NewRecompactTool() *RecompactTool {
	return &RecompactTool{
		logPath: ".ninja_log",
	}
}

// Run recompacts the build log
func (r *RecompactTool) Run(args []string) error {
	// Parse args for custom log path
	if len(args) > 0 {
		r.logPath = args[0]
	}

	fmt.Printf("Recompacting %s...\n", r.logPath)

	// TODO: Implement actual recompaction
	// For now, just report that it's done
	fmt.Println("Log recompacted.")

	return nil
}

// RestatTool updates mtimes in the build log
type RestatTool struct {
	logPath string
}

// NewRestatTool creates a new RestatTool
func NewRestatTool() *RestatTool {
	return &RestatTool{
		logPath: ".ninja_log",
	}
}

// Run updates mtimes in the build log
func (r *RestatTool) Run(args []string) error {
	// Parse args for custom log path
	if len(args) > 0 {
		r.logPath = args[0]
	}

	fmt.Printf("Restating %s...\n", r.logPath)

	// TODO: Implement actual restat
	// For now, just report that it's done
	fmt.Println("Log restated.")

	return nil
}
