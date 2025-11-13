package status

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/buildbuddy-io/gin/internal/build_config"
	"github.com/buildbuddy-io/gin/internal/build_event_publisher"
	"github.com/buildbuddy-io/gin/internal/exit_status"
	"github.com/buildbuddy-io/gin/internal/explanations"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/line_printer"
	"github.com/buildbuddy-io/gin/internal/remote_headers"
	"github.com/buildbuddy-io/gin/internal/util"
	"github.com/buildbuddy-io/gin/internal/version"
	"github.com/google/uuid"

	bespb "github.com/buildbuddy-io/gin/genproto/build_event_stream"
	bepb "github.com/buildbuddy-io/gin/genproto/build_events"
	clpb "github.com/buildbuddy-io/gin/genproto/command_line"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
)

var (
	besBackend   = flag.String("bes_backend", "", "BES backend target, like remote.buildbuddy.io")
	resultsURL   = flag.String("results_url", "https://app.buildbuddy.io", "BuildBuddy results URL")
	invocationID = flag.String("invocation_id", "", "Invocation ID to use (auto-generated if not specified)")
)

type Status interface {
	EdgeAddedToPlan(edge *graph.Edge)
	EdgeRemovedFromPlan(edge *graph.Edge)

	BuildEdgeStarted(edge *graph.Edge, startTimeMillis int64)
	BuildEdgeFinished(edge *graph.Edge, startTimeMillis, endTimeMillis int64, exitCode exit_status.ExitStatusType, output string)

	// InitializeTool is called by ninja.go to report the program command
	// line before work begins.
	InitializeTool(toolName string, args []string)

	// BuildStarted is called when build.go starts running the build process.
	BuildStarted()

	// BuildFinished is called when build.go completes running the build process.
	BuildFinished()

	// FinalizeTool is called by ninja.go to report the exit code before
	// the program completely exits.
	FinalizeTool(ninjaExitCode int)

	SetExplanations(explanations *explanations.Explanations)

	Info(format string, args ...interface{})
	Warning(format string, args ...interface{})
	Error(format string, args ...interface{})
}

type SlidingRateInfo struct {
	rate       float64
	n          int64
	times      []float64
	lastUpdate int64
}

func NewSlidingRateInfo(n int) *SlidingRateInfo {
	return &SlidingRateInfo{
		n:          int64(n),
		rate:       -1,
		lastUpdate: -1,
		times:      make([]float64, 0),
	}
}

func (i *SlidingRateInfo) UpdateRate(updateHint, timeMillis int64) {
	if updateHint == i.lastUpdate {
		return
	}
	i.lastUpdate = updateHint
	if len(i.times) == int(i.n) {
		i.times = i.times[1:] // pop
	}
	i.times = append(i.times, float64(timeMillis))
	if i.times[0] != i.times[len(i.times)-1] {
		i.rate = float64(len(i.times)) / ((i.times[len(i.times)-1] - i.times[0]) / 1e3)
	}
}

func (i *SlidingRateInfo) Rate() float64 {
	return i.rate
}

var _ Status = &StatusPrinter{}

type StatusPrinter struct {
	config *build_config.Config

	startedEdges  int64
	finishedEdges int64
	totalEdges    int64
	runningEdges  int64

	// How much wall clock elapsed so far?
	timeMillis int64

	// How much cpu clock elapsed so far?
	cpuTimeMillis int64

	// What percentage of predicted total time have elapsed already?
	timePredictedPercentage float64

	// Out of all the edges, for how many do we know previous time?
	etaPredictableEdgesTotal int64
	// And how much time did they all take?
	etaPredictableCpuTimeTotalMillis int64

	// Out of all the non-finished edges, for how many do we know previous time?
	etaPredictableEdgesRemaining int64
	// And how much time will they all take?
	etaPredictableCpuTimeRemainingMillis int64

	// For how many edges we don't know the previous run time?
	etaUnpredictableEdgesRemaining int64

	progressStatusFormat string

	printer *line_printer.LinePrinter

	// Why is OptionalExplanations not used here?
	explanations *explanations.Explanations

	currentRate *SlidingRateInfo

	// the fields below will only be set if a bes_backed is enabled.
	// their access should be guarded on the presence of bes.
	// TODO(tylerw): move to a struct?
	bes          *build_event_publisher.Publisher
	invocationID string
	ctx          context.Context
}

