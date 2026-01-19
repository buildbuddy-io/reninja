package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/buildbuddy-io/reninja/internal/browse"
	"github.com/buildbuddy-io/reninja/internal/build"
	"github.com/buildbuddy-io/reninja/internal/build_config"
	"github.com/buildbuddy-io/reninja/internal/build_log"
	"github.com/buildbuddy-io/reninja/internal/clean"
	"github.com/buildbuddy-io/reninja/internal/command_collector"
	"github.com/buildbuddy-io/reninja/internal/debug_flags"
	"github.com/buildbuddy-io/reninja/internal/deps_log"
	"github.com/buildbuddy-io/reninja/internal/disk"
	"github.com/buildbuddy-io/reninja/internal/dyndep"
	"github.com/buildbuddy-io/reninja/internal/dyndep_parser"
	"github.com/buildbuddy-io/reninja/internal/exit_status"
	"github.com/buildbuddy-io/reninja/internal/graph"
	"github.com/buildbuddy-io/reninja/internal/graphviz"
	"github.com/buildbuddy-io/reninja/internal/jobserver"
	"github.com/buildbuddy-io/reninja/internal/manifest_parser"
	"github.com/buildbuddy-io/reninja/internal/metrics"
	"github.com/buildbuddy-io/reninja/internal/missing_deps"
	"github.com/buildbuddy-io/reninja/internal/ninjarc"
	"github.com/buildbuddy-io/reninja/internal/state"
	"github.com/buildbuddy-io/reninja/internal/status"
	"github.com/buildbuddy-io/reninja/internal/subprocess"
	"github.com/buildbuddy-io/reninja/internal/util"
	"github.com/buildbuddy-io/reninja/internal/version"
)

var (
	help      bool
	verbose   bool
	debugging util.StringList
	warnings  util.StringList

	printVersion    = flag.Bool("version", false, fmt.Sprintf("print ninja version (\"%s\")", version.NinjaVersion))
	quiet           = flag.Bool("quiet", false, "don't show progress status, just command output")
	workingDir      = flag.String("C", "", "change to DIR before doing anything else")
	buildFile       = flag.String("f", "", "specify input build file [default=build.ninja]")
	jobs            = flag.Int("j", -1, "run N jobs in parallel (0 means infinity) [default=%d on this system]")
	allowedFailures = flag.Int("k", 1, "keep going until N jobs fail (0 means infinity) [default=1]")
	maxLoad         = flag.Float64("l", -1, "do not start new jobs if the load average is greater than N")
	dryRun          = flag.Bool("n", false, "dry run (don't run commands but act like they succeeded)")
	tool            = flag.String("t", "", "run a subtool (use '-t list' to list subtools)")
	configFlag      = flag.String("config", "", "ninjarc configuration to apply")
)

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

// same as ToolFunc, but accepts an instance as arg0.
type dispatchFunc func(any, *Options, []string) int

func lookupDispatchFunc(methodName string) dispatchFunc {
	return func(i any, opts *Options, argv []string) int {
		instanceValue := reflect.ValueOf(i)
		method := instanceValue.MethodByName(methodName)
		if !method.IsValid() {
			panic(fmt.Sprintf("BUG: %s not found", methodName))
		}
		args := []reflect.Value{
			reflect.ValueOf(opts),
			reflect.ValueOf(argv),
		}
		rvals := method.Call(args)
		if len(rvals) != 1 {
			panic("method did not return single int value")
		}
		rval := rvals[0]
		intValue, ok := rval.Interface().(int)
		if !ok {
			panic("method did not return single int value")
		}
		return intValue
	}
}

type Tool struct {
	Name     string
	Desc     string
	When     toolRunTime
	ToolFunc dispatchFunc
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
		startTimeMillis: time.Now().UnixMilli(),
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
					util.Fatal("edge has no outputs")
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
			suggestion := m.state.SpellcheckNode(path)
			if suggestion != nil {
				errMsg += fmt.Sprintf(", did you mean '%s'?", suggestion.Path())
			}
		}
		return nil, fmt.Errorf("%s", errMsg)
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

