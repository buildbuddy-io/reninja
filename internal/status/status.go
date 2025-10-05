package status

import (
	"fmt"
	"os"
	"strings"

	"github.com/buildbuddy-io/gin/internal/build_config"
	"github.com/buildbuddy-io/gin/internal/exit_status"
	"github.com/buildbuddy-io/gin/internal/explanations"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/line_printer"
	"github.com/buildbuddy-io/gin/internal/util"
)

type Status interface {
	EdgeAddedToPlan(edge *graph.Edge)
	EdgeRemovedFromPlan(edge *graph.Edge)

	BuildEdgeStarted(edge *graph.Edge, startTimeMillis int64)
	BuildEdgeFinished(edge *graph.Edge, startTimeMillis, endTimeMillis int64, exitCode exit_status.ExitStatusType, output string)

	BuildStarted()
	BuildFinished()

	SetExplanations(explanations *explanations.Explanations)

	Info(format string, args ...interface{})
	Warning(format string, args ...interface{})
	Error(format string, args ...interface{})
}

type SlidingRateInfo struct {
	rate       float64
	n          int64
	times      []float64
	lastUpdate int
}

func NewSlidingRateInfo(n int) *SlidingRateInfo {
	return &SlidingRateInfo{
		n:          int64(n),
		rate:       -1,
		lastUpdate: -1,
		times:      make([]float64, 0),
	}
}

func (i *SlidingRateInfo) UpdateRate(updateHint int, timeMillis int64) {
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
	config build_config.Config

	startedEdges  int
	finishedEdges int
	totalEdges    int
	runningEdges  int

	// How much wall clock elapsed so far?
	timeMillis int64

	// How much cpu clock elapsed so far?
	cpuTimeMillis int64

	// What percentage of predicted total time have elapsed already?
	timePredictedPercentage float64

	// Out of all the edges, for how many do we know previous time?
	etaPredictableEdgesTotal int
	// And how much time did they all take?
	etaPredictableCpuTimeTotalMillis int64

	// Out of all the non-finished edges, for how many do we know previous time?
	etaPredictableEdgesRemaining int
	// And how much time will they all take?
	etaPredictableCpuTimeRemainingMillis int64

	// For how many edges we don't know the previous run time?
	etaUnpredictableEdgesRemaining int

	progressStatusFormat string

	printer *line_printer.LinePrinter

	// Why is OptionalExplanations not used here?
	explanations *explanations.Explanations

	currentRate *SlidingRateInfo
}

func NewPrinter(config build_config.Config) *StatusPrinter {
	sp := &StatusPrinter{
		config:               config,
		printer:              line_printer.New(),
		currentRate:          NewSlidingRateInfo(config.Parallelism),
		progressStatusFormat: os.Getenv("NINJA_STATUS"),
	}
	if sp.progressStatusFormat == "" {
		sp.progressStatusFormat = "[%f/%t] "
	}
	return sp
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

	var edgesWithUnknownRuntime int
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
}

func (p *StatusPrinter) BuildFinished() {
	p.printer.SetConsoleLocked(false)
	p.printer.PrintOnNewline("")
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
				percent := 0
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