func NewPrinter(config *build_config.Config) *StatusPrinter {
	sp := &StatusPrinter{
		config:               config,
		printer:              line_printer.New(),
		currentRate:          NewSlidingRateInfo(config.Parallelism),
		progressStatusFormat: os.Getenv("NINJA_STATUS"),
	}
	if sp.progressStatusFormat == "" {
		sp.progressStatusFormat = "[%f/%t] "
	}

	if *besBackend != "" {
		sp.invocationID = *invocationID
		if sp.invocationID == "" {
			sp.invocationID = uuid.New().String()
		}

		extraHeaders := remote_headers.GetPairs()
		publisher, err := build_event_publisher.New(*besBackend, sp.invocationID, extraHeaders)
		if err != nil {
			util.Errorf("failed to create publisher: %s", err)
			return sp
		}
		sp.ctx = context.TODO()
		sp.bes = publisher

		sp.bes.Start(sp.ctx)
		sp.printStreamURL()
	}
	return sp
}

func (p *StatusPrinter) printStreamURL() {
	if p.bes == nil {
		return
	}
	invocationURL := fmt.Sprintf("%s/invocation/%s", *resultsURL, p.invocationID)
	streamingLog := fmt.Sprintf(p.printer.Esc(32) + "INFO:" + p.printer.Esc() + fmt.Sprintf(" Streaming results to: %s", p.printer.Esc(4, 34)+invocationURL+p.printer.Esc()))

	fmt.Fprintln(os.Stderr, streamingLog)
}

func (p *StatusPrinter) EdgeAddedToPlan(edge *graph.Edge) {
	p.totalEdges += 1

	// Do we know how long did this edge take last time?
	if edge.PrevElapsedTimeMillis() != -1 {
		p.etaPredictableEdgesTotal += 1
		p.etaPredictableEdgesRemaining += 1
		p.etaPredictableCpuTimeTotalMillis += edge.PrevElapsedTimeMillis()
		p.etaPredictableCpuTimeRemainingMillis += edge.PrevElapsedTimeMillis()
	} else {
		p.etaUnpredictableEdgesRemaining += 1
	}
}

func (p *StatusPrinter) EdgeRemovedFromPlan(edge *graph.Edge) {
	p.totalEdges -= 1

	// Do we know how long did this edge take last time?
	if edge.PrevElapsedTimeMillis() != -1 {
		p.etaPredictableEdgesTotal -= 1
		p.etaPredictableEdgesRemaining -= 1
		p.etaPredictableCpuTimeTotalMillis -= edge.PrevElapsedTimeMillis()
		p.etaPredictableCpuTimeRemainingMillis -= edge.PrevElapsedTimeMillis()
	} else {
		p.etaUnpredictableEdgesRemaining -= 1
	}
}

func (p *StatusPrinter) BuildEdgeStarted(edge *graph.Edge, startTimeMillis int64) {
	p.startedEdges += 1
	p.runningEdges += 1
	p.timeMillis = startTimeMillis

	if edge.UseConsole() || p.printer.SmartTerminal() {
		p.PrintStatus(edge, startTimeMillis)
	}

	if edge.UseConsole() {
		p.printer.SetConsoleLocked(true)
	}
}