var gExperimentalStatcache bool

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
			debug_flags.Explaining = true
		case "keepdepfile":
			debug_flags.KeepDepfile = true
		case "keeprsp":
			debug_flags.KeepRsp = true
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
	if errors.Is(err, build_log.ErrBuildLogVersionOld) {
		// Print warning but don't fail - version is too old, we just start fresh
		util.Warningf("%s", err)
	} else if err != nil {
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
func (m *NinjaMain) ToolGraph(opts *Options, args []string) int {
	nodes, err := m.CollectTargetsFromArgs(args)
	if err != nil {
		util.Errorf("%s", err)
		return 1
	}

	graph := graphviz.New(m.state, m.diskInterface)
	graph.Start()
	for _, n := range nodes {
		graph.AddTarget(n)
	}
	graph.Finish()

	return 0
}

func (m *NinjaMain) ToolQuery(opts *Options, args []string) int {
	if len(args) == 0 {
		util.Error("expected a target to query")
		return 1
	}

	dyndepLoader := dyndep.NewDyndepLoader(m.state, m.diskInterface)
	for _, arg := range args {
		node, err := m.CollectTarget(arg)
		if err != nil {
			util.Errorf("%s", err)
			return 1
		}
		fmt.Printf("%s:\n", node.Path())
		edge := node.InEdge()
		if edge != nil {
			if edge.Dyndep() != nil && edge.Dyndep().DyndepPending() {
				ddf := dyndep_parser.NewDyndepFile()
				if err := dyndepLoader.LoadDyndeps(edge.Dyndep(), ddf); err != nil {
					util.Warningf("%s\n", err)
				}
			}
			fmt.Printf("  input: %s\n", edge.Rule().Name())
			for in := 0; in < len(edge.Inputs()); in++ {
				label := ""
				if edge.IsImplicit(in) {
					label = "| "
				} else if edge.IsOrderOnly(in) {
					label = "|| "
				}
				fmt.Printf("    %s%s\n", label, edge.Inputs()[in].Path())
			}
			if len(edge.Validations()) > 0 {
				fmt.Printf("  validations:\n")
				for _, validation := range edge.Validations() {
					fmt.Printf("    %s\n", validation.Path())
				}
			}
		}
		fmt.Printf("  outputs:\n")
		for _, edge := range node.OutEdges() {
			for _, out := range edge.Outputs() {
				fmt.Printf("    %s\n", out.Path())
			}
		}
		validationEdges := node.ValidationOutEdges()
		if len(validationEdges) > 0 {
			fmt.Printf("  validation for:\n")
			for _, edge := range validationEdges {
				for _, out := range edge.Outputs() {
					fmt.Printf("    %s\n", out.Path())
				}
			}
		}
	}
	return 0
}

func (m *NinjaMain) ToolDeps(opts *Options, args []string) int {
	nodes := make([]*graph.Node, 0)
	if len(args) == 0 {
		for _, ni := range m.depsLog.Nodes() {
			if m.depsLog.IsDepsEntryLiveFor(ni) {
				nodes = append(nodes, ni)
			}
		}
	} else {
		targets, err := m.CollectTargetsFromArgs(args)
		if err != nil {
			util.Errorf("%s", err)
			return 1
		}
		nodes = targets
	}

	for _, it := range nodes {
		deps := m.depsLog.GetDeps(it)
		if deps == nil {
			fmt.Printf("%s: deps not found\n", it.Path())
			continue
		}

		mtime, err := m.diskInterface.Stat(it.Path())
		if mtime == -1 {
			util.Errorf("%s", err) // Log and ignore Stat() errors;
		}
		val := "VALID"
		if mtime == 0 || mtime > deps.Mtime {
			val = "STALE"
		}
		fmt.Printf("%s: #deps %d, deps mtime %d (%s)\n", it.Path(), len(deps.Nodes), deps.Mtime, val)
		for i := 0; i < len(deps.Nodes); i++ {
			fmt.Printf("    %s\n", deps.Nodes[i].Path())
		}
		fmt.Printf("\n")
	}
	return 0
}

func (m *NinjaMain) ToolMissingDeps(opts *Options, args []string) int {
	nodes, err := m.CollectTargetsFromArgs(args)
	if err != nil {
		util.Errorf("%s", err)
		return 1
	}
	printer := &missing_deps.MissingDependencyPrinter{}
	scanner := missing_deps.NewScanner(printer, m.depsLog, m.state, m.diskInterface)
	for _, node := range nodes {
		scanner.ProcessNode(node)
	}
	scanner.PrintStats()
	if scanner.HadMissingDeps() {
		return 3
	}
	return 0
}

func (m *NinjaMain) ToolBrowse(opts *Options, args []string) int {
	err := browse.RunBrowsePython(m.state, m.ninjaCommand, opts.InputFile, args)
	if err != nil {
		return 1
	}
	return 0
}

func (m *NinjaMain) ToolMSVC(*Options, []string) int {
	util.Error("TBD: windows support")
	return 1
}

func ToolTargetsList(nodes []*graph.Node, depth, indent int) int {
	for _, n := range nodes {
		for range indent {
			fmt.Printf("  ")
		}
		target := n.Path()
		if n.InEdge() != nil {
			fmt.Printf("%s: %s\n", target, n.InEdge().Rule().Name())
			if depth > 1 || depth <= 0 {
				ToolTargetsList(n.InEdge().Inputs(), depth-1, indent+1)
			}
		} else {
			fmt.Printf("%s\n", target)
		}
	}
	return 0
}

func ToolTargetsSourceList(state *state.State) int {
	for _, e := range state.Edges() {
		for _, inps := range e.Inputs() {
			if inps.InEdge() == nil {
				fmt.Printf("%s\n", inps.Path())
			}
		}
	}
	return 0
}

func ToolTargetsListStateRule(state *state.State, ruleName string) int {
	rules := make([]string, 0)
	for _, e := range state.Edges() {
		if e.Rule().Name() == ruleName {
			for _, outNode := range e.Outputs() {
				rules = append(rules, outNode.Path())
			}
		}
	}

	// Print them.
	for _, i := range rules {
		fmt.Printf("%s\n", i)
	}

	return 0
}

func ToolTargetsListState(state *state.State) int {
	for _, e := range state.Edges() {
		for _, outNode := range e.Outputs() {
			fmt.Printf("%s: %s\n", outNode.Path(), e.Rule().Name())
		}
	}

	return 0
}

func (m *NinjaMain) ToolTargets(opts *Options, args []string) int {
	depth := 1
	if len(args) >= 1 {
		mode := args[0]
		if mode == "rule" {
			var rule string
			if len(args) > 1 {
				rule = args[1]
			}
			if rule == "" {
				return ToolTargetsSourceList(m.state)
			} else {
				return ToolTargetsListStateRule(m.state, rule)
			}
		} else if mode == "depth" {
			if len(args) > 1 {
				i, _ := strconv.Atoi(args[1])
				depth = i
			}
		} else if mode == "all" {
			return ToolTargetsListState(m.state)
		} else {
			suggestion := util.SpellcheckString(mode, "rule", "depth", "all")
			if suggestion != "" {
				util.Errorf("unknown tool target mode '%s', did you mean '%s'?", mode, suggestion)
			} else {
				util.Errorf("unknown target tool mode '%s'", mode)
			}
			return 1
		}
	}

	rootNodes, err := m.state.RootNodes()
	if err != nil {
		util.Errorf("%s", err)
		return 1
	}
	return ToolTargetsList(rootNodes, depth, 0)
}

func (m *NinjaMain) ToolCommands(opts *Options, args []string) int {
	toolUsage := func() {
		fmt.Printf(`
usage: ninja -t commands [options] [targets]

options:
  -s     only print the final command to build [target], not the whole chain
`)
	}

	fs := flag.NewFlagSet("ToolCommands", flag.ContinueOnError)
	fs.Usage = toolUsage
	single := fs.Bool("s", false, "only print the final command to build [target], not the whole chain")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	mode := PCMAll
	if *single {
		mode = PCMSingle
	}
	nodes, err := m.CollectTargetsFromArgs(fs.Args())
	if err != nil {
		util.Errorf("%s", err)
		return 1
	}

	seen := make(map[*graph.Edge]struct{}, 0)
	for _, in := range nodes {
		PrintCommands(in.InEdge(), seen, mode)
	}
	return 0
}

func (m *NinjaMain) ToolInputs(opts *Options, args []string) int {
	toolUsage := func() {
		fmt.Printf(`
Usage '-t inputs [options] [targets]

List all inputs used for a set of targets, sorted in dependency order.
Note that by default, results are shell escaped, and sorted alphabetically,
and never include validation target paths.

Options:"
  -h, --help          Print this message.
  -0, --print0            Use \\0, instead of \\n as a line terminator.
  -E, --no-shell-escape   Do not shell escape the result.
  -d, --dependency-order  Sort results by dependency order.
`)
	}
	fs := flag.NewFlagSet("ToolInputs", flag.ContinueOnError)
	fs.Usage = toolUsage

	var zero bool
	fs.BoolVar(&zero, "0", false, "Use \\0, instead of \\n as a line terminator.")
	fs.BoolVar(&zero, "print0", false, "Use \\0, instead of \\n as a line terminator.")

	var noShellEscape bool
	fs.BoolVar(&noShellEscape, "E", false, "Do not shell escape the result.")
	fs.BoolVar(&noShellEscape, "no-shell-escape", false, "Do not shell escape the result.")

	var dependencyOrder bool
	fs.BoolVar(&dependencyOrder, "d", false, "Sort results by dependency order.")
	fs.BoolVar(&dependencyOrder, "dependency-order", false, "Sort results by dependency order.")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	nodes, err := m.CollectTargetsFromArgs(fs.Args())
	if err != nil {
		util.Errorf("%s", err)
		return 1
	}

	collector := graph.NewInputsCollector()
	for _, node := range nodes {
		collector.VisitNode(node)
	}
	shellEscape := !noShellEscape
	inputs := collector.GetInputsAsStrings(shellEscape)
	if !dependencyOrder {
		sort.Strings(inputs)
	}
	if zero {
		for _, input := range inputs {
			os.Stdout.Write(append([]byte(input), 0))
		}
	} else {
		for _, input := range inputs {
			fmt.Printf("%s\n", input)
		}
	}
	return 0
}

func (m *NinjaMain) ToolMultiInputs(opts *Options, args []string) int {
	toolUsage := func() {
		fmt.Printf(`
Usage '-t multi-inputs [options] [targets]

Print one or more sets of inputs required to build targets, sorted in dependency order.
The tool works like inputs tool but with addition of the target for each line.
The output will be a series of lines with the following elements:
<target> <delimiter> <input> <terminator>
Note that a given input may appear for several targets if it is used by more than one targets.
Options:
  -h, --help                   Print this message.
  -d  --delimiter=DELIM        Use DELIM instead of TAB for field delimiter.
  -0, --print0                 Use \\0, instead of \\n as a line terminator.
`)
	}
	fs := flag.NewFlagSet("ToolMultiInputs", flag.ContinueOnError)
	fs.Usage = toolUsage

	var zero bool
	fs.BoolVar(&zero, "0", false, "Use \\0, instead of \\n as a line terminator.")
	fs.BoolVar(&zero, "print0", false, "Use \\0, instead of \\n as a line terminator.")

	var delimiter string
	fs.StringVar(&delimiter, "d", "\t", "Use DELIM instead of TAB for field delimiter.")
	fs.StringVar(&delimiter, "delimiter", "\t", "Use DELIM instead of TAB for field delimiter.")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	var terminator byte = '\n'
	if zero {
		terminator = 0
	}
	nodes, err := m.CollectTargetsFromArgs(fs.Args())
	if err != nil {
		util.Errorf("%s", err)
		return 1
	}

	for _, node := range nodes {
		collector := graph.NewInputsCollector()
		collector.VisitNode(node)
		inputs := collector.GetInputsAsStrings(false)
		for _, input := range inputs {
			fmt.Printf("%s%s%s", node.Path(), delimiter, input)
			os.Stdout.Write([]byte{terminator})
		}
	}
	return 0
}

func (m *NinjaMain) ToolClean(opts *Options, args []string) int {
	toolUsage := func() {
		fmt.Printf(`
usage: ninja -t clean [options] [targets]

options:
  -g     also clean files marked as ninja generator output
  -r     interpret targets as a list of rules to clean instead
`)
	}
	fs := flag.NewFlagSet("ToolClean", flag.ContinueOnError)
	fs.Usage = toolUsage
	generator := fs.Bool("g", false, "also clean files marked as ninja generator output")
	cleanRules := fs.Bool("r", false, "interpret targets as a list of rules to clean instead")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *cleanRules && len(fs.Args()) == 0 {
		util.Error("expected a rule to clean")
		return 1
	}

	cleaner := clean.NewCleaner(m.state, m.config, m.diskInterface)
	if len(fs.Args()) > 0 {
		if *cleanRules {
			return cleaner.CleanRules(fs.Args())
		} else {
			return cleaner.CleanTargets(fs.Args())
		}
	} else {
		return cleaner.CleanAll(*generator)
	}
}

func (m *NinjaMain) ToolCleanDead(opts *Options, args []string) int {
	cleaner := clean.NewCleaner(m.state, m.config, m.diskInterface)
	return cleaner.CleanDead(m.buildLog.Entries())
}

type EvaluateCommandMode int

const (
	EcmNormal EvaluateCommandMode = iota
	EcmExpandRspfile
)

func PrintJSONString(in string) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	err := enc.Encode(in)
	if err == nil {
		s := buf.String()
		fmt.Printf("%s", s[:len(s)-1])
	}
}

