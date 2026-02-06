package status

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/buildbuddy-io/reninja/internal/bes_event"
	"github.com/buildbuddy-io/reninja/internal/build_config"
	"github.com/buildbuddy-io/reninja/internal/build_event_publisher"
	"github.com/buildbuddy-io/reninja/internal/build_metadata"
	"github.com/buildbuddy-io/reninja/internal/compact_execution"
	"github.com/buildbuddy-io/reninja/internal/compression"
	"github.com/buildbuddy-io/reninja/internal/digest"
	"github.com/buildbuddy-io/reninja/internal/exit_status"
	"github.com/buildbuddy-io/reninja/internal/explanations"
	"github.com/buildbuddy-io/reninja/internal/filetransfer"
	"github.com/buildbuddy-io/reninja/internal/flamegraph"
	"github.com/buildbuddy-io/reninja/internal/graph"
	"github.com/buildbuddy-io/reninja/internal/line_printer"
	"github.com/buildbuddy-io/reninja/internal/remote_flags"
	"github.com/buildbuddy-io/reninja/internal/remote_headers"
	"github.com/buildbuddy-io/reninja/internal/request_metadata"
	"github.com/buildbuddy-io/reninja/internal/span"
	"github.com/buildbuddy-io/reninja/internal/spawn"
	"github.com/buildbuddy-io/reninja/internal/util"
	"github.com/google/uuid"

	bespb "github.com/buildbuddy-io/reninja/genproto/build_event_stream"
	bepb "github.com/buildbuddy-io/reninja/genproto/build_events"
	repb "github.com/buildbuddy-io/reninja/genproto/remote_execution"
)

type Status interface {
	EdgeAddedToPlan(edge *graph.Edge)
	EdgeRemovedFromPlan(edge *graph.Edge)

	BuildEdgeStarted(edge *graph.Edge, absoluteStart time.Time)
	BuildEdgeFinished(edge *graph.Edge, result *spawn.Result)

	// InitializeTool is called by ninja.go to report the program command
	// line before work begins.
	InitializeTool(toolName string, args []string)

	// BuildStarted is called when build.go starts running the build process.
	BuildStarted(buildStart time.Time)

	// BuildFinished is called when build.go completes running the build process.
	BuildFinished()

	// FinalizeTool is called by ninja.go to report the exit code before
	// the program completely exits.
	FinalizeTool(ninjaExitCode int)

	SetBuildDir(dir string)
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
	config   *build_config.Config
	buildDir string

	startedEdges  int64
	finishedEdges int64
	totalEdges    int64
	runningEdges  int64

	// build start time
	buildStart time.Time

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

	ticker *time.Ticker
	done   chan bool
	mu     *sync.Mutex

	// the fields below will only be set if a bes_backed is enabled.
	// their access should be guarded on the presence of bes.
	// TODO(tylerw): move to a struct?
	bes          *build_event_publisher.Publisher
	invocationID string
	ctx          context.Context
	cleanupIO    func()
	uploader     *filetransfer.Uploader

	// The following logs are lazily initialized, once, when SetBuildDir is
	// called, so that their backing files can live in the ninja build dir.
	logsInitialized *sync.Once
	flamegraph      *flamegraph.Flamegraph

	execLogFile         *os.File
	execLogWriter       *compression.ZstdCompressingWriter
	compactExecutionLog *compact_execution.Log

	plannedTargetLabels []string
}