func (p *StatusPrinter) RecalculateProgressPrediction() {
	p.timePredictedPercentage = 0.0

	// Sometimes, the previous and actual times may be wildly different.
	// For example, the previous build may have been fully recovered from ccache,
	// so it was blazing fast, while the new build no longer gets hits from ccache
	// for whatever reason, so it actually compiles code, which takes much longer.
	// We should detect such cases, and avoid using "wrong" previous times.

	// Note that we will only use the previous times if there are edges with
	// previous time knowledge remaining.
	usePreviousTimes := p.etaPredictableEdgesRemaining > 0 && p.etaPredictableCpuTimeRemainingMillis > 0

	// Iff we have sufficient statistical information for the current run,
	// that is, if we have took at least 15 sec AND finished at least 5% of edges,
	// we can check whether our performance so far matches the previous one.
	if usePreviousTimes && p.totalEdges > 0 && p.finishedEdges > 0 && (p.timeMillis > 15*1e3) && (float64(p.finishedEdges)/float64(p.totalEdges)) >= .05 {
		// Over the edges we've just run, how long did they take on average?
		actualAverageCpuTimeMillis := float64(p.cpuTimeMillis) / float64(p.finishedEdges)
		previousAverageCpuTimeMillis := float64(p.etaPredictableCpuTimeTotalMillis) / float64(p.etaPredictableEdgesTotal)

		ratio := max(previousAverageCpuTimeMillis, actualAverageCpuTimeMillis) /
			min(previousAverageCpuTimeMillis, actualAverageCpuTimeMillis)

		// Let's say that the average times should differ by less than 10x
		usePreviousTimes = ratio < 10
	}
	edgesWithKnownRuntime := p.finishedEdges
	if usePreviousTimes {
		edgesWithKnownRuntime += p.etaPredictableEdgesRemaining
	}
	if edgesWithKnownRuntime == 0 {
		return
	}

	var edgesWithUnknownRuntime int64
	if usePreviousTimes {
		edgesWithUnknownRuntime = p.etaUnpredictableEdgesRemaining
	} else {
		edgesWithUnknownRuntime = p.totalEdges - p.finishedEdges
	}

	// Given the time elapsed on the edges we've just run,
	// and the runtime of the edges for which we know previous runtime,
	// what's the edge's average runtime?
	edgesKnownRuntimeTotalMills := p.cpuTimeMillis
	if usePreviousTimes {
		edgesKnownRuntimeTotalMills += p.etaPredictableCpuTimeRemainingMillis
	}

	averageCpuTimeMillis := float64(edgesKnownRuntimeTotalMills) / float64(edgesWithKnownRuntime)
	// For the edges for which we do not have the previous runtime,
	// let's assume that their average runtime is the same as for the other edges,
	// and we therefore can predict their remaining runtime.
	unpredictableCpuTimeRemainingMillis := averageCpuTimeMillis * float64(edgesWithUnknownRuntime)
	// And therefore we can predict the remaining and total runtimes.
	totalCpuTimeRemainingMillis := unpredictableCpuTimeRemainingMillis
	if usePreviousTimes {
		totalCpuTimeRemainingMillis += float64(p.etaPredictableCpuTimeRemainingMillis)
	}
	totalCpuTimeMillis := float64(p.cpuTimeMillis) + totalCpuTimeRemainingMillis
	if totalCpuTimeMillis == 0.0 {
		return
	}

	// After that we can tell how much work we've completed, in time units
	p.timePredictedPercentage = float64(p.cpuTimeMillis) / totalCpuTimeMillis
}

func (p *StatusPrinter) PrintStatus(edge *graph.Edge, timeMillis int64) {
	if p.explanations != nil {
		// Collect all explanations for the current edge's outputs.
		exps := make([]string, 0)
		for _, output := range edge.Outputs() {
			exps = append(exps, p.explanations.LookupAndAppend(output)...)
		}
		if len(exps) > 0 {
			// Start a new line so that the first explanation does not append to the
			// status line.
			p.printer.PrintOnNewline("")
			for _, exp := range exps {
				fmt.Fprintf(os.Stderr, "ninja explain: %s\n", exp)
			}
		}
	}

	if p.config.Verbosity == build_config.Quiet || p.config.Verbosity == build_config.NoStatusUpdate {
		return
	}

	p.RecalculateProgressPrediction()

	forceFullCommand := p.config.Verbosity == build_config.Verbose

	toPrint := edge.GetBinding("description")
	if toPrint == "" || forceFullCommand {
		toPrint = edge.GetBinding("command")
	}

	toPrint = p.FormatProgressStatus(p.progressStatusFormat, timeMillis) + toPrint

	elideMode := line_printer.Elide
	if forceFullCommand {
		elideMode = line_printer.Full
	}

	p.printer.Print(toPrint, elideMode)
}

