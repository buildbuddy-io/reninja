package main

import (
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/buildbuddy-io/gin/internal/build"
	"github.com/buildbuddy-io/gin/internal/build_config"
	"github.com/buildbuddy-io/gin/internal/build_log"
	"github.com/buildbuddy-io/gin/internal/deps_log"
	"github.com/buildbuddy-io/gin/internal/disk"
	"github.com/buildbuddy-io/gin/internal/exit_status"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/jobserver"
	"github.com/buildbuddy-io/gin/internal/manifest_parser"
	"github.com/buildbuddy-io/gin/internal/metrics"
	"github.com/buildbuddy-io/gin/internal/state"
	"github.com/buildbuddy-io/gin/internal/status"
	"github.com/buildbuddy-io/gin/internal/util"
	"github.com/buildbuddy-io/gin/internal/version"
)

var (
	help      bool
	verbose   bool
	debugging StringList
	warnings  StringList

	printVersion    = flag.Bool("version", false, fmt.Sprintf("print ninja version (\"%s\")", version.NinjaVersion))
	quiet           = flag.Bool("quiet", false, "don't show progress status, just command output")
	workingDir      = flag.String("C", "", "change to DIR before doing anything else")
	buildFile       = flag.String("f", "", "specify input build file [default=build.ninja]")
	jobs            = flag.Int("j", -1, "run N jobs in parallel (0 means infinity) [default=%d on this system]")
	allowedFailures = flag.Int("k", 1, "keep going until N jobs fail (0 means infinity) [default=1]")
	maxLoad         = flag.Float64("l", -1, "do not start new jobs if the load average is greater than N")
	dryRun          = flag.Bool("n", false, "dry run (don't run commands but act like they succeeded)")
	tool            = flag.String("t", "", "run a subtool (use '-t list' to list subtools)")
)

// StringList implements a flag.Value that accepts an sequence of values as a CSV.
type StringList []string

// Set implements part of the flag.Getter interface and will append new values to the flag.
func (f *StringList) Set(s string) error {
	*f = append(*f, strings.Split(s, ",")...)
	return nil
}

// String implements part of the flag.Getter interface and returns a string-ish value for the flag.
func (f *StringList) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(*f, ",")
}

// Get implements flag.Getter and returns a slice of string values.
func (f *StringList) Get() any {
	if f == nil {
		return []string(nil)
	}
	return *f
}

func registerFlags() {
	flag.BoolVar(&help, "help", false, "show usage and exit")
	flag.BoolVar(&help, "h", false, "show usage and exit")

	flag.BoolVar(&verbose, "verbose", false, "show all command lines while building")
	flag.BoolVar(&verbose, "v", false, "show all command lines while building")

	flag.Var(&debugging, "d", "enable debugging (use '-d list' to list modes)")
	flag.Var(&warnings, "w", "adjust warnings (use '-w list' to list warnings)")

}

// Options represents command-line options
type Options struct {
	InputFile           string
	WorkingDir          string
	Tool                *Tool
	PhonyCycleShouldErr bool
}

type toolRunTime int

const (
	runAfterFlags toolRunTime = iota
	runAfterLoad
	runAfterLogs
)

type ToolFunc func(*Options, []string) int

type Tool struct {
	name     string
	desc     string
	when     toolRunTime
	toolFunc ToolFunc
}

func GuessParallelism() int {
	processors := runtime.NumCPU()
	switch processors {
	case 0:
		fallthrough
	case 1:
		return 2
	case 2:
		return 3
	default:
		return processors + 2
	}
}

type DeferGuessParallelism struct {
	needGuess bool
	config    *build_config.Config
}

func NewDeferGuessParallelism(config *build_config.Config) *DeferGuessParallelism {
	return &DeferGuessParallelism{
		needGuess: true,
		config:    config,
	}
}
func (p *DeferGuessParallelism) Refresh() {
	if p.needGuess {
		p.needGuess = true
		p.config.Parallelism = GuessParallelism()
	}
}

var _ build_log.BuildLogUser = &NinjaMain{}

type NinjaMain struct {
	// Command line used to run Ninja.
	ninjaCommand string

	// Build configuration set from flags (e.g. parallelism).
	config *build_config.Config

	// Loaded state (rules, nodes).
	state *state.State

	// Functions for accessing the disk.
	diskInterface disk.Interface

	// The build directory, used for storing the build log etc.
	buildDir string

	buildLog *build_log.BuildLog
	depsLog  *deps_log.DepsLog

	startTimeMillis int64
}