func NewPrinter(config *build_config.Config) *StatusPrinter {
	// Check for NINJA_STATUS. Use default only if not set, not if set to empty.
	progressStatusFormat := "[%f/%t] "
	if progressFormatOverride, ok := os.LookupEnv("NINJA_STATUS"); ok {
		progressStatusFormat = progressFormatOverride
	}
	sp := &StatusPrinter{
		config:               config,
		currentRate:          NewSlidingRateInfo(config.Parallelism),
		progressStatusFormat: progressStatusFormat,
		ticker:               time.NewTicker(500 * time.Millisecond),
		done:                 make(chan bool),
		mu:                   &sync.Mutex{},
		logsInitialized:      &sync.Once{},
	}

	if remote_flags.EnableBES() {
		sp.invocationID = remote_flags.InvocationID()
		if sp.invocationID == "" {
			sp.invocationID = uuid.New().String()
		}
		request_metadata.SetInvocationID(sp.invocationID)
		extraHeaders := remote_headers.GetPairs()
		if key, val, ok := request_metadata.GetInvocationRequestMetadata(); ok {
			extraHeaders = append(extraHeaders, key, val)
		}
		publisher, err := build_event_publisher.New(remote_flags.BESBackend(), sp.invocationID, extraHeaders)
		if err != nil {
			util.Errorf("failed to create publisher: %s", err)
			return sp
		}
		sp.ctx = context.TODO()
		sp.bes = publisher

		// Check term support before monkey patching it.
		smartTerm := line_printer.SmartTerminal()
		supportsColor := line_printer.SupportsColor()

		// Start the stream.
		sp.bes.Start(sp.ctx)

		// Hook stdout and stderr so everything gets sent there.
		unhookStdout, err := sp.wrap(bepb.ConsoleOutputStream_STDOUT)
		if err != nil {
			util.Errorf("failed to hook stdout: %s", err)
			return sp
		}
		unhookStderr, err := sp.wrap(bepb.ConsoleOutputStream_STDERR)
		if err != nil {
			util.Errorf("failed to hook stderr: %s", err)
			return sp
		}
		sp.cleanupIO = func() {
			unhookStdout()
			unhookStderr()
		}

		// Make a new line writer with the original options but our hooked stdout.
		sp.printer = line_printer.NewCustom(os.Stdout, smartTerm, supportsColor)
		sp.printStreamURL()
	} else {
		sp.printer = line_printer.New() // leave stdout alone.
	}

	return sp
}

func (p *StatusPrinter) wrap(streamType bepb.ConsoleOutputStream) (func(), error) {
	if p.bes == nil {
		return func() {}, nil
	}

	// Save a pointer to the original file so we can restore
	// it.
	var originalIO *os.File
	if streamType == bepb.ConsoleOutputStream_STDOUT {
		originalIO = os.Stdout
	} else if streamType == bepb.ConsoleOutputStream_STDERR {
		originalIO = os.Stderr
	} else {
		return nil, fmt.Errorf("unknown stream type")
	}

	// Buffer the besWriter so we don't block stdio on
	// sending each BES event.
	besReader, besWriter := io.Pipe()
	mw := io.MultiWriter(originalIO, besWriter)

	// Create an os.Pipe (not io.Pipe) with which stdio
	// can be replaced.
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	wg := new(sync.WaitGroup)

	wg.Add(1)
	go func(wg *sync.WaitGroup) {
		defer wg.Done()
		io.Copy(mw, r)
		besWriter.Close()
	}(wg)

	wg.Add(1)
	go func(wg *sync.WaitGroup) {
		defer wg.Done()
		buf := make([]byte, 1024)
		for {
			n, err := besReader.Read(buf)
			if n > 0 {
				p.bes.Publish(bes_event.ConsoleOutputEvent(string(buf[:n]), streamType))
			}
			if err != nil {
				break
			}
		}
	}(wg)

	shutItDown := func() {
		if streamType == bepb.ConsoleOutputStream_STDOUT {
			os.Stdout = originalIO
		} else if streamType == bepb.ConsoleOutputStream_STDERR {
			os.Stderr = originalIO
		} else {
			panic("should not happen")
		}

		w.Close()
		wg.Wait()
	}

	if streamType == bepb.ConsoleOutputStream_STDOUT {
		os.Stdout = w
	} else if streamType == bepb.ConsoleOutputStream_STDERR {
		os.Stderr = w
	} else {
		panic("should not happen")
	}

	return shutItDown, nil
}

