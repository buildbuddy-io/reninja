package build

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/buildbuddy-io/reninja/internal/build_config"
	"github.com/buildbuddy-io/reninja/internal/cachetools"
	"github.com/buildbuddy-io/reninja/internal/digest"
	"github.com/buildbuddy-io/reninja/internal/exit_status"
	"github.com/buildbuddy-io/reninja/internal/filetransfer"
	"github.com/buildbuddy-io/reninja/internal/graph"
	"github.com/buildbuddy-io/reninja/internal/jobserver"
	"github.com/buildbuddy-io/reninja/internal/project_root"
	"github.com/buildbuddy-io/reninja/internal/remote_exec"
	"github.com/buildbuddy-io/reninja/internal/remote_flags"
	"github.com/buildbuddy-io/reninja/internal/remote_headers"
	"github.com/buildbuddy-io/reninja/internal/request_metadata"
	"github.com/buildbuddy-io/reninja/internal/span"
	"github.com/buildbuddy-io/reninja/internal/spawn"
	"github.com/buildbuddy-io/reninja/internal/statuserr"
	"github.com/buildbuddy-io/reninja/internal/subprocess"
	"github.com/buildbuddy-io/reninja/internal/util"
	"github.com/google/shlex"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/metadata"

	repb "github.com/buildbuddy-io/reninja/genproto/remote_execution"
)

type RemoteCommandRunner struct {
	config      *build_config.Config
	jobserver   jobserver.Client
	mu          *sync.Mutex
	activeEdges []*activeEdgeState

	context    context.Context
	cancel     context.CancelFunc
	uploader   *filetransfer.Uploader
	downloader *filetransfer.Downloader
	executor   *remote_exec.Executor
}

func NewRemoteCommandRunner(config *build_config.Config, jobserver jobserver.Client) *RemoteCommandRunner {
	if filetransfer.DefaultUploader() == nil || filetransfer.DefaultDownloader() == nil {
		util.Fatalf("--cache requires --remote_cache to be set")
	}
	if remote_exec.DefaultExecutor() == nil {
		util.Fatalf("--exec requires --remote_executor to be set")
	}

	ctx, cancelFunc := context.WithCancel(context.TODO())
	extraHeaders := remote_headers.GetPairs()
	if len(extraHeaders) > 1 {
		ctx = metadata.AppendToOutgoingContext(ctx, extraHeaders...)
	}

	return &RemoteCommandRunner{
		config:      config,
		jobserver:   jobserver,
		mu:          &sync.Mutex{},
		activeEdges: make([]*activeEdgeState, 0),

		cancel:     cancelFunc,
		context:    ctx,
		uploader:   filetransfer.DefaultUploader(),
		downloader: filetransfer.DefaultDownloader(),
		executor:   remote_exec.DefaultExecutor(),
	}
}

func (r *RemoteCommandRunner) ClearJobTokens() {
	if r.jobserver != nil {
		for _, edge := range r.GetActiveEdges() {
			r.jobserver.Release(edge.JobSlot())
		}
	}
}

func (r *RemoteCommandRunner) GetActiveEdges() []*graph.Edge {
	// returns number of inflight edges (running + uncollected)
	r.mu.Lock()
	active := make([]*graph.Edge, len(r.activeEdges))
	for i, edgeState := range r.activeEdges {
		active[i] = edgeState.edge
	}
	r.mu.Unlock()

	return active
}

func (r *RemoteCommandRunner) Abort() {
	r.cancel()
	r.ClearJobTokens()
}

func (r *RemoteCommandRunner) CanRunMore() int {
	// returns number of running edges + number of uncollected edges.
	subprocNumber := 0
	r.mu.Lock()
	for _, edgeState := range r.activeEdges {
		if edgeState.executing.Load() {
			subprocNumber += 1
		}
	}
	r.mu.Unlock()
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

	if capacity == 0 && len(r.activeEdges) == 0 {
		// Ensure that we make progress.
		capacity = 1
	}

	return capacity
}

