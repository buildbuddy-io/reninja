package flamegraph

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/buildbuddy-io/gin/internal/build_log"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/statuserr"
)

// Phase constants
const (
	PhaseComplete = "X"
	PhaseCounter  = "C"
	PhaseMetadata = "M"
)

// Profile represents a trace profile, including all trace events.
type Profile struct {
	TraceEvents []*Event `json:"traceEvents,omitempty"`
}

// Event represents a trace event.
type Event struct {
	Category  string         `json:"cat,omitempty"`
	Name      string         `json:"name,omitempty"`
	ProcessID int64          `json:"pid,omitempty"`
	ThreadID  int64          `json:"tid,omitempty"`
	CName     string         `json:"cname,omitempty"`
	Phase     string         `json:"ph,omitempty"`
	Timestamp int64          `json:"ts"`
	Duration  int64          `json:"dur"`
	Args      map[string]any `json:"args,omitempty"`
}

// Target represents an edge.
type Target struct {
	StartTimeMillis int64
	EndTimeMillis   int64
	Targets         []string
}

type Float64Sample struct {
	Value           float64
	StartTimeMillis int64
}

type Int64Sample struct {
	Value           int64
	StartTimeMillis int64
}

// Example Event:
// {"name":"System load average","pid":1,"tid":35,"cname":"generic_work","ph":"C","ts":196155,"args":{"load":0.5126953125000001}}
func loadAverageEvent(sample Float64Sample) Event {
	return Event{
		Name:      "System load average",
		Phase:     PhaseCounter,
		ProcessID: 1,
		ThreadID:  1,
		CName:     "generic_work",
		Timestamp: sample.StartTimeMillis * 1000,
		Args:      map[string]any{"load": sample.Value},
	}
}

// Example Event:
// {"name":"CPU usage (Bazel)","pid":1,"tid":35,"cname":"good","ph":"C","ts":81196155,"args":{"cpu":3.986242310474936}},
func cpuUsageEvent(sample Float64Sample) Event {
	return Event{
		Name:      "CPU usage (cores)",
		Phase:     PhaseCounter,
		ProcessID: 1,
		ThreadID:  35,
		CName:     "good",
		Timestamp: sample.StartTimeMillis * 1000,
		Args:      map[string]any{"cpu": sample.Value},
	}

}

// Example Event:
// {"name":"Memory usage (Bazel)","pid":1,"tid":35,"cname":"olive","ph":"C","ts":216196155,"args":{"memory":708.0}}
func memoryUsageEvent(sample Float64Sample) Event {
	return Event{
		Name:      "Memory usage (Bazel)",
		Phase:     PhaseCounter,
		ProcessID: 1,
		ThreadID:  35,
		CName:     "olive",
		Timestamp: sample.StartTimeMillis * 1000,
		Args:      map[string]any{"memory": sample.Value},
	}
}

// Example Event:
// {"name":"action count","pid":1,"tid":35,"ph":"C","ts":221790845,"args":{"action":1.0,"local action cache":0.0}},
func actionCountEvent(sample Int64Sample) Event {
	return Event{
		Name:      "action count",
		Phase:     PhaseCounter,
		ProcessID: 1,
		ThreadID:  35,
		CName:     "detailed_memory_dump",
		Timestamp: sample.StartTimeMillis * 1000,
		Args:      map[string]any{"action": sample.Value},
	}
}

// Example Event:
// {"name":"CPU usage (total)","pid":1,"tid":35,"cname":"rail_load","ph":"C","ts":196155,"args":{"system cpu":2.980659307359308}}
func systemCPUUsageEvent(sample Float64Sample) Event {
	return Event{
		Name:      "CPU usage (total)",
		Phase:     PhaseCounter,
		ProcessID: 1,
		ThreadID:  35,
		CName:     "rail_load",
		Timestamp: sample.StartTimeMillis * 1000,
		Args:      map[string]any{"system cpu": sample.Value},
	}

}

// Example Event:
// {"name":"Memory usage (total)","pid":1,"tid":35,"cname":"bad","ph":"C","ts":41196155,"args":{"system memory":2041.0009999999997}}
func systemMemoryUsageEvent(sample Float64Sample) Event {
	return Event{
		Name:      "Memory usage (total)",
		Phase:     PhaseCounter,
		ProcessID: 1,
		ThreadID:  35,
		CName:     "bad",
		Timestamp: sample.StartTimeMillis * 1000,
		Args:      map[string]any{"system memory": sample.Value},
	}
}