func NewNinjaMain(ninjaCommand string, config *build_config.Config) *NinjaMain {
	return &NinjaMain{
		ninjaCommand:    ninjaCommand,
		config:          config,
		state:           state.New(),
		diskInterface:   disk.NewRealDiskInterface(),
		buildLog:        build_log.NewBuildLog(),
		depsLog:         deps_log.NewDepsLog(),
		startTimeMillis: metrics.GetTimeMillis(),
	}
}

func (m *NinjaMain) IsPathDead(s string) bool {
	n := m.state.LookupNode(s)
	if n != nil && n.InEdge() != nil {
		return false
	}
	// Just checking n isn't enough: If an old output is both in the build log
	// and in the deps log, it will have a Node object in state_.  (It will also
	// have an in edge if one of its inputs is another output that's in the deps
	// log, but having a deps edge product an output that's input to another deps
	// edge is rare, and the first recompaction will delete all old outputs from
	// the deps log, and then a second recompaction will clear the build log,
	// which seems good enough for this corner case.)
	// Do keep entries around for files which still exist on disk, for
	// generators that want to use this information.
	mtime, err := m.diskInterface.Stat(s)
	if err != nil {
		util.Error(err.Error()) // Log and ignore Stat() errors.
	}
	return mtime == 0
}

func (m *NinjaMain) DumpMetrics() {
	metrics.DefaultMetrics.Report()

	fmt.Printf("\n")
	count := len(m.state.Paths())
	minBuckets := math.Ceil(float64(count) / 6.5)
	buckets := 1
	for float64(buckets) < minBuckets {
		buckets *= 2
	}
	fmt.Printf("path->node hash load %.2f (%d entries / %d buckets)\n", float64(count)/float64(buckets), count, buckets)
}

func (m *NinjaMain) EnsureBuildDirExists() bool {
	m.buildDir = m.state.Bindings().LookupVariable("builddir")
	if m.buildDir != "" && !m.config.DryRun {
		if err := m.diskInterface.MakeDirs(m.buildDir + "/."); err != nil {
			util.Errorf("creating build directory %s: %s", m.buildDir, err)
			return false
		}
	}
	return true
}

func (m *NinjaMain) SetupJobserverClient(status status.Status) jobserver.Client {
	// If dry-run or explicit job count, don't even look at MAKEFLAGS
	if m.config.DisableJobserverClient {
		return nil
	}

	makeFlags := os.Getenv("MAKEFLAGS")
	if makeFlags == "" {
		// MAKEFLAGS is not defined.
		return nil
	}

	js := &jobserver.Jobserver{}
	jobserverConfig, err := js.ParseNativeMakeFlagsValue(makeFlags)
	if err != nil {
		// MAKEFLAGS is defined but could not be parsed correctly.
		if m.config.Verbosity > build_config.Quiet {
			status.Warning("Ignoring jobserver: %s [%s]", err, makeFlags)
		}
		return nil
	}

	if !jobserverConfig.HasMode() {
		// MAKEFLAGS is defined, but does not describe a jobserver mode.
		return nil
	}

	if m.config.Verbosity > build_config.NoStatusUpdate {
		status.Info("Jobserver mode detected: %s", makeFlags)
	}

	client := jobserver.NewClient()
	if err := client.Create(jobserverConfig); err != nil {
		// Jobserver client initialization failed !?
		if m.config.Verbosity > build_config.Quiet {
			status.Error("Could not initialize jobserver: %s", err)
		}
		return nil
	}
	return client
}

