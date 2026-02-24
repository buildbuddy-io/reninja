package build

import (
	"context"
	"maps"
	"math"
	"slices"

	"github.com/buildbuddy-io/reninja/internal/build_config"
	"github.com/buildbuddy-io/reninja/internal/exit_status"
	"github.com/buildbuddy-io/reninja/internal/graph"
	"github.com/buildbuddy-io/reninja/internal/jobserver"
	"github.com/buildbuddy-io/reninja/internal/remote_flags"
	"github.com/buildbuddy-io/reninja/internal/span"
	"github.com/buildbuddy-io/reninja/internal/spawn"
	"github.com/buildbuddy-io/reninja/internal/subprocess"
	"github.com/buildbuddy-io/reninja/internal/util"
)

type CommandRunner interface {
	CanRunMore() int
	StartCommand(edge *graph.Edge) error
	WaitForCommand() *spawn.Result
	GetActiveEdges() []*graph.Edge
	Abort()
	ClearJobTokens()
}

// CancellableCommandRunner is an optional interface that command runners
// can implement to support cancelling in-flight work without a full abort.
// This is used to stop pending remote actions promptly when the failure
// budget is exhausted, rather than waiting for all of them to complete.
type CancellableCommandRunner interface {
	Cancel()
}

type DryCommandRunner struct {
	finished []*graph.Edge
}

func NewDryCommandRunner() *DryCommandRunner {
	return &DryCommandRunner{}
}

// CanRunMore always returns true for dry run
func (d *DryCommandRunner) CanRunMore() int {
	return math.MaxInt
}

func (r *DryCommandRunner) ClearJobTokens() {}

// StartCommand simulates starting a command
func (d *DryCommandRunner) StartCommand(edge *graph.Edge) error {
	d.finished = append(d.finished, edge)
	return nil
}

func (d *DryCommandRunner) WaitForCommand() *spawn.Result {
	if len(d.finished) == 0 {
		return nil
	}

	front := d.finished[0]
	d.finished = d.finished[1:]

	r := &spawn.Result{
		Status:   exit_status.ExitSuccess,
		Edge:     front,
		Runner:   "local",
		CacheHit: false,
		Context:  span.BeginTracing(context.TODO()),
	}
	return r
}

func (d *DryCommandRunner) GetActiveEdges() []*graph.Edge {
	return nil
}

func (d *DryCommandRunner) Abort() {}

type RealCommandRunner struct {
	config        *build_config.Config
	subprocs      *subprocess.Set
	jobserver     jobserver.Client
	subprocToEdge map[*subprocess.Subprocess]*graph.Edge
}

func NewRealCommandRunner(config *build_config.Config, jobserver jobserver.Client) CommandRunner {
	if remote_flags.EnableCache() && remote_flags.EnableExec() {
		return NewRemoteCommandRunner(config, jobserver)
	}
	if remote_flags.EnableCache() {
		return NewRemoteCachingCommandRunner(config, jobserver)
	}
	return &RealCommandRunner{
		config:        config,
		subprocs:      subprocess.NewSet(),
		jobserver:     jobserver,
		subprocToEdge: make(map[*subprocess.Subprocess]*graph.Edge, 0),
	}
}

func (r *RealCommandRunner) ClearJobTokens() {
	if r.jobserver != nil {
		for _, edge := range r.GetActiveEdges() {
			r.jobserver.Release(edge.JobSlot())
		}
	}
}

func (r *RealCommandRunner) GetActiveEdges() []*graph.Edge {
	return slices.Collect(maps.Values(r.subprocToEdge))
}

func (r *RealCommandRunner) Abort() {
	r.ClearJobTokens()
	r.subprocs.Clear()
}

func (r *RealCommandRunner) CanRunMore() int {
	subprocNumber := len(r.subprocs.Running()) + len(r.subprocs.Finished())

	capacity := r.config.Parallelism - subprocNumber

	if r.jobserver != nil {
		// When a jobserver token pool is used, make the
		// capacity infinite, and let FindWork() limit jobs
		// through token acquisitions instead.
		capacity = math.MaxInt
	}

	if r.config.MaxLoadAverage > 0.0 {
		loadCapacity := int(r.config.MaxLoadAverage - util.GetLoadAverage())
		if loadCapacity < capacity {
			capacity = loadCapacity
		}
	}

	if capacity < 0 {
		capacity = 0
	}

	if capacity == 0 && len(r.subprocs.Running()) == 0 {
		// Ensure that we make progress.
		capacity = 1
	}

	return capacity
}

func (r *RealCommandRunner) StartCommand(edge *graph.Edge) error {
	command := edge.EvaluateCommand(false)
	subproc, err := r.subprocs.Add(command, edge.UseConsole())
	if err != nil {
		return err
	}
	r.subprocToEdge[subproc] = edge
	return nil
}

func (r *RealCommandRunner) WaitForCommand() *spawn.Result {
	var subproc *subprocess.Subprocess
	for ; subproc == nil; subproc = r.subprocs.NextFinished() {
		interrupted := r.subprocs.DoWork()
		if interrupted {
			return nil
		}
	}

	result := &spawn.Result{
		Status:   subproc.Finish(),
		Output:   subproc.GetOutput(),
		Edge:     r.subprocToEdge[subproc],
		Runner:   "local",
		CacheHit: false,
		Context:  span.BeginTracing(context.TODO()),
	}

	delete(r.subprocToEdge, subproc)
	return result
}