// Example Event:
// {"name":"Network Up usage (total)","pid":1,"tid":35,"cname":"rail_response","ph":"C","ts":134196155,"args":{"system network up (Mbps)":0.049276101143796214}}
func systemNetworkUploadEvent(sample Float64Sample) Event {
	return Event{
		Name:      "Network Up usage (total)",
		Phase:     PhaseCounter,
		ProcessID: 1,
		ThreadID:  35,
		CName:     "rail_response",
		Timestamp: sample.StartTimeMillis * 1000,
		Args:      map[string]any{"system network up (Mbps)": sample.Value},
	}
}

// Example Event:
// {"name":"Network Down usage (total)","pid":1,"tid":35,"cname":"rail_response","ph":"C","ts":81196155,"args":{"system network down (Mbps)":5.327599588506307}}
func systemNetworkDownloadEvent(sample Float64Sample) Event {
	return Event{
		Name:      "Network Down usage (total)",
		Phase:     PhaseCounter,
		ProcessID: 1,
		ThreadID:  35,
		CName:     "rail_response",
		Timestamp: sample.StartTimeMillis * 1000,
		Args:      map[string]any{"system network down (Mbps)": sample.Value},
	}
}

type Flamegraph struct {
	mu                           *sync.Mutex
	targets                      map[string]*Target
	loadAverageSamples           []Float64Sample
	cpuUsageSamples              []Float64Sample
	memoryUsageSamples           []Float64Sample
	actionCountSamples           []Int64Sample
	systemCPUUsageSamples        []Float64Sample
	systemMemoryUsageSamples     []Float64Sample
	systemNetworkUploadSamples   []Float64Sample
	systemNetworkDownloadSamples []Float64Sample
	wroteFirst                   bool
}

func New() *Flamegraph {
	return &Flamegraph{
		mu:                           &sync.Mutex{},
		targets:                      make(map[string]*Target, 0),
		loadAverageSamples:           make([]Float64Sample, 0),
		cpuUsageSamples:              make([]Float64Sample, 0),
		memoryUsageSamples:           make([]Float64Sample, 0),
		actionCountSamples:           make([]Int64Sample, 0),
		systemCPUUsageSamples:        make([]Float64Sample, 0),
		systemMemoryUsageSamples:     make([]Float64Sample, 0),
		systemNetworkUploadSamples:   make([]Float64Sample, 0),
		systemNetworkDownloadSamples: make([]Float64Sample, 0),
	}
}

func (g *Flamegraph) RecordSystemNetworkUsage(upload, download float64, startTimeMillis int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.systemNetworkUploadSamples = append(g.systemNetworkUploadSamples, Float64Sample{
		Value:           upload,
		StartTimeMillis: startTimeMillis,
	})
	g.systemNetworkDownloadSamples = append(g.systemNetworkDownloadSamples, Float64Sample{
		Value:           download,
		StartTimeMillis: startTimeMillis,
	})
}

func (g *Flamegraph) RecordLoadAverage(avg float64, startTimeMillis int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.loadAverageSamples = append(g.loadAverageSamples, Float64Sample{
		Value:           avg,
		StartTimeMillis: startTimeMillis,
	})
}

func (g *Flamegraph) RecordActionCount(count int64, startTimeMillis int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.actionCountSamples = append(g.actionCountSamples, Int64Sample{
		Value:           count,
		StartTimeMillis: startTimeMillis,
	})
}

func (g *Flamegraph) RecordCPUUsage(cores float64, startTimeMillis int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cpuUsageSamples = append(g.cpuUsageSamples, Float64Sample{
		Value:           cores,
		StartTimeMillis: startTimeMillis,
	})
}

func (g *Flamegraph) RecordMemoryUsage(mb float64, startTimeMillis int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.memoryUsageSamples = append(g.memoryUsageSamples, Float64Sample{
		Value:           mb,
		StartTimeMillis: startTimeMillis,
	})
}

func (g *Flamegraph) RecordSystemMemoryUsage(mb float64, startTimeMillis int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.systemMemoryUsageSamples = append(g.systemMemoryUsageSamples, Float64Sample{
		Value:           mb,
		StartTimeMillis: startTimeMillis,
	})
}

func (g *Flamegraph) RecordSystemCPUUsage(cores float64, startTimeMillis int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.systemCPUUsageSamples = append(g.systemCPUUsageSamples, Float64Sample{
		Value:           cores,
		StartTimeMillis: startTimeMillis,
	})
}