func (m *NinjaMain) RunBuild(positionalArgs []string, status status.Status) exit_status.ExitStatusType {
	targets, err := m.CollectTargetsFromArgs(positionalArgs)
	if err != nil {
		status.Error("%s", err)
		return exit_status.ExitFailure
	}

	// m.diskInterface.AllowStatCache(gExperimentalStatcache)

	// Detect jobserver context and inject Jobserver::Client into the builder
	// if needed.
	jobserverClient := m.SetupJobserverClient(status)

	builder := build.NewBuilder(m.state, m.config, m.buildLog, m.depsLog, m.diskInterface, status, m.startTimeMillis)
	if jobserverClient != nil {
		builder.SetJobserverClient(jobserverClient)
	}

	for _, target := range targets {
		_, err := builder.AddTarget(target)
		if err != nil {
			status.Error("%s", err)
			return exit_status.ExitFailure
		} else {
			// Added a target that is already up-to-date; not really
			// an error.
		}
	}

	// Make sure restat rules do not see stale timestamps.
	// m.diskInterface.AllowStatCache(false)

	if builder.AlreadyUpToDate() {
		if m.config.Verbosity != build_config.NoStatusUpdate {
			status.Info("no work to do.")
		}
		return exit_status.ExitSuccess
	}

	exitCode, err := builder.Build()
	if err != nil || exitCode != exit_status.ExitSuccess {
		status.Info("build stopped: %s.", err)
		if strings.Contains(err.Error(), "interrupted by user") {
			return exit_status.ExitInterrupted
		}
	}

	return exitCode
}

func (m *NinjaMain) CollectTarget(cpath string) (*graph.Node, error) {
	path := cpath
	if path == "" {
		return nil, fmt.Errorf("empty path")
	}

	path, _ = util.CanonicalizePath(path)

	// Special syntax: "foo.cc^" means "the first output of foo.cc".
	firstDependent := false
	if path != "" && path[len(path)-1] == '^' {
		path = path[:len(path)-1]
		firstDependent = true
	}

	node := m.state.LookupNode(path)
	if node != nil {
		if firstDependent {
			if len(node.OutEdges()) == 0 {
				revDeps := m.depsLog.GetFirstReverseDepsNode(node)
				if revDeps == nil {
					return nil, fmt.Errorf("'%s' has no out edge", path)
				}
				node = revDeps
			} else {
				edge := node.OutEdges()[0]
				if len(edge.Outputs()) == 0 {
					edge.Dump("")
					log.Fatalf("edge has no outputs")
				}
				node = edge.Outputs()[0]
			}
		}
		return node, nil
	} else {
		errMsg := fmt.Sprintf("unknown target '%s'", path)
		if path == "clean" {
			errMsg += ", did you mean 'ninja -t clean'?"
		} else if path == "help" {
			errMsg += ", did you mean 'ninja -h'?"
		} else {
			// TODO(tylerw): implement suggestions.
			log.Fatalf("TYLER: implement suggestions")
		}
		return nil, fmt.Errorf(errMsg)
	}
}

func (m *NinjaMain) CollectTargetsFromArgs(args []string) ([]*graph.Node, error) {
	if len(args) == 0 {
		return m.state.DefaultNodes()
	}

	targets := make([]*graph.Node, 0, len(args))
	for _, arg := range args {
		node, err := m.CollectTarget(arg)
		if err != nil {
			return nil, err
		}
		targets = append(targets, node)
	}
	return targets, nil
}

var (
	gExplaining            bool
	gKeepDepfile           bool
	gKeepRsp               bool
	gExperimentalStatcache bool
)

func DebugEnable(names []string) bool {
	for _, name := range names {
		switch name {
		case "list":
			fmt.Printf(`debugging modes:
  stats        print operation counts/timing info
  explain      explain what caused a command to execute
  keepdepfile  don't delete depfiles after they're read by ninja
  keeprsp      don't delete @response files on success
multiple modes can be enabled via -d FOO -d BAR
`)
			return false
		case "stats":
			metrics.Enable()
		case "explain":
			gExplaining = true
		case "keepdepfile":
			gKeepDepfile = true
		case "keeprsp":
			gKeepRsp = true
		case "nostatcache":
			gExperimentalStatcache = false
		default:
			suggestion := util.SpellcheckString(name, "stats", "explain", "keepdepfile", "keeprsp", "nostatcache")
			if suggestion != "" {
				util.Errorf("unknown debug setting '%s', did you mean '%s'?", name, suggestion)
			} else {
				util.Errorf("unknown debug setting '%s'", name)
			}
			return false
		}
	}
	return true
}