func (p *StatusPrinter) printStreamURL() {
	if p.bes == nil {
		return
	}
	invocationURL := fmt.Sprintf("%s/invocation/%s", remote_flags.ResultsURL(), p.invocationID)
	streamingTo := fmt.Sprintf("Streaming results to: %s", p.printer.Esc(4, 34)+invocationURL+p.printer.Esc())
	streamingLog := p.printer.Esc(32) + "INFO: " + p.printer.Esc() + streamingTo
	fmt.Fprintln(os.Stderr, streamingLog)
}

func (p *StatusPrinter) EdgeAddedToPlan(edge *graph.Edge) {
	p.totalEdges += 1

	if p.bes != nil {
		p.plannedTargetLabels = append(p.plannedTargetLabels, edge.TargetLabel())
	}

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
func (p *StatusPrinter) BuildEdgeStarted(edge *graph.Edge, absoluteStart time.Time) {
	p.startedEdges += 1
	p.runningEdges += 1

	startTimeMillis := absoluteStart.Sub(p.buildStart).Milliseconds()
	p.timeMillis = startTimeMillis

	// In verbose mode, the status line ends with a newline and can't be
	// overwritten, so only print status at start for non-verbose smart terminal.
	isVerbose := p.config.Verbosity == build_config.Verbose
	if (edge.UseConsole() || p.printer.SmartTerminal()) && !isVerbose {
		p.PrintStatus(edge, startTimeMillis)
	}

	if edge.UseConsole() {
		p.printer.SetConsoleLocked(true)
	}

	if p.bes != nil {
		if err := p.bes.Publish(bes_event.TargetConfiguredEvent(edge.TargetLabel(), edge.ActionMnemonic(), edge.ActionID())); err != nil {
			util.Warningf("Failed to publish build metadata: %s", err)
		}
	}
	p.recordSystemMetrics(absoluteStart)
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

func computeBESFiles(uploadedFiles []*repb.OutputFile) []*bespb.File {
	bytestreamURIPrefix := remote_flags.BytestreamURIPrefix()
	instanceName := remote_flags.RemoteInstanceName()
	digestFunction := filetransfer.DigestFunction

	besFiles := make([]*bespb.File, len(uploadedFiles))
	for _, file := range uploadedFiles {
		d := file.GetDigest()
		rn := digest.NewCASResourceName(d, instanceName, digestFunction)
		uri := fmt.Sprintf("%s/%s", bytestreamURIPrefix, rn.DownloadString())
		besFiles = append(besFiles, &bespb.File{
			Name:   file.GetPath(),
			File:   &bespb.File_Uri{Uri: uri},
			Digest: d.GetHash(),
			Length: d.GetSizeBytes(),
		})
	}
	return besFiles
}

func (p *StatusPrinter) BuildEdgeFinished(edge *graph.Edge, result *spawn.Result) {
	startOffset := result.Start.Sub(p.buildStart)
	endOffset := result.End.Sub(p.buildStart)
	exitCode := result.Status
	output := result.Output

	p.timeMillis = endOffset.Milliseconds()
	p.finishedEdges += 1

	elapsed := (endOffset - startOffset).Milliseconds()
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
		p.PrintStatus(edge, endOffset.Milliseconds())
	}

	p.runningEdges -= 1

	if p.bes != nil {
		targetLabel := ""
		if outputs := edge.Outputs(); len(outputs) > 0 {
			targetLabel = outputs[0].Path()

			if len(result.Outputs) > 0 {
				if err := p.bes.Publish(bes_event.NamedSetOfFilesEvent(targetLabel, computeBESFiles(result.Outputs))); err != nil {
					util.Warningf("Failed to publish build metadata: %s", err)
				}
			}
		}
		if err := p.bes.Publish(bes_event.TargetCompletedEvent(targetLabel, exitCode)); err != nil {
			util.Warningf("Failed to publish build metadata: %s", err)
		}

		if p.flamegraph != nil {
			p.flamegraph.RecordEdge(edge, result.Start, result.End, span.Events(result.Context)...)
			p.recordSystemMetrics(result.End)
		}

		if p.compactExecutionLog != nil {
			p.compactExecutionLog.RecordEdge(edge, result)
		}
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
		if p.printer.SupportsColor() || !strings.Contains(output, "\x1b") {
			p.printer.PrintOnNewline(output)
		} else {
			finalOutput := util.StripAnsiEscapeCodes(output)
			p.printer.PrintOnNewline(finalOutput)
		}
	}
}

func (p *StatusPrinter) recordSystemMetrics(t time.Time) {
	if p.flamegraph == nil {
		return
	}
	p.mu.Lock()
	actionsRunning := p.runningEdges
	p.mu.Unlock()

	p.flamegraph.RecordActionCount(actionsRunning, t)
	p.flamegraph.RecordLoadAverage(util.GetLoadAverage(), t)
	p.flamegraph.RecordCPUUsage(util.GetProgramCPUUsage(), t)
	p.flamegraph.RecordSystemMemoryUsage(util.GetSystemMemoryUsageMB(), t)
	p.flamegraph.RecordSystemCPUUsage(util.GetSystemCPUUsageCores(), t)
	up, down := util.GetSystemNetworkUsage()
	p.flamegraph.RecordSystemNetworkUsage(up, down, t)
	p.flamegraph.RecordMemoryUsage(util.GetProgramMemoryUsageMB(), t)
}

func (p *StatusPrinter) BuildStarted(buildStart time.Time) {
	p.startedEdges = 0
	p.finishedEdges = 0
	p.runningEdges = 0
	p.buildStart = buildStart

	if p.bes != nil {
		if err := p.bes.Publish(bes_event.ExpandedEvent(bes_event.CollectFlagPatterns(), p.plannedTargetLabels)); err != nil {
			util.Warningf("Failed to publish expanded event: %s", err)
		}
	}

	p.recordSystemMetrics(buildStart)
	p.initializeLogs()

	// Periodically (every 1 second) update system metrics.
	go func() {
		for {
			select {
			case <-p.done:
				return
			case t := <-p.ticker.C:
				p.recordSystemMetrics(t)
			}
		}
	}()
}

func (p *StatusPrinter) BuildFinished() {
	p.printer.SetConsoleLocked(false)
	p.printer.PrintOnNewline("")

	if p.bes != nil {
		if p.flamegraph != nil {
			p.flamegraph.RecordGeneralInformationEvent("buildTargets", p.buildStart, time.Now())
		}
		if err := p.bes.Publish(bes_event.BuildMetricsEvent(p.startedEdges, p.finishedEdges, p.cpuTimeMillis, p.timeMillis)); err != nil {
			util.Warningf("Failed to publish configuration: %s", err)
		}
	}

	p.ticker.Stop()
	p.done <- true

	// Update system metrics one last time
	p.recordSystemMetrics(time.Now())
}

func (p *StatusPrinter) InitializeTool(toolName string, args []string) {
	if p.bes == nil {
		return
	}
	if err := p.bes.Publish(bes_event.StartedEvent(toolName, os.Args, p.invocationID, time.Now(), bes_event.CollectFlagPatterns())); err != nil {
		util.Warningf("Failed to publish started event: %s", err)
	}

	if err := p.bes.Publish(bes_event.OptionsParsedEvent(args)); err != nil {
		util.Warningf("Failed to publish structured command line: %s", err)
	}

	if err := p.bes.Publish(bes_event.StructuredCommandLineEvent(args)); err != nil {
		util.Warningf("Failed to publish structured command line: %s", err)
	}

	if err := p.bes.Publish(bes_event.BuildMetadataEvent(build_metadata.GetMetadata())); err != nil {
		util.Warningf("Failed to publish build metadata: %s", err)
	}

	if err := p.bes.Publish(bes_event.WorkspaceStatusEvent()); err != nil {
		util.Warningf("Failed to publish workspace status: %s", err)
	}

	if err := p.bes.Publish(bes_event.ConfigurationEvent()); err != nil {
		util.Warningf("Failed to publish configuration: %s", err)
	}

}

func (p *StatusPrinter) uploadCompactExecutionLog() (*digest.CASResourceName, error) {
	uploader := filetransfer.DefaultUploader()
	if uploader == nil {
		return nil, nil
	}
	if p.execLogWriter == nil {
		return nil, nil
	}

	if err := p.execLogWriter.Close(); err != nil {
		return nil, err
	}
	if err := p.execLogFile.Close(); err != nil {
		return nil, err
	}
	ctx := request_metadata.AttachCacheRequestMetadata(context.TODO(), "bes-upload", "", "")
	return uploader.UploadFile(ctx, p.execLogFile.Name())
}

func (p *StatusPrinter) uploadFlamegraph() (*digest.CASResourceName, error) {
	uploader := filetransfer.DefaultUploader()
	if uploader == nil {
		return nil, nil
	}
	if p.flamegraph == nil || p.flamegraph.NumEvents() == 0 {
		return nil, nil
	}
	tmpFile, err := os.CreateTemp("", "command-*.profile.gz")
	if err != nil {
		return nil, err
	}

	w := gzip.NewWriter(tmpFile)
	if err := p.flamegraph.Write(w); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if err := tmpFile.Close(); err != nil {
		return nil, err
	}

	ctx := request_metadata.AttachCacheRequestMetadata(context.TODO(), "bes-upload", "", "")
	return uploader.UploadFile(ctx, tmpFile.Name())
}

func (p *StatusPrinter) writeBuildLogEvent() error {
	bytestreamURIPrefix := remote_flags.BytestreamURIPrefix()

	commandProfileGz, err := p.uploadFlamegraph()
	if err != nil {
		return err
	}
	execLogBinpbZstd, err := p.uploadCompactExecutionLog()
	if err != nil {
		return err
	}
	if err := p.bes.Publish(bes_event.BuildToolLogsEvent(bytestreamURIPrefix, commandProfileGz, execLogBinpbZstd)); err != nil {
		return err
	}
	return nil
}

func (p *StatusPrinter) FinalizeTool(ninjaExitCode int) {
	if p.bes == nil {
		return
	}
	p.printStreamURL()
	p.cleanupIO()

	if err := p.writeBuildLogEvent(); err != nil {
		util.Warningf("Failed to write build tool event: %s", err)
	}

	if err := p.bes.Publish(bes_event.FinishedEvent(ninjaExitCode)); err != nil {
		util.Warningf("Failed to publish finished event: %s", err)
	}

	if err := p.bes.Finish(); err != nil {
		util.Warningf("Failed to finish publishing events: %s", err)
	}
}

func (p *StatusPrinter) SetExplanations(exp *explanations.Explanations) {
	p.explanations = exp
}

func (p *StatusPrinter) SetBuildDir(dir string) {
	p.buildDir = dir
	p.initializeLogs()
}

func (p *StatusPrinter) initializeLogs() {
	if p.bes == nil {
		return
	}

	p.logsInitialized.Do(func() {
		p.flamegraph = flamegraph.New(time.Now())

		execLogPath := ".ninja_compact_execution_log.binpb.zst"
		if p.buildDir != "" {
			execLogPath = filepath.Join(p.buildDir, execLogPath)
		}
		os.Remove(execLogPath)
		execLogFile, err := os.Create(execLogPath)
		if err != nil {
			util.Warningf("Failed to open compact exec log file: %s", err)
			return
		}
		zstdWriter, err := compression.NewZstdCompressingWriter(execLogFile, 16384)
		if err != nil {
			util.Warningf("Failed to open compact exec log compressor: %s", err)
			return
		}
		p.execLogFile = execLogFile
		p.execLogWriter = zstdWriter
		p.compactExecutionLog = compact_execution.New(p.execLogWriter)
	})
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

func (p *StatusPrinter) Info(format string, args ...interface{}) {
	util.Infof(format, args...)
}

func (p *StatusPrinter) Warning(format string, args ...interface{}) {
	util.Warningf(format, args...)
}

func (p *StatusPrinter) Error(format string, args ...interface{}) {
	util.Errorf(format, args...)
}