func (g *Flamegraph) RecordEdge(edge *graph.Edge, startTimeMillis, endTimeMillis int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	command := edge.EvaluateCommand(true)
	commandHash := fmt.Sprintf("%x", build_log.HashCommand(command))

	for _, out := range edge.Outputs() {
		target, ok := g.targets[commandHash]
		if !ok {
			target = &Target{startTimeMillis, endTimeMillis, make([]string, 0)}
			g.targets[commandHash] = target
		}
		g.targets[commandHash].Targets = append(g.targets[commandHash].Targets, out.Path())
	}
}

type ThreadTracker struct {
	workers []int64
}

func (t *ThreadTracker) alloc(target *Target) (int, bool) {
	for worker := range len(t.workers) {
		if t.workers[worker] >= target.EndTimeMillis {
			t.workers[worker] = target.StartTimeMillis
			return worker, false
		}
	}
	t.workers = append(t.workers, target.StartTimeMillis)
	return len(t.workers) - 1, true
}

func (g *Flamegraph) NumEvents() int {
	return len(g.targets)
}

func (g *Flamegraph) writeEvent(w io.Writer, e *Event) error {
	delim := ",\n"
	if !g.wroteFirst {
		delim = "\n"
		g.wroteFirst = true
	}
	if _, err := io.WriteString(w, delim); err != nil {
		return fmt.Errorf("write event delimiter: %w", err)
	}

	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	if _, err := w.Write(b); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	return nil
}

func (g *Flamegraph) Write(w io.Writer) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if len(g.targets) == 0 {
		return nil
	}

	allTargets := slices.Collect(maps.Values(g.targets))

	// Sort by descending end time.
	slices.SortFunc(allTargets, func(a, b *Target) int {
		return int(b.EndTimeMillis - a.EndTimeMillis)
	})

	if _, err := io.WriteString(w, `{"traceEvents":[`); err != nil {
		return statuserr.WrapError(err, "write response")
	}
	threadTracker := &ThreadTracker{make([]int64, 0)}
	for _, target := range allTargets {
		tid, newThread := threadTracker.alloc(target)
		tid += 100 // offset thread IDs by 100 to leave room for other stuff.
		if newThread {
			ev := &Event{
				ProcessID: 1,
				ThreadID:  int64(tid),
				Name:      "thread_name",
				Args:      map[string]any{"name": fmt.Sprintf("ninja-%d", tid)},
				Phase:     PhaseMetadata,
			}
			if err := g.writeEvent(w, ev); err != nil {
				return err
			}
			mev := &Event{
				ProcessID: 1,
				ThreadID:  int64(tid),
				Name:      "thread_sort_index",
				Args:      map[string]any{"sort_index": tid},
				Phase:     PhaseMetadata,
			}
			if err := g.writeEvent(w, mev); err != nil {
				return err
			}
		}

		ev := &Event{
			Category:  "targets",
			Name:      fmt.Sprintf("%0s", strings.Join(target.Targets, ", ")),
			Phase:     PhaseComplete,
			Timestamp: target.StartTimeMillis * 1000,
			Duration:  (target.EndTimeMillis - target.StartTimeMillis) * 1000,
			ProcessID: 1,
			ThreadID:  int64(tid),
			Args:      map[string]any{},
		}

		if err := g.writeEvent(w, ev); err != nil {
			return err
		}
	}

	for _, sample := range g.actionCountSamples {
		ev := actionCountEvent(sample)
		if err := g.writeEvent(w, &ev); err != nil {
			return err
		}
	}

	for _, sample := range g.systemNetworkUploadSamples {
		ev := systemNetworkUploadEvent(sample)
		if err := g.writeEvent(w, &ev); err != nil {
			return err
		}
	}

	for _, sample := range g.loadAverageSamples {
		ev := loadAverageEvent(sample)
		if err := g.writeEvent(w, &ev); err != nil {
			return err
		}
	}

	for _, sample := range g.systemCPUUsageSamples {
		ev := systemCPUUsageEvent(sample)
		if err := g.writeEvent(w, &ev); err != nil {
			return err
		}
	}

	// TODO: ninja CPU

	for _, sample := range g.systemMemoryUsageSamples {
		ev := systemMemoryUsageEvent(sample)
		if err := g.writeEvent(w, &ev); err != nil {
			return err
		}
	}

	for _, sample := range g.systemNetworkDownloadSamples {
		ev := systemNetworkDownloadEvent(sample)
		if err := g.writeEvent(w, &ev); err != nil {
			return err
		}
	}

	for _, sample := range g.memoryUsageSamples {
		ev := memoryUsageEvent(sample)
		if err := g.writeEvent(w, &ev); err != nil {
			return err
		}
	}

	// Close the events list and the outer profile object.
	if _, err := io.WriteString(w, "]}"); err != nil {
		return statuserr.WrapError(err, "write response")
	}
	return nil
}