func (r *RemoteCommandRunner) assembleCommand(edge *graph.Edge) (*repb.Command, error) {
	command := edge.EvaluateCommand(false)
	splitCommand, err := shlex.Split(command)
	if err != nil {
		return nil, err
	}
	cmdProto := &repb.Command{
		Arguments:        splitCommand,
		WorkingDirectory: project_root.WorkingDirectory(),
	}
	for _, output := range edge.Outputs() {
		cmdProto.OutputPaths = append(cmdProto.OutputPaths, output.Path())
	}
	// TODO(tylerw): maybe hash and include other stuff here???
	fmt.Fprintf(os.Stderr, "DEBUG assembleCommand: WorkingDirectory=%q Arguments=%v OutputPaths=%v\n", cmdProto.WorkingDirectory, cmdProto.Arguments, cmdProto.OutputPaths)
	return cmdProto, nil
}

func (r *RemoteCommandRunner) assembleAndHashAction(ctx context.Context, edge *graph.Edge) (*repb.Action, *repb.Command, filetransfer.FlattenedTree, error) {
	defer span.Record(ctx, "MerkleTreeComputer.buildForSpawn")()

	cmd, err := r.assembleCommand(edge)
	if err != nil {
		return nil, nil, nil, err
	}

	files := make([]string, 0, len(edge.Inputs()))
	for _, input := range edge.Inputs() {
		if _, err := os.Stat(input.Path()); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, nil, nil, err
		}
		files = append(files, input.Path())
	}
	inputRootDigest, flattenedTree, err := r.uploader.HashDirectoryTree(files)
	if err != nil {
		return nil, nil, nil, err
	}
	commandDigest, err := digest.ComputeForMessage(cmd, filetransfer.DigestFunction)
	if err != nil {
		return nil, nil, nil, err
	}
	action := &repb.Action{
		CommandDigest:   commandDigest,
		InputRootDigest: inputRootDigest,
	}
	return action, cmd, flattenedTree, nil
}