func EvaluateCommandWithRspfile(edge *graph.Edge, mode EvaluateCommandMode) string {
	command := edge.EvaluateCommand(false)
	if mode == EcmNormal {
		return command
	}

	rspFile := edge.GetUnescapedRspfile()
	if rspFile == "" {
		return command
	}

	index := strings.Index(command, rspFile)
	if index == 0 || index == -1 || (command[index-1] != '@' && strings.Index(command, "--option-file=") != index-14 && strings.Index(command, "-f ") != index-3) {
		return command
	}

	rspfileContent := edge.GetBinding("rspfile_content")
	newlineIndex := strings.Index(rspfileContent, "\n")
	for newlineIndex != -1 {
		newlineIndex = strings.Index(rspfileContent, "\n")
	}

	commandBytes := []byte(command)

	if command[index-1] == '@' {
		copy(commandBytes[index-1:index+len(rspfileContent)], rspfileContent)
	} else if strings.Index(command, "-f ") == index-3 {
		copy(commandBytes[index-3:index+len(rspfileContent)], rspfileContent)
	} else { // --option-file syntax
		copy(commandBytes[index-14:index+len(rspfileContent)], rspfileContent)
	}

	return string(commandBytes)
}

// PrintCompdbObjectsForEdge prints one JSON object per input of the edge.
func PrintCompdbObjectsForEdge(directory string, edge *graph.Edge, mode EvaluateCommandMode) {
	command := EvaluateCommandWithRspfile(edge, mode)
	first := true
	for _, input := range edge.Inputs() {
		if !first {
			fmt.Printf(",")
		}
		fmt.Printf("\n  {\n    \"directory\": ")
		PrintJSONString(directory)
		fmt.Printf(",\n    \"command\": ")
		PrintJSONString(command)
		fmt.Printf(",\n    \"file\": ")
		PrintJSONString(input.Path())
		fmt.Printf(",\n    \"output\": ")
		PrintJSONString(edge.Outputs()[0].Path())
		fmt.Printf("\n  }")
		first = false
	}
}

