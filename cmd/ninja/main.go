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

package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/buildbuddy-io/gin/internal/build"
	"github.com/buildbuddy-io/gin/internal/clean"
	"github.com/buildbuddy-io/gin/internal/parser"
	"github.com/buildbuddy-io/gin/internal/state"
	"github.com/buildbuddy-io/gin/internal/tools"
)

const (
	// Version information
	version = "1.12.0-gin"
)

// Options represents command-line options
type Options struct {
	inputFile          string
	workingDir         string
	parallelism        int
	keepGoing          int
	dryRun             bool
	verbose            bool
	showVersion        bool
	showHelp           bool
	debugMode          string
	targets            []string
	toolMode           string
	phonyCycleShouldErr bool
	remoteEndpoint     string
	remoteInstance     string
}

func main() {
	opts := parseOptions()

	if opts.showHelp {
		showHelp()
		os.Exit(0)
	}

	if opts.showVersion {
		fmt.Printf("gin %s\n", version)
		os.Exit(0)
	}

	// Change to working directory if specified
	if opts.workingDir != "" {
		if err := os.Chdir(opts.workingDir); err != nil {
			fmt.Fprintf(os.Stderr, "gin: error: %v\n", err)
			os.Exit(1)
		}
	}

	// Run tool mode if specified
	if opts.toolMode != "" {
		// Pass the input file through args for tools that need it
		toolArgs := opts.targets
		if opts.inputFile != "build.ninja" {
			toolArgs = append([]string{"-f", opts.inputFile}, toolArgs...)
		}
		runTool(opts.toolMode, toolArgs)
		return
	}

	// Load build file
	s := state.New()
	p := parser.New(s)
	
	if err := p.ParseFile(opts.inputFile); err != nil {
		fmt.Fprintf(os.Stderr, "gin: error: %v\n", err)
		os.Exit(1)
	}

	// Determine targets to build
	targets := opts.targets
	if len(targets) == 0 {
		// Use default targets if no targets specified
		defaults := s.Defaults()
		if len(defaults) == 0 {
			fmt.Fprintf(os.Stderr, "gin: error: no targets specified and no default target\n")
			os.Exit(1)
		}
		for _, node := range defaults {
			targets = append(targets, node.Path())
		}
	}

	// Create build configuration
	config := &build.Config{
		Parallelism:    opts.parallelism,
		KeepGoing:      opts.keepGoing,
		DryRun:         opts.dryRun,
		Verbose:        opts.verbose,
		RemoteEndpoint: opts.remoteEndpoint,
		RemoteInstance: opts.remoteInstance,
	}

	// Build targets
	builder := build.New(s, config)
	if err := builder.Build(targets); err != nil {
		fmt.Fprintf(os.Stderr, "gin: build stopped: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("gin: build complete")
}

func parseOptions() *Options {
	opts := &Options{}

	flag.StringVar(&opts.inputFile, "f", "build.ninja", "specify input build file")
	flag.StringVar(&opts.workingDir, "C", "", "change to directory before doing anything")
	flag.IntVar(&opts.parallelism, "j", runtime.NumCPU(), "run N jobs in parallel")
	flag.IntVar(&opts.keepGoing, "k", 1, "keep going until N jobs fail")
	flag.BoolVar(&opts.dryRun, "n", false, "dry run (don't run commands)")
	
	// Check if 'v' flag already exists (might be defined by glog)
	if flag.Lookup("v") == nil {
		flag.BoolVar(&opts.verbose, "v", false, "show all command lines")
	} else {
		// If it exists, try to read its value
		if vFlag := flag.Lookup("v"); vFlag != nil {
			if vVal, ok := vFlag.Value.(flag.Getter); ok {
				if v, ok := vVal.Get().(bool); ok {
					opts.verbose = v
				}
			}
		}
	}
	
	flag.BoolVar(&opts.showVersion, "version", false, "print ninja version")
	flag.BoolVar(&opts.showHelp, "h", false, "show help")
	flag.StringVar(&opts.debugMode, "d", "", "enable debugging (use -d list for options)")
	flag.StringVar(&opts.toolMode, "t", "", "run a tool (use -t list for options)")
	flag.BoolVar(&opts.phonyCycleShouldErr, "w", false, "phony cycles are errors")
	flag.StringVar(&opts.remoteEndpoint, "remote", "", "remote execution endpoint (e.g., grpc://localhost:8980)")
	flag.StringVar(&opts.remoteInstance, "remote-instance", "", "remote execution instance name")

	// Custom usage message
	flag.Usage = showHelp

	flag.Parse()

	// Remaining arguments are targets
	opts.targets = flag.Args()

	return opts
}

func showHelp() {
	fmt.Printf(`usage: gin [options] [targets...]

options:
  -C DIR      change to DIR before doing anything
  -f FILE     specify input build file [default=build.ninja]
  
  -j N        run N jobs in parallel [default=%d]
  -k N        keep going until N jobs fail [default=1]
  -l N        do not start new jobs if load average is greater than N
  -n          dry run (don't run commands)
  
  -v          show all command lines
  -d MODE     enable debugging (use -d list for options)
  -t TOOL     run a tool (use -t list for options)
  -w FLAG     adjust warnings (use -w list for options)
  
  -remote ENDPOINT      remote execution endpoint
                        grpc://host[:port]  - insecure (default port 80)
                        grpcs://host[:port] - secure TLS (default port 443)
                        host[:port]         - defaults to grpcs://
  -remote-instance NAME remote execution instance name
  
  -version    print gin version
  -h          show this help

`, runtime.NumCPU())
}

func runTool(tool string, args []string) {
	switch tool {
	case "list":
		fmt.Println("gin subtools:")
		fmt.Println("  browse    browse dependency graph in a web browser")
		fmt.Println("  clean     clean built files")
		fmt.Println("  commands  list all commands")
		fmt.Println("  deps      show dependencies")
		fmt.Println("  graph     output build graph in graphviz format")
		fmt.Println("  query     show build info about a target")
		fmt.Println("  targets   list targets")
		fmt.Println("  compdb    generate compilation database")
		fmt.Println("  recompact recompact build log")
		fmt.Println("  restat    update file timestamps in build log")

	case "clean":
		runClean(args)
		
	case "graph":
		runGraphTool(args)
		
	case "query":
		runQueryTool(args)
		
	case "targets":
		runTargetsTool(args)
		
	case "commands":
		runCommandsTool(args)
		
	case "deps":
		fmt.Println("gin: deps tool not yet implemented")
		
	case "browse":
		fmt.Println("gin: browse tool not yet implemented")
		
	case "compdb":
		runCompDBTool(args)
		
	case "recompact":
		runRecompactTool(args)
		
	case "restat":
		runRestatTool(args)
		
	default:
		fmt.Fprintf(os.Stderr, "gin: unknown tool '%s'\n", tool)
		os.Exit(1)
	}
}

func runClean(args []string) {
	// Find the build file from the parent args
	inputFile := "build.ninja"
	cleanArgs := args
	for i := 0; i < len(args); i++ {
		if args[i] == "-f" && i+1 < len(args) {
			inputFile = args[i+1]
			// Remove -f and filename from clean args
			cleanArgs = append(args[:i], args[i+2:]...)
			break
		}
	}
	
	// Parse options
	cleanFlags := flag.NewFlagSet("clean", flag.ExitOnError)
	verbose := cleanFlags.Bool("v", false, "verbose output")
	dryRun := cleanFlags.Bool("n", false, "dry run")
	generator := cleanFlags.Bool("g", false, "clean generator files")
	rules := cleanFlags.Bool("r", false, "clean rule outputs")
	
	cleanFlags.Parse(cleanArgs)
	
	// Load build file
	s := state.New()
	p := parser.New(s)
	
	if err := p.ParseFile(inputFile); err != nil {
		fmt.Fprintf(os.Stderr, "gin: error: %v\n", err)
		os.Exit(1)
	}
	
	// Create cleaner
	config := &clean.Config{
		Verbose:   *verbose,
		DryRun:    *dryRun,
		Generator: *generator,
		Rules:     *rules,
	}
	cleaner := clean.New(s, config)
	
	// Run clean
	targets := cleanFlags.Args()
	var cleaned int
	var err error
	
	if len(targets) > 0 {
		cleaned, err = cleaner.CleanTargets(targets)
	} else {
		cleaned, err = cleaner.CleanAll()
	}
	
	if err != nil {
		fmt.Fprintf(os.Stderr, "gin: clean failed: %v\n", err)
		os.Exit(1)
	}
	
	fmt.Printf("Cleaned %d files.\n", cleaned)
}

func runGraphTool(args []string) {
	// Find the build file from args
	inputFile := "build.ninja"
	toolArgs := args
	for i := 0; i < len(args); i++ {
		if args[i] == "-f" && i+1 < len(args) {
			inputFile = args[i+1]
			// Remove -f and filename from tool args
			toolArgs = append(args[:i], args[i+2:]...)
			break
		}
	}
	
	// Load build file
	s := state.New()
	p := parser.New(s)
	
	if err := p.ParseFile(inputFile); err != nil {
		fmt.Fprintf(os.Stderr, "gin: error: %v\n", err)
		os.Exit(1)
	}
	
	// Run tool
	tool := tools.NewGraphTool(s)
	if err := tool.Run(toolArgs); err != nil {
		fmt.Fprintf(os.Stderr, "gin: error: %v\n", err)
		os.Exit(1)
	}
}

func runQueryTool(args []string) {
	// Find the build file from args
	inputFile := "build.ninja"
	toolArgs := args
	for i := 0; i < len(args); i++ {
		if args[i] == "-f" && i+1 < len(args) {
			inputFile = args[i+1]
			// Remove -f and filename from tool args
			toolArgs = append(args[:i], args[i+2:]...)
			break
		}
	}
	
	// Load build file
	s := state.New()
	p := parser.New(s)
	
	if err := p.ParseFile(inputFile); err != nil {
		fmt.Fprintf(os.Stderr, "gin: error: %v\n", err)
		os.Exit(1)
	}
	
	// Run tool
	tool := tools.NewQueryTool(s)
	if err := tool.Run(toolArgs); err != nil {
		fmt.Fprintf(os.Stderr, "gin: error: %v\n", err)
		os.Exit(1)
	}
}

func runTargetsTool(args []string) {
	// Find the build file from args
	inputFile := "build.ninja"
	toolArgs := args
	for i := 0; i < len(args); i++ {
		if args[i] == "-f" && i+1 < len(args) {
			inputFile = args[i+1]
			// Remove -f and filename from tool args
			toolArgs = append(args[:i], args[i+2:]...)
			break
		}
	}
	
	// Load build file
	s := state.New()
	p := parser.New(s)
	
	if err := p.ParseFile(inputFile); err != nil {
		fmt.Fprintf(os.Stderr, "gin: error: %v\n", err)
		os.Exit(1)
	}
	
	// Run tool
	tool := tools.NewTargetsTool(s)
	if err := tool.Run(toolArgs); err != nil {
		fmt.Fprintf(os.Stderr, "gin: error: %v\n", err)
		os.Exit(1)
	}
}

func runCommandsTool(args []string) {
	// Find the build file from args
	inputFile := "build.ninja"
	toolArgs := args
	for i := 0; i < len(args); i++ {
		if args[i] == "-f" && i+1 < len(args) {
			inputFile = args[i+1]
			// Remove -f and filename from tool args
			toolArgs = append(args[:i], args[i+2:]...)
			break
		}
	}
	
	// Load build file
	s := state.New()
	p := parser.New(s)
	
	if err := p.ParseFile(inputFile); err != nil {
		fmt.Fprintf(os.Stderr, "gin: error: %v\n", err)
		os.Exit(1)
	}
	
	// Run tool
	tool := tools.NewCommandsTool(s)
	if err := tool.Run(toolArgs); err != nil {
		fmt.Fprintf(os.Stderr, "gin: error: %v\n", err)
		os.Exit(1)
	}
}

func runCompDBTool(args []string) {
	// Find the build file from args
	inputFile := "build.ninja"
	toolArgs := args
	for i := 0; i < len(args); i++ {
		if args[i] == "-f" && i+1 < len(args) {
			inputFile = args[i+1]
			// Remove -f and filename from tool args
			toolArgs = append(args[:i], args[i+2:]...)
			break
		}
	}
	
	// Load build file
	s := state.New()
	p := parser.New(s)
	
	if err := p.ParseFile(inputFile); err != nil {
		fmt.Fprintf(os.Stderr, "gin: error: %v\n", err)
		os.Exit(1)
	}
	
	// Run tool
	tool := tools.NewCompDBTool(s)
	if err := tool.Run(toolArgs); err != nil {
		fmt.Fprintf(os.Stderr, "gin: error: %v\n", err)
		os.Exit(1)
	}
}

func runRecompactTool(args []string) {
	tool := tools.NewRecompactTool()
	if err := tool.Run(args); err != nil {
		fmt.Fprintf(os.Stderr, "gin: error: %v\n", err)
		os.Exit(1)
	}
}

func runRestatTool(args []string) {
	tool := tools.NewRestatTool()
	if err := tool.Run(args); err != nil {
		fmt.Fprintf(os.Stderr, "gin: error: %v\n", err)
		os.Exit(1)
	}
}