func WarningEnable(names []string, options *Options) bool {
	for _, name := range names {
		switch name {
		case "list":
			fmt.Printf(`warning flags:
  phonycycle={err,warn}  phony build statement references itself
`)
			return false
		case "phonycycle=err":
			options.PhonyCycleShouldErr = true
		case "phonycycle=warn":
			options.PhonyCycleShouldErr = false
		case "dupbuild=err", "dupbuild=warn":
			util.Warning("deprecated warning 'dupbuild'")
		case "depfilemulti=err", "depfilemulti=warn":
			util.Warning("deprecated warning 'depfilemulti'")
		default:
			suggestion := util.SpellcheckString(name, "phonycycle=err", "phonycycle=warn")
			if suggestion != "" {
				util.Errorf("unknown warning flag '%s', did you mean '%s'?", name, suggestion)
			} else {
				util.Errorf("unknown warning flag '%s'", name)
			}
			return false
		}
	}
	return true
}

func (m *NinjaMain) ParsePreviousElapsedTimes() {
	for _, edge := range m.state.Edges() {
		for _, out := range edge.Outputs() {
			logEntry := m.buildLog.LookupByOutput(out.Path())
			if logEntry == nil {
				continue // Maybe we'll have log entry for next output of this edge?
			}
			edge.SetPrevElapsedTimeMillis(logEntry.EndTime - logEntry.StartTime)
			break // Onto next edge.
		}
	}
}

func (m *NinjaMain) OpenBuildLog(recompactOnly bool) bool {
	logPath := ".ninja_log"
	if m.buildDir != "" {
		logPath = filepath.Join(m.buildDir, logPath)
	}

	err := m.buildLog.Load(logPath)
	if err != nil {
		util.Errorf("loading build log %s: %s", logPath, err)
		return false
	}

	if recompactOnly {
		// if the file is empty, we're done
		fi, err := os.Stat(logPath)
		if err != nil {
			if os.IsNotExist(err) {
				return true
			}
			util.Errorf("loading build log %s: %s", logPath, err)
			return false
		}
		if fi.Size() == 0 {
			return true
		}
		if err := m.buildLog.Recompact(logPath, m); err != nil {
			util.Errorf("failed recompaction: %s", err)
			return false
		}
	}
	if !m.config.DryRun {
		if err := m.buildLog.OpenForWrite(logPath, m); err != nil {
			util.Errorf("opening build log: %s", err)
			return false
		}
	}
	return true
}

func (m *NinjaMain) OpenDepsLog(recompactOnly bool) bool {
	logPath := ".ninja_deps"
	if m.buildDir != "" {
		logPath = filepath.Join(m.buildDir, logPath)
	}

	err := m.depsLog.Load(logPath, m.state)
	if err != nil {
		util.Errorf("loading deps log %s: %s", logPath, err)
		return false
	}

	if recompactOnly {
		// if the file is empty, we're done
		fi, err := os.Stat(logPath)
		if err != nil {
			if os.IsNotExist(err) {
				return true
			}
			util.Errorf("opening deps log: %s", err)
			return false
		}
		if fi.Size() == 0 {
			return true
		}
		if err := m.depsLog.Recompact(logPath); err != nil {
			util.Errorf("failed recompaction: %s", err)
			return false
		}
	}
	if !m.config.DryRun {
		if err := m.depsLog.OpenForWrite(logPath); err != nil {
			util.Errorf("opening build log: %s", err)
			return false
		}
	}
	return true
}

