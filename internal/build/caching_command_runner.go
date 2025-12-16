package build

import (
	"bytes"
	"context"
	"log"
	"math"
	"os"
	"slices"
	"sync"

	"github.com/buildbuddy-io/gin/internal/build_config"
	"github.com/buildbuddy-io/gin/internal/cachetools"
	"github.com/buildbuddy-io/gin/internal/digest"
	"github.com/buildbuddy-io/gin/internal/exit_status"
	"github.com/buildbuddy-io/gin/internal/filetransfer"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/jobserver"
	"github.com/buildbuddy-io/gin/internal/remote_flags"
	"github.com/buildbuddy-io/gin/internal/request_metadata"
	"github.com/buildbuddy-io/gin/internal/statuserr"
	"github.com/buildbuddy-io/gin/internal/subprocess"
	"github.com/buildbuddy-io/gin/internal/util"
	"github.com/google/shlex"
	"golang.org/x/sync/errgroup"

	repb "github.com/buildbuddy-io/gin/genproto/remote_execution"
)

type CachingCommandRunner struct {
	config      *build_config.Config
	jobserver   jobserver.Client
	mu          *sync.Mutex
	eg          *errgroup.Group
	activeEdges []*activeEdgeState

	context    context.Context
	cancel     context.CancelFunc
	uploader   *filetransfer.Uploader
	downloader *filetransfer.Downloader
}

type activeEdgeState struct {
	edge           *graph.Edge
	subprocess     *subprocess.Subprocess
	finishedResult chan *Result
}

