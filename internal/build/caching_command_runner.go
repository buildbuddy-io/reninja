package build

import (
	"bytes"
	"context"
	"math"
	"os"
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
	"github.com/buildbuddy-io/reninja/internal/jobserver"
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

const (
	remoteCacheRunner = "remote-cache"
	localRunner       = "local"
)

type CachingCommandRunner interface {
	CommandRunner
	CacheResult(*spawn.Result, []*graph.Node) error
	WaitForUploads() error
}

type RemoteCachingCommandRunner struct {
	config      *build_config.Config
	jobserver   jobserver.Client
	mu          *sync.Mutex
	activeEdges []*activeEdgeState

	context    context.Context
	cancel     context.CancelFunc
	uploader   *filetransfer.Uploader
	downloader *filetransfer.Downloader

	group *errgroup.Group
}

type activeEdgeState struct {
	edge           *graph.Edge
	subprocess     *subprocess.Subprocess
	finishedResult chan *spawn.Result
	executing      atomic.Bool
}

func NewRemoteCachingCommandRunner(config *build_config.Config, jobserver jobserver.Client) *RemoteCachingCommandRunner {
	if filetransfer.DefaultUploader() == nil || filetransfer.DefaultDownloader() == nil {
		util.Fatalf("--cache requires --remote_cache to be set")
	}
	ctx, cancelFunc := context.WithCancel(context.TODO())

	extraHeaders := remote_headers.GetPairs()
	if len(extraHeaders) > 1 {
		ctx = metadata.AppendToOutgoingContext(ctx, extraHeaders...)
	}

	group, _ := errgroup.WithContext(ctx)
	return &RemoteCachingCommandRunner{
		config:      config,
		jobserver:   jobserver,
		mu:          &sync.Mutex{},
		activeEdges: make([]*activeEdgeState, 0),

		cancel:     cancelFunc,
		context:    ctx,
		uploader:   filetransfer.DefaultUploader(),
		downloader: filetransfer.DefaultDownloader(),

		group: group,
	}
}

func (r *RemoteCachingCommandRunner) ClearJobTokens() {
	if r.jobserver != nil {
		for _, edge := range r.GetActiveEdges() {
			r.jobserver.Release(edge.JobSlot())
		}
	}
}

func (r *RemoteCachingCommandRunner) GetActiveEdges() []*graph.Edge {
	// returns number of inflight edges (running + uncollected)
	r.mu.Lock()
	active := make([]*graph.Edge, len(r.activeEdges))
	for i, edgeState := range r.activeEdges {
		active[i] = edgeState.edge
	}
	r.mu.Unlock()

	return active
}

func (r *RemoteCachingCommandRunner) Abort() {
	r.cancel()
	r.ClearJobTokens()
}

func (r *RemoteCachingCommandRunner) CanRunMore() int {
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

func assembleCommand(edge *graph.Edge) (*repb.Command, error) {
	splitCommand, err := shlex.Split(edge.EvaluateCommand(false))
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

func encodeDyndepPaths(nodes []*graph.Node) string {
	paths := make([]string, len(nodes))
	for i, n := range nodes {
		paths[i] = n.Path()
	}
	return strings.Join(paths, "\x00")
}

func decodeDyndepPaths(encoded string) []string {
	if encoded == "" {
		return nil
	}
	return strings.Split(encoded, "\x00")
}

func extractPaths(nodes []*graph.Node) []string {
	paths := make([]string, len(nodes))
	for i, node := range nodes {
		paths[i] = node.Path()
	}
	slices.Sort(paths)
	return slices.Compact(paths)
}

func (r *RemoteCachingCommandRunner) assembleAction(ctx context.Context, cmd *repb.Command, inputs []string) (*repb.Action, filetransfer.FlattenedTree, error) {
	files := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if _, err := os.Stat(input); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, nil, err
		}
		files = append(files, input)
	}
	inputRootDigest, flattenedTree, err := r.uploader.HashDirectoryTree(files)
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

func (r *RemoteCachingCommandRunner) fetchOutputsAndResult(ctx context.Context, actionResult *repb.ActionResult, edge *graph.Edge) (*spawn.Result, error) {
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

	var output string
	if len(actionResult.StdoutRaw) > 0 {
		output = string(actionResult.StdoutRaw)
	} else if actionResult.GetStdoutDigest() != nil {
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
	return &spawn.Result{
		Status:       exit_status.ExitStatusType(actionResult.GetExitCode()),
		Output:       output,
		Edge:         edge,
		Runner:       remoteCacheRunner,
		CacheHit:     true,
		Context:      ctx,
		Outputs:      actionResult.GetOutputFiles(),
		StdoutDigest: actionResult.GetStdoutDigest(),
	}, nil
}

// isDepsFileResult returns true if the ActionResult is a pointer to another action
// (contains dep paths but no actual outputs).
func isDepsFileResult(ar *repb.ActionResult) bool {
	return len(ar.GetOutputFiles()) == 0 && !digest.IsEmptyHash(ar.GetStdoutDigest(), filetransfer.DigestFunction)
}

// extractDepPathsFromPointer extracts dynamic dep paths from a pointer ActionResult.
func (r *RemoteCachingCommandRunner) extractDepPathsFromPointer(ctx context.Context, ar *repb.ActionResult) ([]string, error) {
	instanceName := remote_flags.RemoteInstanceName()
	digestFunction := filetransfer.DigestFunction

	var encoded string
	if len(ar.StdoutRaw) > 0 {
		encoded = string(ar.StdoutRaw)
	} else if !digest.IsEmptyHash(ar.GetStdoutDigest(), digestFunction) {
		buf := &bytes.Buffer{}
		casDigest := digest.NewCASResourceName(ar.GetStdoutDigest(), instanceName, digestFunction)
		if err := r.downloader.GetBlob(ctx, casDigest, buf); err != nil {
			return nil, err
		}
		encoded = buf.String()
	}

	return decodeDyndepPaths(encoded), nil
}

func (r *RemoteCachingCommandRunner) assembleAndHashAction(ctx context.Context, edge *graph.Edge) (*repb.Action, filetransfer.FlattenedTree, error) {
	defer span.Record(ctx, "MerkleTreeComputer.buildForSpawn")()
	cmd, err := assembleCommand(edge)
	if err != nil {
		return nil, nil, err
	}

	inputs := edge.NonOrderOnlyInputs()
	return r.assembleAction(ctx, cmd, extractPaths(inputs))
}

func (r *RemoteCachingCommandRunner) StartCommand(edge *graph.Edge) error {
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

	// Compute the action for current known inputs.
	// If deps have been loaded from .ninja_deps, this will include them.
	// If not, this will only include manifest inputs.
	action, _, err := r.assembleAndHashAction(ctx, edge)
	if err != nil {
		return err
	}

	makeFailureResult := func(err error) *spawn.Result {
		return &spawn.Result{
			Edge:    edge,
			Status:  exit_status.ExitFailure,
			Output:  err.Error(),
			Context: ctx,
		}
	}

	go func() {
		res, lookupErr := r.downloadCompletedEdge(ctx, action, edge)

		if lookupErr == nil && res != nil {
			edgeState.finishedResult <- res
			return
		}

		doneExecutingFn := span.Record(ctx, "subprocess.run")
		command := edge.EvaluateCommand(false)
		subproc, err := subprocess.NewSubprocess(command, edge.UseConsole())
		if err != nil {
			doneExecutingFn()
			edgeState.finishedResult <- makeFailureResult(err)
			return
		}

		edgeState.executing.Store(true)
		exitCode := subproc.Finish()
		edgeState.executing.Store(false)
		output := subproc.GetOutput()
		doneExecutingFn()

		var uploadedOutputs []*repb.OutputFile
		var stdoutDigest *repb.Digest
		if exitCode == exit_status.ExitSuccess {
			// Upload the outputs (no action result) of this action.
			outputFiles, stdoutDigest, err := r.uploadEdgeOutputs(ctx, edge, output)
			if err != nil {
				edgeState.finishedResult <- makeFailureResult(err)
				return
			}
			uploadedOutputs = outputFiles
			stdoutDigest = stdoutDigest
		}

		edgeState.finishedResult <- &spawn.Result{
			Edge:         edge,
			Status:       exitCode,
			Output:       output,
			Runner:       localRunner,
			CacheHit:     false,
			Context:      ctx,
			Outputs:      uploadedOutputs,
			StdoutDigest: stdoutDigest,
		}
	}()

	return nil
}

func (r *RemoteCachingCommandRunner) CacheResult(result *spawn.Result, depsNodes []*graph.Node) error {
	if result.Status != exit_status.ExitSuccess {
		return nil
	}
	if result.Runner == remoteCacheRunner {
		return nil
	}
	ctx := result.Context

	r.group.Go(func() error {
		return r.uploadActionResult(ctx, result, depsNodes)
	})
	return nil
}

func (r *RemoteCachingCommandRunner) WaitForUploads() error {
	return r.group.Wait()
}

func (r *RemoteCachingCommandRunner) downloadCompletedEdge(ctx context.Context, action *repb.Action, edge *graph.Edge) (*spawn.Result, error) {
	defer span.Record(ctx, "remote output download")()
	actionResult, err := r.downloader.DownloadActionResult(ctx, action)
	if err != nil {
		return nil, err
	}
	if actionResult == nil || actionResult.GetExitCode() != 0 {
		return nil, statuserr.NotFoundError("ActionResult not found")
	}

	if isDepsFileResult(actionResult) {
		dynamicDepPaths, err := r.extractDepPathsFromPointer(ctx, actionResult)
		if err != nil {
			return nil, err
		}

		allInputPaths := append(extractPaths(edge.StaticInputs()), dynamicDepPaths...)

		// Compute full action with all inputs and follow the pointer if
		// one is found.
		cmd, err := assembleCommand(edge)
		if err != nil {
			return nil, err
		}
		fullAction, _, err := r.assembleAction(ctx, cmd, allInputPaths)
		if err != nil {
			return nil, err
		}
		actionResult, err = r.downloader.DownloadActionResult(ctx, fullAction)
		if err != nil {
			return nil, err
		}
		if actionResult == nil || actionResult.GetExitCode() != 0 {
			return nil, statuserr.NotFoundError("ActionResult not found (after following pointer)")
		}
		return r.fetchOutputsAndResult(ctx, actionResult, edge)
	}

	return r.fetchOutputsAndResult(ctx, actionResult, edge)
}

// uploadEdgeOutputs uploads the outputs of a completed edge to the CAS.
// It does not upload any action result pointing to these outputs, that
// should happen seperately.
func (r *RemoteCachingCommandRunner) uploadEdgeOutputs(ctx context.Context, edge *graph.Edge, output string) ([]*repb.OutputFile, *repb.Digest, error) {
	defer span.Record(ctx, "upload outputs")()
	instanceName := remote_flags.RemoteInstanceName()
	digestFunction := filetransfer.DigestFunction

	ul := cachetools.NewBatchCASUploader(ctx, r.uploader, r.uploader, instanceName, digestFunction)
	outputFiles := make([]*repb.OutputFile, 0, len(edge.Outputs()))

	for _, output := range edge.Outputs() {
		fi, err := os.Stat(output.Path())
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, nil, err
		}
		d, err := ul.UploadFile(output.Path())
		if err != nil {
			return nil, nil, err
		}
		outputFiles = append(outputFiles, &repb.OutputFile{
			Path:         output.Path(),
			Digest:       d,
			IsExecutable: cachetools.IsExecutable(fi),
		})
	}

	// Upload stdout.
	stdoutDigest, err := ul.UploadBlob([]byte(output))
	if err != nil {
		return nil, nil, err
	}
	if err := ul.Wait(); err != nil {
		return nil, nil, err
	}
	return outputFiles, stdoutDigest, nil
}

func (r *RemoteCachingCommandRunner) uploadActionResult(ctx context.Context, result *spawn.Result, depsNodes []*graph.Node) error {
	instanceName := remote_flags.RemoteInstanceName()
	digestFunction := filetransfer.DigestFunction

	edge := result.Edge
	cmd, err := assembleCommand(edge)
	if err != nil {
		return err
	}

	setActionResult := func(inputs []*graph.Node, actionResult *repb.ActionResult) error {
		stopMerkleTracing := span.Record(ctx, "MerkleTreeComputer.buildForSpawn")
		action, _, err := r.assembleAction(ctx, cmd, extractPaths(inputs))
		stopMerkleTracing()

		defer span.Record(ctx, "upload action result")()
		actionDigest, err := digest.ComputeForMessage(action, digestFunction)
		if err != nil {
			return err
		}

		acrn := digest.NewACResourceName(actionDigest, instanceName, digestFunction)
		return r.uploader.UploadActionResult(ctx, acrn, actionResult)
	}

	uploadActionResultReference := func(staticInputs, dynamicInputs []*graph.Node) error {
		stopUploadOutputs := span.Record(ctx, "upload outputs")
		encoded := encodeDyndepPaths(dynamicInputs)
		blobdigest, err := r.uploader.UploadInMemoryBlob(ctx, strings.NewReader(encoded))
		stopUploadOutputs()
		if err != nil {
			return err
		}

		ar := &repb.ActionResult{
			ExitCode:     0,
			StdoutDigest: blobdigest.GetDigest(),
		}
		return setActionResult(staticInputs, ar)
	}

	uploadFullActionResult := func(inputs []*graph.Node) error {
		ar := &repb.ActionResult{
			ExitCode:    int32(result.Status),
			OutputFiles: result.Outputs,
		}
		return setActionResult(inputs, ar)
	}

	// Cases:
	//  1) no dynamic inputs, some discovered -> upload ptr, upload full
	//  2) no dynamic inputs, none discovered -> ----------, upload full
	//  3) dynamic inputs, none discovered    -> upload ptr, -----------
	//  4) dynamic inputs, some discovered    -> upload ptr, -----------

	if len(edge.DynamicInputs()) == 0 && len(depsNodes) > 0 {
		manifestOnlyInputs := edge.StaticInputs()
		allInputs := slices.Concat(manifestOnlyInputs, depsNodes)
		if err := uploadActionResultReference(manifestOnlyInputs, allInputs); err != nil {
			return err
		}
		if err := uploadFullActionResult(allInputs); err != nil {
			return err
		}
	} else if len(edge.DynamicInputs()) == 0 && len(depsNodes) == 0 {
		manifestOnlyInputs := edge.StaticInputs()
		if err := uploadFullActionResult(manifestOnlyInputs); err != nil {
			return err
		}
	} else if len(edge.DynamicInputs()) > 0 && len(depsNodes) == 0 {
		manifestOnlyInputs := edge.StaticInputs()
		if err := uploadActionResultReference(manifestOnlyInputs, edge.DynamicInputs()); err != nil {
			return err
		}
	} else if len(edge.DynamicInputs()) > 0 && len(depsNodes) > 0 {
		manifestOnlyInputs := edge.StaticInputs()
		dynamicInputs := slices.Concat(edge.DynamicInputs(), depsNodes)
		allInputs := slices.Concat(manifestOnlyInputs, dynamicInputs)
		if err := uploadActionResultReference(manifestOnlyInputs, dynamicInputs); err != nil {
			return err
		}
		if err := uploadFullActionResult(allInputs); err != nil {
			return err
		}
	} else {
		util.Fatal("Unable to cache result: this should not happen.")
	}
	return nil
}

func (r *RemoteCachingCommandRunner) WaitForCommand() *spawn.Result {
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