func (m *NinjaMain) RebuildManifest(inputFile string, status status.Status) (bool, error) {
	inputPath := inputFile
	if inputPath == "" {
		return false, fmt.Errorf("empty path")
	}
	inputPath, _ = util.CanonicalizePath(inputPath)
	node := m.state.LookupNode(inputPath)
	if node == nil {
		return false, nil
	}

	builder := build.NewBuilder(m.state, m.config, m.buildLog, m.depsLog, m.diskInterface, status, m.startTimeMillis)
	added, err := builder.AddTarget(node)
	if err != nil || !added {
		return false, err
	}

	if builder.AlreadyUpToDate() {
		return false, nil // Not an error, but we didn't rebuild.
	}

	exitCode, err := builder.Build()
	if err != nil || exitCode != exit_status.ExitSuccess {
		return false, err
	}

	// The manifest was only rebuilt if it is now dirty (it may have been cleaned
	// by a restat).
	if !node.Dirty() {
		// Reset the state to prevent problems like
		// https://github.com/ninja-build/ninja/issues/874
		m.state.Reset()
		return false, nil
	}

	return true, nil
}
func (m *NinjaMain) ToolGraph(*Options, []string) int                         { return 0 }
func (m *NinjaMain) ToolQuery(*Options, []string) int                         { return 0 }
func (m *NinjaMain) ToolDeps(*Options, []string) int                          { return 0 }
func (m *NinjaMain) ToolMissingDeps(*Options, []string) int                   { return 0 }
func (m *NinjaMain) ToolBrowse(*Options, []string) int                        { return 0 }
func (m *NinjaMain) ToolMSVC(*Options, []string) int                          { return 0 }
func (m *NinjaMain) ToolTargets(*Options, []string) int                       { return 0 }
func (m *NinjaMain) ToolCommands(*Options, []string) int                      { return 0 }
func (m *NinjaMain) ToolInputs(*Options, []string) int                        { return 0 }
func (m *NinjaMain) ToolMultiInputs(*Options, []string) int                   { return 0 }
func (m *NinjaMain) ToolClean(*Options, []string) int                         { return 0 }
func (m *NinjaMain) ToolCleanDead(*Options, []string) int                     { return 0 }
func (m *NinjaMain) ToolCompilationDatabase(*Options, []string) int           { return 0 }
func (m *NinjaMain) ToolCompilationDatabaseForTargets(*Options, []string) int { return 0 }
func (m *NinjaMain) ToolRecompact(*Options, []string) int                     { return 0 }
func (m *NinjaMain) ToolRestat(*Options, []string) int                        { return 0 }
func (m *NinjaMain) ToolUrtle(*Options, []string) int                         { return 0 }
func (m *NinjaMain) ToolRules(*Options, []string) int                         { return 0 }
func (m *NinjaMain) ToolWinCodePage(*Options, []string) int                   { return 0 }

func (m *NinjaMain) ChooseTool(toolName string) *Tool {
	tools := []*Tool{
		{"browse", "browse dependency graph in a web browser", runAfterLoad, m.ToolBrowse},
	}
	_ = tools
	return nil
}

// Parse argv for command-line options.
// Returns an exit code, or -1 if Ninja should continue.
func (m *NinjaMain) ReadFlags(args []string, options *Options, config *build_config.Config) int {
	deferGuessParallelism := NewDeferGuessParallelism(config)
	defer deferGuessParallelism.Refresh()

	if len(debugging) > 0 {
		if !DebugEnable(debugging) {
			return 1
		}
	}
	if *buildFile != "" {
		options.InputFile = *buildFile
	}
	if *jobs != -1 {
		if *jobs > 0 && *jobs < math.MaxInt32 {
			config.Parallelism = *jobs
		} else {
			config.Parallelism = math.MaxInt32
		}
		config.DisableJobserverClient = true
		deferGuessParallelism.needGuess = false
	}

	// We want to go until N jobs fail, which means we should allow
	// N failures and then stop.  For N <= 0, INT_MAX is close enough
	// to infinite for most sane builds.
	if *allowedFailures > 0 && *allowedFailures < math.MaxInt32 {
		config.FailuresAllowed = *allowedFailures
	} else {
		config.FailuresAllowed = int(math.MaxInt32)
	}

	config.MaxLoadAverage = *maxLoad

	if *dryRun {
		config.DryRun = true
		config.DisableJobserverClient = true
	}

	if *tool != "" {
		options.Tool = m.ChooseTool(*tool)
		if options.Tool == nil {
			return 0
		}
	}

	if verbose {
		config.Verbosity = build_config.Verbose
	}
	if *quiet {
		config.Verbosity = build_config.NoStatusUpdate
	}
	if len(warnings) > 0 {
		if !WarningEnable(warnings, options) {
			return 1
		}
	}
	if *workingDir != "" {
		options.WorkingDir = *workingDir
	}

	if *printVersion {
		fmt.Printf("%s\n", version.NinjaVersion)
	}
	if help {
		deferGuessParallelism.Refresh()
		Usage(config)
		return 1
	}
	return -1
}