func (p *StatusPrinter) BuildEdgeFinished(edge *graph.Edge, startTimeMillis, endTimeMillis int64, exitCode exit_status.ExitStatusType, output string) {
	p.timeMillis = endTimeMillis
	p.finishedEdges += 1

	elapsed := endTimeMillis - startTimeMillis
	p.cpuTimeMillis += elapsed

	// Do we know how long did this edge take last time?
	if edge.PrevElapsedTimeMillis() != -1 {
		p.etaPredictableEdgesRemaining -= 1
		p.etaPredictableCpuTimeRemainingMillis -= edge.PrevElapsedTimeMillis()
	} else {
		p.etaUnpredictableEdgesRemaining -= 1
	}

	if edge.UseConsole() {
		p.printer.SetConsoleLocked(false)
	}

	if p.config.Verbosity == build_config.Quiet {
		return
	}

	if !edge.UseConsole() {
		p.PrintStatus(edge, endTimeMillis)
	}

	p.runningEdges -= 1

	if p.bes != nil {
		p.logToBes(bepb.ConsoleOutputStream_STDOUT, edge.EvaluateCommand(false))
	}

	// Print the command that is spewing before printing its output.
	if exitCode != exit_status.ExitSuccess {
		var outputs string
		for _, o := range edge.Outputs() {
			outputs += o.Path() + " "
		}

		failed := fmt.Sprintf("FAILED: [code=%d] ", exitCode)
		if p.printer.SupportsColor() {
			p.printer.PrintOnNewline("\x1B[31m" + failed + "\x1B[0m" + outputs + "\n")
		} else {
			p.printer.PrintOnNewline(failed + outputs + "\n")
		}
		p.printer.PrintOnNewline(edge.EvaluateCommand(false) + "\n")
	}

	if output != "" {
		// ninja sets stdout and stderr of subprocesses to a pipe, to be able to
		// check if the output is empty. Some compilers, e.g. clang, check
		// isatty(stderr) to decide if they should print colored output.
		// To make it possible to use colored output with ninja, subprocesses should
		// be run with a flag that forces them to always print color escape codes.
		// To make sure these escape codes don't show up in a file if ninja's output
		// is piped to a file, ninja strips ansi escape codes again if it's not
		// writing to a |smart_terminal_|.
		// (Launching subprocesses in pseudo ttys doesn't work because there are
		// only a few hundred available on some systems, and ninja can launch
		// thousands of parallel compile commands.)
		if p.printer.SupportsColor() && !strings.Contains(output, "\x1b") {
			p.printer.PrintOnNewline(output)
		} else {
			finalOutput := util.StripAnsiEscapeCodes(output)
			p.printer.PrintOnNewline(finalOutput)
		}
	}
}

func (p *StatusPrinter) BuildStarted() {
	p.startedEdges = 0
	p.finishedEdges = 0
	p.runningEdges = 0

	if p.bes != nil {
		if err := p.bes.Publish(buildMetadataEvent()); err != nil {
			util.Warningf("Failed to publish build metadata: %s", err)
		}

		if err := p.bes.Publish(workspaceStatusEvent()); err != nil {
			util.Warningf("Failed to publish workspace status: %s", err)
		}

		if err := p.bes.Publish(configurationEvent()); err != nil {
			util.Warningf("Failed to publish configuration: %s", err)
		}
	}
}

func (p *StatusPrinter) BuildFinished() {
	p.printer.SetConsoleLocked(false)
	p.printer.PrintOnNewline("")

	if p.bes != nil {
		if err := p.bes.Publish(buildMetricsEvent(p.startedEdges, p.finishedEdges, p.cpuTimeMillis, p.timeMillis)); err != nil {
			util.Warningf("Failed to publish configuration: %s", err)
		}
	}
}

func (p *StatusPrinter) InitializeTool(toolName string, args []string) {
	if p.bes == nil {
		return
	}
	if err := p.bes.Publish(startedEvent(toolName, os.Args, p.invocationID, time.Now())); err != nil {
		util.Warningf("Failed to publish started event: %s", err)
	}

	if err := p.bes.Publish(structuredCommandLineEvent(args)); err != nil {
		util.Warningf("Failed to publish structured command line: %s", err)
	}

}