func (r *RemoteCommandRunner) fetchOutputsAndResult(ctx context.Context, actionResult *repb.ActionResult, edge *graph.Edge) (*spawn.Result, error) {
	instanceName := remote_flags.RemoteInstanceName()
	digestFunction := filetransfer.DigestFunction
	eg, gctx := errgroup.WithContext(ctx)

	for _, outputFile := range actionResult.GetOutputFiles() {
		eg.Go(func() error {
			matchedEdgeOutput := false
			for _, output := range edge.Outputs() {
				// Generally edges have few outputs, so this is fine.
				if output.Path() == outputFile.GetPath() {
					matchedEdgeOutput = true
					break
				}
			}
			if !matchedEdgeOutput {
				util.Errorf("ActionResult contained output (%s) not found in edge!", outputFile)
				return nil // Skip writing any outputs that aren't outputs of this edge.
			}

			f, err := os.Create(outputFile.GetPath())
			if err != nil {
				return err
			}
			defer f.Close()

			casDigest := digest.NewCASResourceName(outputFile.GetDigest(), instanceName, digestFunction)
			if err := r.downloader.GetBlob(gctx, casDigest, f); err != nil {
				return err
			}
			if outputFile.GetIsExecutable() {
				if err := os.Chmod(outputFile.GetPath(), 0755); err != nil {
					return err
				}
			}
			return nil
		})
	}

	stdout := ""
	stderr := ""

	if len(actionResult.StdoutRaw) > 0 {
		stdout = string(actionResult.StdoutRaw)
	} else {
		if !digest.IsEmptyHash(actionResult.GetStdoutDigest(), digestFunction) {
			eg.Go(func() error {
				buf := &bytes.Buffer{}
				casDigest := digest.NewCASResourceName(actionResult.GetStdoutDigest(), instanceName, digestFunction)
				if err := r.downloader.GetBlob(gctx, casDigest, buf); err != nil {
					return err
				}
				stdout = buf.String()
				return nil
			})
		}
	}
	if len(actionResult.StderrRaw) > 0 {
		stderr = string(actionResult.StdoutRaw)
	} else {
		if !digest.IsEmptyHash(actionResult.GetStderrDigest(), digestFunction) {
			eg.Go(func() error {
				buf := &bytes.Buffer{}
				casDigest := digest.NewCASResourceName(actionResult.GetStderrDigest(), instanceName, digestFunction)
				if err := r.downloader.GetBlob(gctx, casDigest, buf); err != nil {
					return err
				}
				stderr += buf.String()
				return nil
			})
		}
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	return &spawn.Result{
		Status:       exit_status.ExitStatusType(actionResult.GetExitCode()),
		Output:       stdout + stderr,
		Edge:         edge,
		Runner:       remoteCacheRunner,
		CacheHit:     true,
		Context:      ctx,
		Outputs:      actionResult.GetOutputFiles(),
		StdoutDigest: actionResult.GetStdoutDigest(),
	}, nil
}

func (r *RemoteCommandRunner) StartCommand(edge *graph.Edge) error {
	edgeState := &activeEdgeState{
		edge:           edge,
		finishedResult: make(chan *spawn.Result),
		executing:      atomic.Bool{},
	}
	r.mu.Lock()
	r.activeEdges = append(r.activeEdges, edgeState)
	r.mu.Unlock()

	ctx := request_metadata.AttachCacheRequestMetadata(r.context, edge.ActionID(), edge.ActionMnemonic(), edge.TargetLabel())
	ctx = span.BeginTracing(ctx)

	action, cmd, flattenedTree, err := r.assembleAndHashAction(ctx, edge)
	if err != nil {
		return err
	}

	instanceName := remote_flags.RemoteInstanceName()
	digestFunction := filetransfer.DigestFunction
	d, err := digest.ComputeForMessage(action, digestFunction)
	if err != nil {
		return err
	}
	arn := digest.NewCASResourceName(d, instanceName, digestFunction)

	makeFailureResult := func(err error) *spawn.Result {
		return &spawn.Result{
			Edge:    edge,
			Status:  exit_status.ExitFailure,
			Output:  err.Error(),
			Context: ctx,
		}
	}

	uploadActionInputs := func() error {
		defer span.Record(ctx, "upload inputs")()
		ul := cachetools.NewBatchCASUploader(ctx, r.uploader, r.uploader, instanceName, digestFunction)
		err := filetransfer.UploadDirectoryTreeToCAS(ul, flattenedTree)
		if err != nil {
			return err
		}
		_, err = ul.UploadProto(cmd)
		if err != nil {
			return err
		}
		_, err = ul.UploadProto(action)
		if err != nil {
			return err
		}
		return ul.Wait()
	}

	runActionRemotely := func() (*remote_exec.Response, error) {
		defer span.Record(ctx, "remote action execution")()
		stream, err := r.executor.Start(ctx, arn)
		if err != nil {
			return nil, err
		}
		rsp, err := remote_exec.Wait(stream)
		if err != nil {
		}
		return rsp, err
	}

	go func() {
		if res, err := r.downloadCompletedEdge(ctx, action, edge); err == nil {
			edgeState.finishedResult <- res
			return
		}

		if err := uploadActionInputs(); err != nil {
			edgeState.finishedResult <- makeFailureResult(err)
			return
		}

		rsp, err := runActionRemotely()
		if err != nil {
			edgeState.finishedResult <- makeFailureResult(err)
			return
		}

		result, err := r.fetchOutputsAndResult(ctx, rsp.ExecuteResponse.GetResult(), edge)
		if err != nil {
			edgeState.finishedResult <- makeFailureResult(err)
			return
		}
		fmt.Printf("\nFINISHED RESULT: %+v\n", result)
		edgeState.finishedResult <- result
	}()

	return nil
}

func (r *RemoteCommandRunner) downloadCompletedEdge(ctx context.Context, action *repb.Action, edge *graph.Edge) (*spawn.Result, error) {
	defer span.Record(ctx, "remote output download")()

	actionResult, err := r.downloader.DownloadActionResult(ctx, action)
	if err == nil && actionResult != nil && actionResult.GetExitCode() == 0 {
		return r.fetchOutputsAndResult(ctx, actionResult, edge)
	}
	return nil, statuserr.NotFoundError("ActionResult not found")
}

func (r *RemoteCommandRunner) WaitForCommand() *spawn.Result {
	for {
		r.mu.Lock()
		edges := make([]*activeEdgeState, len(r.activeEdges))
		copy(edges, r.activeEdges)
		r.mu.Unlock()

		for i := 0; i < len(edges); i++ {
			select {
			case res := <-edges[i].finishedResult:
				r.mu.Lock()
				r.activeEdges = slices.DeleteFunc(r.activeEdges, func(n *activeEdgeState) bool {
					return n == edges[i]
				})
				r.mu.Unlock()
				return res
			default:
				if subprocess.Interrupted() {
					r.cancel()
					return nil
				}
			}
		}
	}
}