func Usage(config *build_config.Config) {
	fmt.Fprintf(os.Stderr, `usage: ninja [options] [targets...]

if targets are unspecified, builds the 'default' target (see manual).

options:
  --version      print ninja version ("%s")
  -v, --verbose  show all command lines while building
  --quiet        don't show progress status, just command output

  -C DIR   change to DIR before doing anything else
  -f FILE  specify input build file [default=build.ninja]

  -j N     run N jobs in parallel (0 means infinity) [default=%d on this system]
  -k N     keep going until N jobs fail (0 means infinity) [default=1]
  -l N     do not start new jobs if the load average is greater than N
  -n       dry run (don't run commands but act like they succeeded)

  -d MODE  enable debugging (use '-d list' to list modes)
  -t TOOL  run a subtool (use '-t list' to list subtools)
    terminates toplevel options; further flags are passed to the tool
  -w FLAG  adjust warnings (use '-w list' to list warnings)
`, version.NinjaVersion, config.Parallelism)
}

func main() {
	config := build_config.Create()
	options := &Options{}
	options.InputFile = "build.ninja"

	registerFlags()
	flag.Usage = func() { Usage(config) }
	flag.Parse()

	ninjaCommand := os.Args[0]
	positionalArgs := flag.Args()

	ninja := NewNinjaMain(ninjaCommand, config)
	exitCode := ninja.ReadFlags(positionalArgs, options, config)
	if exitCode >= 0 {
		os.Exit(exitCode)
	}
	status := status.NewPrinter(config)

	if options.WorkingDir != "" {
		// The formatting of this string, complete with funny quotes, is
		// so Emacs can properly identify that the cwd has changed for
		// subsequent commands.
		// Don't print this if a tool is being used, so that tool output
		// can be piped into a file without this string showing up.
		if options.Tool == nil && config.Verbosity != build_config.NoStatusUpdate {
			status.Info("Entering directory `&s'", options.WorkingDir)
		}
		if err := os.Chdir(options.WorkingDir); err != nil {
			log.Fatalf("chdir to '%s' - %s", options.WorkingDir, err)
		}
	}

	if options.Tool != nil && options.Tool.when == runAfterFlags {
		// None of the RUN_AFTER_FLAGS actually use a NinjaMain, but it's needed
		// by other tools.
		fmt.Printf("here\n")
		os.Exit(options.Tool.toolFunc(options, positionalArgs))
	}

	// Limit number of rebuilds, to prevent infinite loops.
	cycleLimit := 100
	for cycle := 1; cycle <= cycleLimit; cycle++ {
		ninja = NewNinjaMain(ninjaCommand, config)

		parserOpts := manifest_parser.ManifestParserOptions{}
		if options.PhonyCycleShouldErr {
			parserOpts.PhonyCycleAction = manifest_parser.PhonyCycleActionError
		}
		parser := manifest_parser.New(ninja.state, ninja.diskInterface, parserOpts)
		if err := parser.ParseFile(options.InputFile); err != nil {
			status.Error("%s", err.Error())
			os.Exit(1)
		}

		fmt.Printf("options: %+v", options)
		if options.Tool != nil && options.Tool.when == runAfterLoad {
			os.Exit(options.Tool.toolFunc(options, positionalArgs))
		}

		if !ninja.EnsureBuildDirExists() {
			os.Exit(1)
		}

		if !ninja.OpenBuildLog(false /*=recompactOnly*/) || !ninja.OpenDepsLog(false /*=recompactOnly*/) {
			os.Exit(1)
		}

		if options.Tool != nil && options.Tool.when == runAfterLogs {
			os.Exit(options.Tool.toolFunc(options, positionalArgs))
		}

		// Attempt to rebuild the manifest before building anything else
		ok, err := ninja.RebuildManifest(options.InputFile, status)
		if ok {
			// In dry_run mode the regeneration will succeed without changing the
			// manifest forever. Better to return immediately.
			if config.DryRun {
				os.Exit(0)
			}
			// Start the build over with the new manifest.
			continue
		} else if err != nil {
			status.Error("rebuilding '%s': %s", options.InputFile, err)
			os.Exit(1)
		}

		ninja.ParsePreviousElapsedTimes()

		result := ninja.RunBuild(positionalArgs, status)
		if metrics.DefaultMetrics != nil {
			ninja.DumpMetrics()
		}
		os.Exit(int(result))
	}

	status.Error("manifest '%s' still dirty after %d tries, perhaps system time is not set", options.InputFile, cycleLimit)
	os.Exit(1)
}