func (p *StatusPrinter) FinalizeTool(ninjaExitCode int) {
	if p.bes == nil {
		return
	}

	if err := p.bes.Publish(finishedEvent(ninjaExitCode)); err != nil {
		util.Warningf("Failed to publish finished event: %s", err)
	}

	if err := p.bes.Finish(); err != nil {
		util.Warningf("Failed to finish publishing events: %s", err)
	}
	p.printStreamURL()
}

func (p *StatusPrinter) SetExplanations(exp *explanations.Explanations) {
	p.explanations = exp
}

func formatRate(rate float64, format string) string {
	if rate == -1 {
		return "?"
	}
	return fmt.Sprintf(format, rate)
}

func (p *StatusPrinter) FormatProgressStatus(format string, timeMillis int64) string {
	var out strings.Builder

	for i := 0; i < len(format); i++ {
		if format[i] == '%' && i+1 < len(format) {
			i++
			switch format[i] {
			case '%':
				out.WriteByte('%')

			// Started edges
			case 's':
				out.WriteString(fmt.Sprintf("%d", p.startedEdges))

			// Total edges
			case 't':
				out.WriteString(fmt.Sprintf("%d", p.totalEdges))

			// Running edges
			case 'r':
				out.WriteString(fmt.Sprintf("%d", p.runningEdges))

			// Unstarted edges
			case 'u':
				out.WriteString(fmt.Sprintf("%d", p.totalEdges-p.startedEdges))

			// Finished edges
			case 'f':
				out.WriteString(fmt.Sprintf("%d", p.finishedEdges))

			// Overall finished edges per second
			case 'o':
				rate := float64(p.finishedEdges) / (float64(timeMillis) / 1e3)
				out.WriteString(formatRate(rate, "%.1f"))

			// Current rate, average over the last '-j' jobs
			case 'c':
				if p.currentRate != nil {
					p.currentRate.UpdateRate(p.finishedEdges, timeMillis)
					out.WriteString(formatRate(p.currentRate.Rate(), "%.1f"))
				} else {
					out.WriteString("?")
				}

			// Percentage of edges completed
			case 'p':
				percent := int64(0)
				if p.finishedEdges != 0 && p.totalEdges != 0 {
					percent = (100 * p.finishedEdges) / p.totalEdges
				}
				out.WriteString(fmt.Sprintf("%3d%%", percent))

			// Wall time and ETA formatting
			case 'e', 'w', 'E', 'W':
				elapsedSec := float64(timeMillis) / 1e3
				etaSec := -1.0

				if p.timePredictedPercentage != 0.0 {
					totalWallTime := float64(timeMillis) / p.timePredictedPercentage
					etaSec = (totalWallTime - float64(timeMillis)) / 1e3
				}

				printWithHours := elapsedSec >= 60*60 || etaSec >= 60*60

				sec := -1.0
				switch format[i] {
				case 'e', 'w':
					sec = elapsedSec
				case 'E', 'W':
					sec = etaSec
				}

				if sec < 0 {
					out.WriteString("?")
				} else {
					switch format[i] {
					case 'e', 'E': // seconds format
						out.WriteString(fmt.Sprintf("%.3f", sec))
					case 'w', 'W': // human-readable format
						if printWithHours {
							hours := int64(sec) / 3600
							minutes := (int64(sec) % 3600) / 60
							seconds := int64(sec) % 60
							out.WriteString(fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds))
						} else {
							minutes := int64(sec) / 60
							seconds := int64(sec) % 60
							out.WriteString(fmt.Sprintf("%02d:%02d", minutes, seconds))
						}
					}
				}

			// Percentage of time spent out of the predicted time total
			case 'P':
				percent := int(100.0 * p.timePredictedPercentage)
				out.WriteString(fmt.Sprintf("%3d%%", percent))

			default:
				panic(fmt.Sprintf("unknown placeholder '%%%c' in NINJA_STATUS", format[i]))
			}
		} else {
			out.WriteByte(format[i])
		}
	}

	return out.String()
}