func (m *NinjaMain) ToolCompilationDatabase(opts *Options, args []string) int {
	toolUsage := func() {
		fmt.Printf(`
usage: ninja -t compdb [options] [rules]

options:
  -x     expand @rspfile style response file invocations
`)
	}
	fs := flag.NewFlagSet("ToolCompilationDatabase", flag.ContinueOnError)
	fs.Usage = toolUsage
	expand := fs.Bool("x", false, "expand @rspfile style response file invocations")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	evalMode := EcmNormal
	if *expand {
		evalMode = EcmExpandRspfile
	}

	first := true
	directory, err := disk.GetWorkingDirectory()
	if err != nil {
		return 1
	}
	fmt.Printf("[")
	for _, edge := range m.state.Edges() {
		if len(edge.Inputs()) == 0 {
			continue
		}
		if len(args) == 0 {
			if !first {
				fmt.Printf(",")
			}
			PrintCompdbObjectsForEdge(directory, edge, evalMode)
			first = false
		} else {
			for _, arg := range args {
				if edge.Rule().Name() == arg {
					if !first {
						fmt.Printf(",")
					}
					PrintCompdbObjectsForEdge(directory, edge, evalMode)
					first = false
				}
			}
		}
	}
	fmt.Printf("\n]\n")
	return 0
}

