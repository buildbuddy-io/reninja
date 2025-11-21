package build

import (
	"bytes"
	"context"
	"log"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/buildbuddy-io/gin/internal/build_config"
	"github.com/buildbuddy-io/gin/internal/cachetools"
	"github.com/buildbuddy-io/gin/internal/digest"
	"github.com/buildbuddy-io/gin/internal/exit_status"
	"github.com/buildbuddy-io/gin/internal/filetransfer"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/jobserver"
	"github.com/buildbuddy-io/gin/internal/remote_flags"
	"github.com/buildbuddy-io/gin/internal/subprocess"
	"github.com/buildbuddy-io/gin/internal/util"

	repb "github.com/buildbuddy-io/gin/genproto/remote_execution"
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

type CachingCommandRunner struct {
	config        *build_config.Config
	subprocs      *subprocess.Set
	jobserver     jobserver.Client
	cachedEdges   chan *Result
	subprocToEdge map[*subprocess.Subprocess]*graph.Edge
	uploader      *filetransfer.Uploader
	downloader    *filetransfer.Downloader
}

func NewCachingCommandRunner(config *build_config.Config, jobserver jobserver.Client) *CachingCommandRunner {
	if filetransfer.DefaultUploader() == nil || filetransfer.DefaultDownloader() == nil {
		log.Fatalf("--cache requires --remote_cache be set")
	}
	return &CachingCommandRunner{
		config:        config,
		subprocs:      subprocess.NewSet(),
		jobserver:     jobserver,
		cachedEdges:   make(chan *Result, 100),
		subprocToEdge: make(map[*subprocess.Subprocess]*graph.Edge, 0),
		uploader:      filetransfer.DefaultUploader(),
		downloader:    filetransfer.DefaultDownloader(),
	}
}

func (r *CachingCommandRunner) ClearJobTokens() {
	if r.jobserver != nil {
		for _, edge := range r.GetActiveEdges() {
			r.jobserver.Release(edge.JobSlot())
		}
	}
}

func (r *CachingCommandRunner) GetActiveEdges() []*graph.Edge {
	return slices.Collect(maps.Values(r.subprocToEdge))
}

func (r *CachingCommandRunner) Abort() {
	r.ClearJobTokens()
	r.subprocs.Clear()
}

func (r *CachingCommandRunner) CanRunMore() int {
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

func hashCommand(edge *graph.Edge) (*digest.ACResourceName, error) {
	d, err := digest.Compute(strings.NewReader(edge.EvaluateCommand(false)), filetransfer.DigestFunction)
	if err != nil {
		return nil, err
	}
	return digest.NewACResourceName(d, remote_flags.RemoteInstanceName(), filetransfer.DigestFunction), nil
}

func (r *CachingCommandRunner) fetchOutputsAndResult(actionResult *repb.ActionResult, edge *graph.Edge) (*Result, error) {
	ctx := context.TODO()
	instanceName := remote_flags.RemoteInstanceName()
	digestFunction := filetransfer.DigestFunction
	for _, outputFile := range actionResult.GetOutputFiles() {
		outputPath := outputFile.GetPath()
		parentDir := filepath.Dir(outputPath)
		if err := os.MkdirAll(parentDir, 0755); err != nil {
			return nil, err
		}
		casDigest := digest.NewCASResourceName(outputFile.GetDigest(), instanceName, digestFunction)
		f, err := os.Create(outputPath)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		if err := r.downloader.GetBlob(ctx, casDigest, f); err != nil {
			return nil, err
		}
		if outputFile.GetIsExecutable() {
			if err := os.Chmod(outputPath, 0755); err != nil {
				return nil, err
			}
		}
	}

	var output string
	if !digest.IsEmptyHash(actionResult.GetStdoutDigest(), digestFunction) {
		buf := &bytes.Buffer{}
		casDigest := digest.NewCASResourceName(actionResult.GetStdoutDigest(), instanceName, digestFunction)
		if err := r.downloader.GetBlob(ctx, casDigest, buf); err != nil {
			return nil, err
		}
		output = buf.String()
	}
	return &Result{
		Status: exit_status.ExitStatusType(actionResult.GetExitCode()),
		Output: output,
		Edge:   edge,
	}, nil
}

func (r *CachingCommandRunner) StartCommand(edge *graph.Edge) error {
	// Try to get the cached command, if it exists, we can skip execution.
	acrn, err := hashCommand(edge)
	if err != nil {
		return err
	}
	actionResult, err := r.downloader.GetActionResult(context.TODO(), acrn)
	if err == nil && actionResult != nil {
		res, err := r.fetchOutputsAndResult(actionResult, edge)
		if err == nil {
			r.cachedEdges <- res
			return nil
		}
	}

	command := edge.EvaluateCommand(false)
	subproc, err := r.subprocs.Add(command, edge.UseConsole())
	if err != nil {
		return err
	}
	r.subprocToEdge[subproc] = edge
	return nil
}

func (r *CachingCommandRunner) uploadCompletedEdge(result *Result) error {
	acrn, err := hashCommand(result.Edge)
	if err != nil {
		return err
	}

	ctx := context.TODO()
	ar := &repb.ActionResult{
		ExitCode: int32(result.Status),
	}

	for _, out := range result.Edge.Outputs() {
		fi, err := os.Stat(out.Path())
		if err != nil {
			return err
		}
		d, err := r.uploader.UploadFile(ctx, out.Path())
		ar.OutputFiles = append(ar.OutputFiles, &repb.OutputFile{
			Path:         out.Path(),
			Digest:       d.GetDigest(),
			IsExecutable: cachetools.IsExecutable(fi),
		})
	}
	stdout, err := r.uploader.UploadInMemoryBlob(context.TODO(), strings.NewReader(result.Output))
	if err != nil {
		return err
	}
	ar.StdoutDigest = stdout.GetDigest()
	return r.uploader.UploadActionResult(ctx, acrn, ar)
}

func (r *CachingCommandRunner) WaitForCommand() *Result {
	var subproc *subprocess.Subprocess

	select {
	case result := <-r.cachedEdges:
		return result
	default:
		break
	}

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

	if err := r.uploadCompletedEdge(result); err != nil {
		util.Warningf("error uploading cache result: %s", err)
	}
	delete(r.subprocToEdge, subproc)
	return result
}