func (p *StatusPrinter) logToBes(streamType bepb.ConsoleOutputStream, format string, args ...interface{}) {
	if p.bes == nil {
		return
	}
	line := format
	if len(args) > 0 {
		line = fmt.Sprintf(format, args)
	}
	if len(line) == 0 {
		return
	}
	if line[len(line)-1] != '\n' {
		line = line + "\n"
	}
	if err := p.bes.Publish(consoleOutputEvent(line, streamType)); err != nil {
		util.Infof("Failed to publish console output: %s", err)
	}
}

func (p *StatusPrinter) Info(format string, args ...interface{}) {
	util.Infof(format, args...)

	if p.bes != nil {
		p.logToBes(bepb.ConsoleOutputStream_STDOUT, format, args)
	}
}

func (p *StatusPrinter) Warning(format string, args ...interface{}) {
	util.Warningf(format, args...)

	if p.bes != nil {
		p.logToBes(bepb.ConsoleOutputStream_STDERR, format, args)
	}
}

func (p *StatusPrinter) Error(format string, args ...interface{}) {
	util.Errorf(format, args...)

	if p.bes != nil {
		p.logToBes(bepb.ConsoleOutputStream_STDERR, format, args)
	}
}

// / Begin annoying bazel build event formatting helper code.
func startedEvent(toolName string, cmdArgs []string, invocationID string, startTime time.Time) *bespb.BuildEvent {
	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{
			Id: &bespb.BuildEventId_Started{},
		},
		Children: []*bespb.BuildEventId{
			{Id: &bespb.BuildEventId_BuildMetadata{}},
			{Id: &bespb.BuildEventId_WorkspaceStatus{}},
			{Id: &bespb.BuildEventId_Configuration{Configuration: &bespb.BuildEventId_ConfigurationId{Id: "host"}}},
			{Id: &bespb.BuildEventId_BuildFinished{}},
			{Id: &bespb.BuildEventId_StructuredCommandLine{
				StructuredCommandLine: &bespb.BuildEventId_StructuredCommandLineId{
					CommandLineLabel: "original",
				},
			}},
		},
		Payload: &bespb.BuildEvent_Started{
			Started: &bespb.BuildStarted{
				Uuid:               invocationID,
				BuildToolVersion:   version.NinjaVersion,
				StartTime:          timestamppb.New(startTime),
				Command:            toolName,
				OptionsDescription: strings.Join(cmdArgs, " "),
			},
		},
	}
}

func structuredCommandLineEvent(cmdArgs []string) *bespb.BuildEvent {
	executableName := os.Args[0]
	sections := []*clpb.CommandLineSection{
		{
			SectionLabel: "command",
			SectionType: &clpb.CommandLineSection_ChunkList{
				ChunkList: &clpb.ChunkList{
					Chunk: []string{executableName},
				},
			},
		},
		{
			SectionLabel: "executable",
			SectionType: &clpb.CommandLineSection_ChunkList{
				ChunkList: &clpb.ChunkList{
					Chunk: []string{executableName},
				},
			},
		},
	}

	if len(cmdArgs) > 1 {
		sections = append(sections, &clpb.CommandLineSection{
			SectionLabel: "arguments",
			SectionType: &clpb.CommandLineSection_ChunkList{
				ChunkList: &clpb.ChunkList{
					Chunk: cmdArgs,
				},
			},
		})
	}

	commandLine := &clpb.CommandLine{
		CommandLineLabel: "original",
		Sections:         sections,
	}

	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{
			Id: &bespb.BuildEventId_StructuredCommandLine{
				StructuredCommandLine: &bespb.BuildEventId_StructuredCommandLineId{
					CommandLineLabel: "original",
				},
			},
		},
		Payload: &bespb.BuildEvent_StructuredCommandLine{
			StructuredCommandLine: commandLine,
		},
	}
}

