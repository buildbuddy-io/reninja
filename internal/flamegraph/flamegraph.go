package flamegraph

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

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
	Phase     string         `json:"ph,omitempty"`
	Timestamp int64          `json:"ts"`
	Duration  int64          `json:"dur"`
	ProcessID int64          `json:"pid,omitempty"`
	ThreadID  int64          `json:"tid,omitempty"`
	Args      map[string]any `json:"args,omitempty"`
}

// Target represents an edge.
type Target struct {
	StartTimeMillis int64
	EndTimeMillis   int64
	Targets         []string
}

type Flamegraph struct {
	targets    map[string]*Target
	wroteFirst bool
}

func New() *Flamegraph {
	return &Flamegraph{
		targets: make(map[string]*Target, 0),
	}
}

func (g *Flamegraph) RecordEdge(edge *graph.Edge, startTimeMillis, endTimeMillis int64) {
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

	// Close the events list and the outer profile object.
	if _, err := io.WriteString(w, "]}"); err != nil {
		return statuserr.WrapError(err, "write response")
	}
	return nil
}