func NewCachingCommandRunner(config *build_config.Config, jobserver jobserver.Client) *CachingCommandRunner {
	if filetransfer.DefaultUploader() == nil || filetransfer.DefaultDownloader() == nil {
		log.Fatalf("--cache requires --remote_cache to be set")
	}
	ctx, cancelFunc := context.WithCancel(context.TODO())
	eg, _ := errgroup.WithContext(ctx)

	return &CachingCommandRunner{
		config:      config,
		jobserver:   jobserver,
		mu:          &sync.Mutex{},
		eg:          eg,
		activeEdges: make([]*activeEdgeState, 0),

		cancel:     cancelFunc,
		context:    ctx,
		uploader:   filetransfer.DefaultUploader(),
		downloader: filetransfer.DefaultDownloader(),
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
	// returns number of inflight edges (running + uncollected)
	r.mu.Lock()
	active := make([]*graph.Edge, len(r.activeEdges))
	for i, edgeState := range r.activeEdges {
		active[i] = edgeState.edge
	}
	r.mu.Unlock()

	return active
}

func (r *CachingCommandRunner) Abort() {
	r.cancel()
	r.ClearJobTokens()
}

func (r *CachingCommandRunner) CanRunMore() int {
	// returns number of running edges + number of uncollected edges.
	subprocNumber := len(r.activeEdges)
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

func assembleCommand(edge *graph.Edge) (*repb.Command, error) {
	command := edge.EvaluateCommand(false)
	splitCommand, err := shlex.Split(command)
	if err != nil {
		return nil, err
	}
	cmdProto := &repb.Command{
		Arguments: splitCommand,
	}
	for _, output := range edge.Outputs() {
		cmdProto.OutputPaths = append(cmdProto.OutputPaths, output.Path())
	}
	// TODO(tylerw): maybe hash and include other stuff here???
	return cmdProto, nil
}

func (r *CachingCommandRunner) assembleAndHashAction(ctx context.Context, edge *graph.Edge) (*repb.Action, filetransfer.FlattenedTree, error) {
	cmd, err := assembleCommand(edge)
	if err != nil {
		return nil, nil, err
	}

	files := make([]string, len(edge.Inputs()))
	for i, input := range edge.Inputs() {
		files[i] = input.Path()
	}
	inputRootDigest, flattenedTree, err := r.uploader.HashDirectoryTree(ctx, files)
	if err != nil {
		return nil, nil, err
	}
	commandDigest, err := digest.ComputeForMessage(cmd, filetransfer.DigestFunction)
	if err != nil {
		return nil, nil, err
	}
	action := &repb.Action{
		CommandDigest:   commandDigest,
		InputRootDigest: inputRootDigest,
	}
	return action, flattenedTree, nil
}

func (r *CachingCommandRunner) fetchOutputsAndResult(ctx context.Context, actionResult *repb.ActionResult, edge *graph.Edge) (*Result, error) {
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

	var output string
	if len(actionResult.StdoutRaw) > 0 {
		output = string(actionResult.StdoutRaw)
	} else {
		if !digest.IsEmptyHash(actionResult.GetStdoutDigest(), digestFunction) {
			eg.Go(func() error {
				buf := &bytes.Buffer{}
				casDigest := digest.NewCASResourceName(actionResult.GetStdoutDigest(), instanceName, digestFunction)
				if err := r.downloader.GetBlob(gctx, casDigest, buf); err != nil {
					return err
				}
				output = buf.String()
				return nil
			})
		}
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return &Result{
		Status: exit_status.ExitStatusType(actionResult.GetExitCode()),
		Output: output,
		Edge:   edge,
	}, nil
}

func (r *CachingCommandRunner) StartCommand(edge *graph.Edge) error {
	ctx := request_metadata.AttachCacheRequestMetadata(r.context, edge.ActionID(), edge.ActionMnemonic(), edge.TargetLabel())
	r.eg.Go(func() error {
		edgeState := &activeEdgeState{
			edge:           edge,
			finishedResult: make(chan *Result),
		}
		r.mu.Lock()
		r.activeEdges = append(r.activeEdges, edgeState)
		r.mu.Unlock()

		action, flattenedTree, err := r.assembleAndHashAction(ctx, edge)
		if err != nil {
			return err
		}
		if res, err := r.downloadCompletedEdge(ctx, action, edge); err == nil {
			edgeState.finishedResult <- res
			return nil
		}

		command := edge.EvaluateCommand(false)
		subproc, err := subprocess.NewSubprocess(command, edge.UseConsole())
		if err != nil {
			return err
		}

		exitCode := subproc.Finish()
		output := subproc.GetOutput()

		if err := r.uploadCompletedEdge(edge, exitCode, output, action, flattenedTree); err != nil {
			return err
		}

		edgeState.finishedResult <- &Result{
			Edge:   edge,
			Status: exitCode,
			Output: output,
		}

		return nil
	})
	return nil
}

func (r *CachingCommandRunner) downloadCompletedEdge(ctx context.Context, action *repb.Action, edge *graph.Edge) (*Result, error) {
	instanceName := remote_flags.RemoteInstanceName()
	digestFunction := filetransfer.DigestFunction

	d, err := digest.ComputeForMessage(action, digestFunction)
	if err != nil {
		return nil, err
	}

	acrn := digest.NewACResourceName(d, instanceName, digestFunction)
	actionResult, err := r.downloader.DownloadActionResult(ctx, acrn)
	if err == nil && actionResult != nil && actionResult.GetExitCode() == 0 {
		return r.fetchOutputsAndResult(ctx, actionResult, edge)
	}
	return nil, statuserr.NotFoundError("ActionResult not found")
}

func (r *CachingCommandRunner) uploadCompletedEdge(edge *graph.Edge, exitCode exit_status.ExitStatusType, output string, action *repb.Action, tree filetransfer.FlattenedTree) error {
	// Skip uploading failed actions.
	if exitCode != exit_status.ExitSuccess {
		return nil
	}

	ctx := request_metadata.AttachCacheRequestMetadata(r.context, edge.ActionID(), edge.ActionMnemonic(), edge.TargetLabel())
	ar := &repb.ActionResult{
		ExitCode:    int32(exitCode),
		OutputFiles: make([]*repb.OutputFile, len(edge.Outputs())),
	}

	instanceName := remote_flags.RemoteInstanceName()
	digestFunction := filetransfer.DigestFunction
	actionDigest, err := digest.ComputeForMessage(action, digestFunction)
	if err != nil {
		return err
	}

	ul := cachetools.NewBatchCASUploader(ctx, r.uploader, r.uploader, instanceName, digestFunction)

	// Upload inputs
	if err := filetransfer.UploadDirectoryTreeToCAS(ul, tree); err != nil {
		return err
	}

	// Upload outputs
	for i, output := range edge.Outputs() {
		fi, err := os.Stat(output.Path())
		if err != nil {
			return err
		}
		d, err := ul.UploadFile(output.Path())
		if err != nil {
			return err
		}
		ar.OutputFiles[i] = &repb.OutputFile{
			Path:         output.Path(),
			Digest:       d,
			IsExecutable: cachetools.IsExecutable(fi),
		}
	}

	// Upload stdout
	ar.StdoutDigest, err = ul.UploadBlob([]byte(output))
	if err != nil {
		return err
	}
	if err := ul.Wait(); err != nil {
		return err
	}
	acrn := digest.NewACResourceName(actionDigest, instanceName, digestFunction)

	// Upload the actual action result.
	return r.uploader.UploadActionResult(ctx, acrn, ar)
}

func (r *CachingCommandRunner) WaitForCommand() *Result {

	for {
		r.mu.Lock()
		edges := make([]*activeEdgeState, len(r.activeEdges))
		copy(edges, r.activeEdges)
		r.mu.Unlock()

		for i := 0; i < len(edges); i++ {
			if res, ok := <-edges[i].finishedResult; ok {
				r.mu.Lock()
				r.activeEdges = slices.DeleteFunc(r.activeEdges, func(n *activeEdgeState) bool {
					return n == edges[i]
				})
				r.mu.Unlock()
				return res
			}
			if subprocess.Interrupted() {
				r.cancel()
				return nil
			}
		}
	}
}