func PrintCompdb(directory string, edges []*graph.Edge, mode EvaluateCommandMode) {
	fmt.Printf("[")
	first := true

	for _, edge := range edges {
		if edge.IsPhony() || len(edge.Inputs()) == 0 {
			continue
		}
		if !first {
			fmt.Printf(",")
		}
		PrintCompdbObjectsForEdge(directory, edge, mode)
		first = false
	}
	fmt.Printf("\n]\n")
}

func (m *NinjaMain) ToolCompilationDatabaseForTargets(opts *Options, args []string) int {
	toolUsage := func() {
		fmt.Printf(`usage: ninja -t compdb [-hx] target [targets]

options:
  -h     display this help message
  -x     expand @rspfile style response file invocations
`)
	}
	fs := flag.NewFlagSet("ToolCompilationDatabaseForTargets", flag.ContinueOnError)
	fs.Usage = toolUsage
	expand := fs.Bool("x", false, "expand @rspfile style response file invocations")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if len(fs.Args()) == 0 {
		util.Errorf("compdb-targets expects the name of at least one target")
		toolUsage()
		return 1
	}

	evalMode := EcmNormal
	if *expand {
		evalMode = EcmExpandRspfile
	}
	targets := fs.Args()
	collector := command_collector.New()
	for _, targetArg := range targets {
		node, err := m.CollectTarget(targetArg)
		if err != nil {
			util.Fatalf("%s", err)
			return 1
		}
		if node.InEdge() == nil {
			util.Fatalf("'%s' is not a target (i.e. it is not an output of any `build` statement)", node.Path())
		}
		collector.CollectFrom(node)
	}

	directory, err := disk.GetWorkingDirectory()
	if err != nil {
		return 1
	}
	PrintCompdb(directory, collector.InEdges(), evalMode)
	return 0
}

func (m *NinjaMain) ToolRecompact(*Options, []string) int {
	if !m.EnsureBuildDirExists() {
		return 1
	}

	if !m.OpenBuildLog(false /*=recompactOnly*/) || !m.OpenDepsLog(false /*=recompactOnly*/) {
		return 1
	}
	return 0
}

func (m *NinjaMain) ToolRestat(opts *Options, args []string) int {
	toolUsage := func() {
		fmt.Printf(`
usage: ninja -t restat [outputs]
`)
	}
	fs := flag.NewFlagSet("ToolRestat", flag.ContinueOnError)
	fs.Usage = toolUsage
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if !m.EnsureBuildDirExists() {
		return 1
	}

	logPath := ".ninja_log"
	if m.buildDir != "" {
		logPath = filepath.Join(m.buildDir, logPath)
	}

	// if the file is empty, we're done
	_, err := os.Stat(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return int(exit_status.ExitSuccess)
		}
	}

	err = m.buildLog.Load(logPath)
	if errors.Is(err, build_log.ErrBuildLogVersionOld) {
		util.Warningf("%s", err)
	} else if err != nil {
		util.Errorf("loading build log %s: %s", logPath, err)
		return int(exit_status.ExitFailure)
	}

	err = m.buildLog.Restat(logPath, m.diskInterface, len(args), args)
	if err != nil {
		util.Errorf("failed recompaction: %s", err)
		return int(exit_status.ExitFailure)
	}

	if !m.config.DryRun {
		if err := m.buildLog.OpenForWrite(logPath, m); err != nil {
			util.Errorf("opening build log: %s", err)
			return int(exit_status.ExitFailure)
		}
	}

	return int(exit_status.ExitSuccess)
}

