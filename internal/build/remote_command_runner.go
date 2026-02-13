package build

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/buildbuddy-io/reninja/internal/build_config"
	"github.com/buildbuddy-io/reninja/internal/cachetools"
	"github.com/buildbuddy-io/reninja/internal/digest"
	"github.com/buildbuddy-io/reninja/internal/exit_status"
	"github.com/buildbuddy-io/reninja/internal/filetransfer"
	"github.com/buildbuddy-io/reninja/internal/graph"
	"github.com/buildbuddy-io/reninja/internal/include_scanner"
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
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/metadata"

	repb "github.com/buildbuddy-io/reninja/genproto/remote_execution"
)

type RemoteCommandRunner struct {
	config         *build_config.Config
	jobserver      jobserver.Client
	mu             *sync.Mutex
	activeEdges    []*activeEdgeState
	includeScanner *include_scanner.Scanner

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
		config:         config,
		jobserver:      jobserver,
		mu:             &sync.Mutex{},
		activeEdges:    make([]*activeEdgeState, 0),
		includeScanner: include_scanner.New(),

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
	absoluteMode := strings.Contains(command, project_root.Root())
	args := []string{"sh", "-c", command}

	workingDir := project_root.WorkingDirectory()
	cmdProto := &repb.Command{
		Arguments:        args,
		WorkingDirectory: workingDir,
		Platform:         &repb.Platform{},
	}
	//fmt.Printf("workingDir set to %q\n", cmdProto.WorkingDirectory)

	// If the command references absolute paths, set execroot-path so they
	// resolve correctly on the remote executor.
	if absoluteMode {
		cmdProto.Platform.Properties = append(cmdProto.Platform.Properties, &repb.Platform_Property{
			Name: "execroot-path", Value: project_root.Root(),
		})
	}

	if img := remote_flags.ContainerImage(); img != "" {
		cmdProto.Platform.Properties = append(cmdProto.Platform.Properties, &repb.Platform_Property{
			Name: "container-image", Value: "docker://" + img,
		})
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	for _, output := range edge.Outputs() {
		outputPath := output.Path()
		if filepath.IsAbs(outputPath) {
			if rel, err := filepath.Rel(cwd, outputPath); err == nil {
				outputPath = rel
			}
		}
		cmdProto.OutputPaths = append(cmdProto.OutputPaths, outputPath)
	}
	return cmdProto, nil
}

// canComputeInputs returns whether we can statically determine the minimal set
// of input files for this edge. Build artifacts (inputs produced by other
// edges) are fully described by the graph and need no discovery. Source files
// (graph leaves) are only safe when the include scanner can handle them.
// For unknown source types (scripts, etc.), we upload the entire project root.
func canComputeInputs(edge *graph.Edge) bool {
	if !remote_flags.IncludeScanning() {
		return false
	}
	for _, input := range edge.ExplicitInputs() {
		// Build artifacts are fully described by the graph.
		if input.InEdge() != nil {
			continue
		}
		// Source file — check if the include scanner can handle it.
		ext := strings.ToLower(filepath.Ext(input.Path()))
		switch ext {
		case ".c", ".cc", ".cpp", ".cxx", ".s":
			continue
		default:
			fmt.Printf("canComputeInputs: unknown source input %s\n", input.Path())
			return false
		}
	}
	return true
}

func (r *RemoteCommandRunner) assembleAndHashAction(ctx context.Context, edge *graph.Edge) (*repb.Action, *repb.Command, filetransfer.FlattenedTree, error) {
	defer span.Record(ctx, "MerkleTreeComputer.buildForSpawn")()

	var files []string

	if canComputeInputs(edge) {
		// Optimized path: compute minimal inputs from declared graph
		// inputs, include scanning, and command-referenced paths.
		inputs := edge.NonOrderOnlyInputs()
		files = make([]string, 0, len(inputs))
		for _, input := range inputs {
			files = append(files, input.Path())
		}

		command := edge.EvaluateCommand(false)

		extraFiles, err := r.includeScanner.ScanEdge(files, command)
		if err != nil {
			return nil, nil, nil, err
		}
		files = append(files, extraFiles...)

		// Ensure intermediate directories exist for absolute paths containing
		// ".." so the kernel can resolve them on the remote executor.
		files = append(files, include_scanner.ExtractIntermediateDirsFromCommand(command)...)

		// Include files referenced by absolute path in the command that aren't
		// declared as edge inputs (e.g. cmake scripts, config files).
		files = append(files, include_scanner.ExtractCommandReferencedPaths(command, project_root.Root())...)
	} else {
		util.Warningf("uploading all inputs for edge: %s\n", edge.EvaluateCommand(false))
		// Default path: upload the entire project root since we can't
		// statically determine which files the command needs.
		var err error
		files, err = project_root.WalkFiles()
		if err != nil {
			return nil, nil, nil, err
		}
	}

	inputRootDigest, flattenedTree, err := r.uploader.HashDirectoryTree(files)
	if err != nil {
		return nil, nil, nil, err
	}

	cmd, err := r.assembleCommand(edge)
	if err != nil {
		return nil, nil, nil, err
	}

	commandDigest, err := digest.ComputeForMessage(cmd, remote_flags.DigestFunction())
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
	defer span.Record(ctx, "remote output download")()
	instanceName := remote_flags.RemoteInstanceName()
	digestFunction := remote_flags.DigestFunction()
	eg, gctx := errgroup.WithContext(ctx)

	cwd, _ := os.Getwd()
	for _, outputFile := range actionResult.GetOutputFiles() {
		eg.Go(func() error {
			matchedEdgeOutput := false
			for _, output := range edge.Outputs() {
				edgePath := output.Path()
				// Generally edges have few outputs, so this is fine.
				if edgePath == outputFile.GetPath() {
					matchedEdgeOutput = true
					break
				}
				// Handle absolute edge paths that were made relative to CWD for REAPI.
				if filepath.IsAbs(edgePath) {
					if rel, err := filepath.Rel(cwd, edgePath); err == nil && rel == outputFile.GetPath() {
						matchedEdgeOutput = true
						break
					}
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
	} else if actionResult.GetStdoutDigest() != nil && !digest.IsEmptyHash(actionResult.GetStdoutDigest(), digestFunction) {
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
	if len(actionResult.StderrRaw) > 0 {
		stderr = string(actionResult.StderrRaw)
	} else if actionResult.GetStderrDigest() != nil && !digest.IsEmptyHash(actionResult.GetStderrDigest(), digestFunction) {
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
	digestFunction := remote_flags.DigestFunction()
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

		edgeState.executing.Store(true)
		defer func() {
			edgeState.executing.Store(false)
		}()

		if err := uploadActionInputs(); err != nil {
			edgeState.finishedResult <- makeFailureResult(err)
			return
		}

		rsp, err := runActionRemotely()
		if err != nil {
			edgeState.finishedResult <- makeFailureResult(err)
			return
		}
		if rsp.Err != nil {
			edgeState.finishedResult <- makeFailureResult(rsp.Err)
			return
		}
		result, err := r.fetchOutputsAndResult(ctx, rsp.ExecuteResponse.GetResult(), edge)
		if err != nil {
			edgeState.finishedResult <- makeFailureResult(err)
			return
		}
		edgeState.finishedResult <- result
	}()

	return nil
}

func (r *RemoteCommandRunner) downloadCompletedEdge(ctx context.Context, action *repb.Action, edge *graph.Edge) (*spawn.Result, error) {
	finishActionResultSpan := span.Record(ctx, "cache check")
	actionResult, err := r.downloader.DownloadActionResult(ctx, action)
	finishActionResultSpan()

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