func buildMetadataEvent() *bespb.BuildEvent {
	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{
			Id: &bespb.BuildEventId_BuildMetadata{},
		},
		Payload: &bespb.BuildEvent_BuildMetadata{
			BuildMetadata: &bespb.BuildMetadata{
				Metadata: map[string]string{
					"ROLE": "NINJA",
				},
			},
		},
	}
}

func workspaceStatusEvent() *bespb.BuildEvent {
	user := os.Getenv("USER")
	if user == "" {
		user = "unknown"
	}

	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}

	cwd, err := os.Getwd()
	if err != nil {
		cwd = "unknown"
	}

	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{
			Id: &bespb.BuildEventId_WorkspaceStatus{},
		},
		Payload: &bespb.BuildEvent_WorkspaceStatus{
			WorkspaceStatus: &bespb.WorkspaceStatus{
				Item: []*bespb.WorkspaceStatus_Item{
					{Key: "BUILD_USER", Value: user},
					{Key: "BUILD_HOST", Value: hostname},
					{Key: "BUILD_WORKING_DIRECTORY", Value: cwd},
				},
			},
		},
	}

}

func configurationEvent() *bespb.BuildEvent {
	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{
			Id: &bespb.BuildEventId_Configuration{
				Configuration: &bespb.BuildEventId_ConfigurationId{
					Id: "host",
				},
			},
		},
		Payload: &bespb.BuildEvent_Configuration{
			Configuration: &bespb.Configuration{
				Mnemonic:     "host",
				PlatformName: runtime.GOOS,
				Cpu:          runtime.GOARCH,
				MakeVariable: map[string]string{
					"TARGET_CPU": runtime.GOARCH,
				},
			},
		},
	}

}

func buildMetricsEvent(actionsCreated, actionsExecuted, cpuTimeMillis, wallTimeMillis int64) *bespb.BuildEvent {
	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{
			Id: &bespb.BuildEventId_BuildMetrics{},
		},
		Payload: &bespb.BuildEvent_BuildMetrics{
			BuildMetrics: &bespb.BuildMetrics{
				ActionSummary: &bespb.BuildMetrics_ActionSummary{
					ActionsCreated:                    actionsCreated,
					ActionsCreatedNotIncludingAspects: actionsCreated,
					ActionsExecuted:                   actionsExecuted,
				},
				TimingMetrics: &bespb.BuildMetrics_TimingMetrics{
					CpuTimeInMs:  cpuTimeMillis,
					WallTimeInMs: wallTimeMillis,
				},
			},
		},
	}
}

func finishedEvent(exitCode int) *bespb.BuildEvent {
	var exitCodeName string

	// From https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/util/ExitCode.java#L38
	switch exit_status.ExitStatusType(exitCode) {
	case exit_status.ExitSuccess:
		exitCodeName = "SUCCESS"
	case exit_status.ExitInterrupted:
		exitCodeName = "INTERRUPTED"
	case exit_status.ExitFailure:
		exitCodeName = "FAILED"
	}

	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{
			Id: &bespb.BuildEventId_BuildFinished{},
		},
		Children: []*bespb.BuildEventId{
			{Id: &bespb.BuildEventId_BuildToolLogs{}},
		},
		LastMessage: true,
		Payload: &bespb.BuildEvent_Finished{
			Finished: &bespb.BuildFinished{
				ExitCode: &bespb.BuildFinished_ExitCode{
					Name: exitCodeName,
					Code: int32(exitCode),
				},
				FinishTime: timestamppb.Now(),
			},
		},
	}

}

func consoleOutputEvent(output string, streamType bepb.ConsoleOutputStream) *bespb.BuildEvent {
	progress := &bespb.Progress{}
	if streamType == bepb.ConsoleOutputStream_STDOUT {
		progress.Stdout = output
	} else {
		progress.Stderr = output
	}

	return &bespb.BuildEvent{
		Id: &bespb.BuildEventId{
			Id: &bespb.BuildEventId_Progress{
				Progress: &bespb.BuildEventId_ProgressId{
					OpaqueCount: int32(time.Now().UnixNano()),
				},
			},
		},
		Payload: &bespb.BuildEvent_Progress{
			Progress: progress,
		},
	}
}

/// End annoying bazel build event formatting helper code.