func (m *NinjaMain) ToolUrtle(*Options, []string) int {
	urtle := " 13 ,3;2!2;\n8 ,;<11!;\n5 `'<10!(2`'2!\n11 ,6;, `\\. `\\9 .,c13$ec,.\n6 " +
		",2;11!>; `. ,;!2> .e8$2\".2 \"?7$e.\n <:<8!'` 2.3,.2` ,3!' ;,(?7\";2!2'<" +
		"; `?6$PF ,;,\n2 `'4!8;<!3'`2 3! ;,`'2`2'3!;4!`2.`!;2 3,2 .<!2'`).\n5 3`5" +
		"'2`9 `!2 `4!><3;5! J2$b,`!>;2!:2!`,d?b`!>\n26 `'-;,(<9!> $F3 )3.:!.2 d\"" +
		"2 ) !>\n30 7`2'<3!- \"=-='5 .2 `2-=\",!>\n25 .ze9$er2 .,cd16$bc.'\n22 .e" +
		"14$,26$.\n21 z45$c .\n20 J50$c\n20 14$P\"`?34$b\n20 14$ dbc `2\"?22$?7$c" +
		"\n20 ?18$c.6 4\"8?4\" c8$P\n9 .2,.8 \"20$c.3 ._14 J9$\n .2,2c9$bec,.2 `?" +
		"21$c.3`4%,3%,3 c8$P\"\n22$c2 2\"?21$bc2,.2` .2,c7$P2\",cb\n23$b bc,.2\"2" +
		"?14$2F2\"5?2\",J5$P\" ,zd3$\n24$ ?$3?%3 `2\"2?12$bcucd3$P3\"2 2=7$\n23$P" +
		"\" ,3;<5!>2;,. `4\"6?2\"2 ,9;, `\"?2$\n"
	count := 0
	for i := range len(urtle) {
		p := urtle[i]
		if '0' <= p && p <= '9' {
			count = count*10 + int(p-'0')
		} else {
			for j := 0; j < max(count, 1); j++ {
				fmt.Printf("%c", p)
			}
			count = 0
		}
	}
	return 0
}
func (m *NinjaMain) ToolRules(opts *Options, args []string) int {
	toolUsage := func() {
		fmt.Printf(`
usage: ninja -t rules [options]

options:
  -d     also print the description of the rule
  -h     print this message
`)
	}
	fs := flag.NewFlagSet("ToolClean", flag.ContinueOnError)
	fs.Usage = toolUsage
	printDescription := fs.Bool("d", false, "also print the description of the rule")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	rules := m.state.Bindings().GetRules()
	for ruleName, rule := range rules {
		fmt.Printf("%s", ruleName)
		if *printDescription {
			description, ok := rule.GetBinding("description")
			if ok && description != nil {
				fmt.Printf(": %s", description.Unparse())
			}
		}
		fmt.Printf("\n")
	}
	return 0
}

func (m *NinjaMain) ToolWinCodePage(*Options, []string) int {
	util.Error("TBD: windows support")
	return 1
}

type PrintCommandMode int

const (
	PCMSingle PrintCommandMode = iota
	PCMAll
)

func PrintCommands(edge *graph.Edge, seen map[*graph.Edge]struct{}, mode PrintCommandMode) {
	if edge == nil {
		return
	}
	if _, ok := seen[edge]; ok {
		return
	}
	seen[edge] = struct{}{}

	if mode == PCMAll {
		for _, in := range edge.Inputs() {
			PrintCommands(in.InEdge(), seen, mode)
		}
	}
	if !edge.IsPhony() {
		fmt.Printf("%s\n", edge.EvaluateCommand(false))
	}
}

func ChooseTool(toolName string) *Tool {
	tools := []*Tool{
		{"browse", "browse dependency graph in a web browser", runAfterLoad, lookupDispatchFunc("ToolBrowse")},
		{"clean", "clean built files", runAfterLoad, lookupDispatchFunc("ToolClean")},
		{"query", "show inputs/outputs for a path", runAfterLogs, lookupDispatchFunc("ToolQuery")},
		{"commands", "list all commands required to rebuild given targets", runAfterLoad, lookupDispatchFunc("ToolCommands")},
		{"inputs", "list all inputs required to rebuild given targets", runAfterLoad, lookupDispatchFunc("ToolInputs")},
		{"multi-inputs", "print one or more sets of inputs required to build targets", runAfterLoad, lookupDispatchFunc("ToolMultiInputs")},
		{"deps", "show dependencies stored in the deps log", runAfterLogs, lookupDispatchFunc("ToolDeps")},
		{"missingdeps", "check deps log dependencies on generated files", runAfterLogs, lookupDispatchFunc("ToolMissingDeps")},
		{"graph", "output graphviz dot file for targets", runAfterLoad, lookupDispatchFunc("ToolGraph")},

		{"targets", "list targets by their rule or depth in the DAG", runAfterLoad, lookupDispatchFunc("ToolTargets")},
		{"compdb", "dump JSON compilation database to stdout", runAfterLoad, lookupDispatchFunc("ToolCompilationDatabase")},
		{"compdb-targets", "dump JSON compilation database for a given list of targets to stdout", runAfterLoad, lookupDispatchFunc("ToolCompilationDatabaseForTargets")},
		{"recompact", "recompacts ninja-internal data structures", runAfterLoad, lookupDispatchFunc("ToolRecompact")},
		{"restat", "restats all outputs in the build log", runAfterFlags, lookupDispatchFunc("ToolRestat")},
		{"rules", "list all rules", runAfterLoad, lookupDispatchFunc("ToolRules")},
		{"cleandead", "clean built files that are no longer produced by the manifest", runAfterLogs, lookupDispatchFunc("ToolCleanDead")},
		{"urtle", "", runAfterFlags, lookupDispatchFunc("ToolUrtle")},
	}

	if toolName == "list" {
		fmt.Printf("ninja subtools:\n")
		for _, tool := range tools {
			if tool.Desc != "" {
				fmt.Printf("%11s  %s\n", tool.Name, tool.Desc)
			}
		}
		return nil
	}

	for _, tool := range tools {
		if tool.Name == toolName {
			return tool
		}
	}

	words := make([]string, 0, len(tools))
	for _, tool := range tools {
		words = append(words, tool.Name)
	}
	suggestion := util.SpellcheckString(toolName, words...)
	if suggestion != "" {
		util.Fatalf("unknown tool '%s', did you mean '%s'?", toolName, suggestion)
	} else {
		util.Fatalf("unknown tool '%s'", toolName)
	}
	return nil // Not reached.
}

