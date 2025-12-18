package build

import (
	"maps"
	"math"
	"slices"

	"github.com/buildbuddy-io/gin/internal/build_config"
	"github.com/buildbuddy-io/gin/internal/exit_status"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/jobserver"
	"github.com/buildbuddy-io/gin/internal/remote_flags"
	"github.com/buildbuddy-io/gin/internal/subprocess"
	"github.com/buildbuddy-io/gin/internal/util"
)

type CommandRunner interface {
	CanRunMore() int
	StartCommand(edge *graph.Edge) error
	WaitForCommand() *Result
	GetActiveEdges() []*graph.Edge
	Abort()
	ClearJobTokens()
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

func (d *DryCommandRunner) WaitForCommand() *Result {
	if len(d.finished) == 0 {
		return nil
	}

	front := d.finished[0]
	d.finished = d.finished[1:]

	r := &Result{
		Status: exit_status.ExitSuccess,
		Edge:   front,
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
	if remote_flags.EnableCache() {
		return NewCachingCommandRunner(config, jobserver)
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

func (r *RealCommandRunner) WaitForCommand() *Result {
	var subproc *subprocess.Subprocess
	for ; subproc == nil; subproc = r.subprocs.NextFinished() {
		interrupted := r.subprocs.DoWork()
		if interrupted {
			return nil
		}
	}

	result := &Result{
		Status: subproc.Finish(),
		Output: subproc.GetOutput(),
		Edge:   r.subprocToEdge[subproc],
	}

	delete(r.subprocToEdge, subproc)
	return result
}