// Parse argv for command-line options.
// Returns an exit code, or -1 if Ninja should continue.
func ReadFlags(args []string, options *Options, config *build_config.Config) int {
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
		options.Tool = ChooseTool(*tool)
		if options.Tool == nil {
			return 0
		} else {
			return -1
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
		return 0
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

// from https://stackoverflow.com/questions/74678438/go-flags-ignore-unknown-input
// you rule, cardinalby.
func StripUnknownFlags(flagSet *flag.FlagSet, args []string) ([]string, []string) {
	formalFlagNames := getFormalFlagNames(flagSet)

	res := make([]string, 0, len(args))
	stripped := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		arg := args[i]
		isFlag, isTerminator, flagName, hasInlineValue := parseArg(arg)
		if isTerminator {
			res = append(res, args[i:]...)
			break
		}
		if !isFlag {
			res = append(res, arg)
			continue
		}
		isBoolFlag, exists := formalFlagNames[flagName]
		var appendTo *[]string
		if exists {
			appendTo = &res
		} else {
			appendTo = &stripped
		}
		*appendTo = append(*appendTo, arg)
		if !hasInlineValue && !isBoolFlag {
			// next arg is supposed to be the flag value
			if i+1 < len(args) {
				*appendTo = append(*appendTo, args[i+1])
			}
			i++ // skip the flag value

			if arg == "-t" { // tool args, stop parsing here.
				if len(args) > i {
					stripped = append(stripped, args[i+1:]...)
				}
				break
			}
		}
	}
	return res, stripped
}

func parseArg(arg string) (isFlag bool, isTerminator bool, flagName string, hasInlineValue bool) {
	if len(arg) < 2 || arg[0] != '-' {
		return false, false, "", false
	}
	numMinuses := 1
	if arg[1] == '-' {
		numMinuses++
		if len(arg) == 2 { // "--" terminates the flags
			return false, true, "", false
		}
	}
	flagName = arg[numMinuses:]

	if equalsSignIndex := strings.Index(flagName, "="); equalsSignIndex == 0 {
		// std FlagSet.Parse() will return "bad flag syntax" error
		return false, false, "", false
	} else if equalsSignIndex > 0 {
		flagName = flagName[:equalsSignIndex]
		hasInlineValue = true
	}
	return true, false, flagName, hasInlineValue
}

// preprocessGetoptStyleFlags converts getopt-style flags like "-j3" to "-j=3"
// for single-character flags that take values. This is needed because Go's
// flag package doesn't support the getopt-style syntax.
func preprocessGetoptStyleFlags(args []string, valueFlagNames map[string]bool) []string {
	result := make([]string, 0, len(args))
	for _, arg := range args {
		// Only process single-dash flags (not --flags)
		if len(arg) > 2 && arg[0] == '-' && arg[1] != '-' && !strings.Contains(arg, "=") {
			// Check if the first character after '-' is a known value flag
			flagName := string(arg[1])
			if valueFlagNames[flagName] {
				// Convert -jN to -j=N
				result = append(result, "-"+flagName+"="+arg[2:])
				continue
			}
		}
		result = append(result, arg)
	}
	return result
}

// getValueFlagNames returns the set of single-character flag names that take values
func getValueFlagNames() map[string]bool {
	return map[string]bool{
		"j": true, // jobs
		"C": true, // working dir
		"f": true, // build file
		"k": true, // allowed failures
		"l": true, // max load
		"t": true, // tool
		"d": true, // debugging
		"w": true, // warnings
	}
}

// optional interface to indicate boolean flags that can be
// supplied without "=value" text
type boolFlag interface {
	flag.Value
	IsBoolFlag() bool
}

// getFormalFlagNames returns a map where key is a flag name and value indicates it's a bool flag
func getFormalFlagNames(flagSet *flag.FlagSet) map[string]bool {
	flags := make(map[string]bool)
	flagSet.VisitAll(func(f *flag.Flag) {
		isBoolFlag := false
		if boolFlag, ok := f.Value.(boolFlag); ok {
			isBoolFlag = boolFlag.IsBoolFlag()
		}
		flags[f.Name] = isBoolFlag
	})
	return flags
}

func main() {
	// Set up signal handling early to catch interrupts before any subprocesses start.
	subprocess.SetupSignalHandling()

	config := build_config.Create()
	options := &Options{}
	options.InputFile = "build.ninja"

	registerFlags()
	flag.Usage = func() { Usage(config) }

	// This is a funny little dance we have to do in order to parse flags in roughly
	// the same way that ninja does. Because we're not using getopt and parsing
	// iteratively, sub-command flags will throw off the main flag parser. So they
	// are first stripped, then passed in via the positionalArgs slice.
	//
	// First, preprocess args to convert getopt-style flags like "-j3" to "-j=3"
	preprocessedArgs := preprocessGetoptStyleFlags(os.Args, getValueFlagNames())
	knownFlags, unknownFlags := StripUnknownFlags(flag.CommandLine, preprocessedArgs)
	os.Args = knownFlags

	rcRules, err := ninjarc.ParseRCFiles(options.WorkingDir, "~/.ninjarc")
	if err != nil {
		util.Fatal(err.Error())
	}

	flag.Parse()

	toolName := "build" // 'build' is the default.
	if *tool != "" {
		toolName = *tool
	}
	for _, rule := range rcRules {
		if rule.Phase != "common" && rule.Phase != toolName {
			continue
		}
		if rule.Config != *configFlag {
			continue
		}
		rule.ApplyToFlags()
	}

	flag.Parse()

	ninjaCommand := os.Args[0]
	positionalArgs := append(unknownFlags, flag.Args()...)

	exitCode := ReadFlags(positionalArgs, options, config)
	if exitCode >= 0 {
		os.Exit(exitCode)
	}

	status := status.NewPrinter(config)
	status.InitializeTool(toolName, positionalArgs)

	osExit := func(exitCode int) {
		status.FinalizeTool(exitCode)
		os.Exit(exitCode)
	}

	if options.WorkingDir != "" {
		// The formatting of this string, complete with funny quotes, is
		// so Emacs can properly identify that the cwd has changed for
		// subsequent commands.
		// Don't print this if a tool is being used, so that tool output
		// can be piped into a file without this string showing up.
		if options.Tool == nil && config.Verbosity != build_config.NoStatusUpdate {
			status.Info("Entering directory `%s'", options.WorkingDir)
		}
		if err := os.Chdir(options.WorkingDir); err != nil {
			util.Fatalf("chdir to '%s' - %s", options.WorkingDir, err)
		}
	}

	if options.Tool != nil && options.Tool.When == runAfterFlags {
		// None of the RUN_AFTER_FLAGS actually use a NinjaMain, but it's needed
		// by other tools.
		ninja := NewNinjaMain(ninjaCommand, config)
		exitCode := options.Tool.ToolFunc(ninja, options, positionalArgs)
		osExit(exitCode)
	}

	// Limit number of rebuilds, to prevent infinite loops.
	cycleLimit := 100
	for cycle := 1; cycle <= cycleLimit; cycle++ {
		ninja := NewNinjaMain(ninjaCommand, config)

		parserOpts := manifest_parser.ManifestParserOptions{}
		if options.PhonyCycleShouldErr {
			parserOpts.PhonyCycleAction = manifest_parser.PhonyCycleActionError
		}
		parser := manifest_parser.New(ninja.state, ninja.diskInterface, parserOpts)
		if err := parser.ParseFile(options.InputFile); err != nil {
			status.Error("%s", err.Error())
			osExit(1)
		}

		if options.Tool != nil && options.Tool.When == runAfterLoad {
			osExit(options.Tool.ToolFunc(ninja, options, positionalArgs))
		}

		if !ninja.EnsureBuildDirExists() {
			osExit(1)
		}

		status.SetBuildDir(ninja.buildDir)
		if !ninja.OpenBuildLog(false /*=recompactOnly*/) || !ninja.OpenDepsLog(false /*=recompactOnly*/) {
			osExit(1)
		}

		if options.Tool != nil && options.Tool.When == runAfterLogs {
			osExit(options.Tool.ToolFunc(ninja, options, positionalArgs))
		}

		// Attempt to rebuild the manifest before building anything else
		ok, err := ninja.RebuildManifest(options.InputFile, status)
		if ok {
			// In dry_run mode the regeneration will succeed without changing the
			// manifest forever. Better to return immediately.
			if config.DryRun {
				osExit(0)
			}
			// Start the build over with the new manifest.
			continue
		} else if err != nil {
			status.Error("rebuilding '%s': %s", options.InputFile, err)
			osExit(1)
		}

		ninja.ParsePreviousElapsedTimes()

		result := ninja.RunBuild(positionalArgs, status)
		if metrics.DefaultMetrics != nil {
			ninja.DumpMetrics()
		}
		osExit(int(result))
	}

	status.Error("manifest '%s' still dirty after %d tries, perhaps system time is not set", options.InputFile, cycleLimit)
	osExit(1)
}
