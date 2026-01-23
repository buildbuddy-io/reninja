package build

import (
	"bytes"
	"context"
	"fmt"
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

	bespb "github.com/buildbuddy-io/reninja/genproto/build_event_stream"
	repb "github.com/buildbuddy-io/reninja/genproto/remote_execution"
)

type CachingCommandRunner struct {
	config      *build_config.Config
	jobserver   jobserver.Client
	mu          *sync.Mutex
	activeEdges []*activeEdgeState

	context    context.Context
	cancel     context.CancelFunc
	uploader   *filetransfer.Uploader
	downloader *filetransfer.Downloader
}

type activeEdgeState struct {
	edge           *graph.Edge
	subprocess     *subprocess.Subprocess
	finishedResult chan *spawn.Result
	executing      atomic.Bool
}

func NewCachingCommandRunner(config *build_config.Config, jobserver jobserver.Client) *CachingCommandRunner {
	if filetransfer.DefaultUploader() == nil || filetransfer.DefaultDownloader() == nil {
		util.Fatalf("--cache requires --remote_cache to be set")
	}
	ctx, cancelFunc := context.WithCancel(context.TODO())

	extraHeaders := remote_headers.GetPairs()
	if len(extraHeaders) > 1 {
		ctx = metadata.AppendToOutgoingContext(ctx, extraHeaders...)
	}

	return &CachingCommandRunner{
		config:      config,
		jobserver:   jobserver,
		mu:          &sync.Mutex{},
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

// encodeDynamicDepPaths encodes dynamic dependency paths as newline-separated string.
func encodeDynamicDepPaths(nodes []*graph.Node) string {
	paths := make([]string, len(nodes))
	for i, n := range nodes {
		paths[i] = n.Path()
	}
	return strings.Join(paths, "\n")
}

// decodeDynamicDepPaths decodes dynamic dependency paths from newline-separated string.
func decodeDynamicDepPaths(encoded string) []string {
	if encoded == "" {
		return nil
	}
	return strings.Split(encoded, "\n")
}

// assembleAction creates an Action proto for the given inputs.
func (r *CachingCommandRunner) assembleAction(ctx context.Context, cmd *repb.Command, inputs []*graph.Node) (*repb.Action, filetransfer.FlattenedTree, error) {
	files := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if _, err := os.Stat(input.Path()); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, nil, err
		}
		files = append(files, input.Path())
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

// assembleAndHashAction creates an Action proto using all inputs (for backwards compatibility).
func (r *CachingCommandRunner) assembleAndHashAction(ctx context.Context, edge *graph.Edge) (*repb.Action, filetransfer.FlattenedTree, error) {
	defer span.Record(ctx, "MerkleTreeComputer.buildForSpawn")()
	cmd, err := assembleCommand(edge)
	if err != nil {
		return nil, nil, err
	}
	return r.assembleAction(ctx, cmd, edge.Inputs())
}

// assembleStaticAction creates an Action proto using only static inputs (for deps metadata lookup).
func (r *CachingCommandRunner) assembleStaticAction(ctx context.Context, edge *graph.Edge) (*repb.Action, error) {
	defer span.Record(ctx, "assembleStaticAction")()
	cmd, err := assembleCommand(edge)
	if err != nil {
		return nil, err
	}
	staticInputs := edge.StaticInputs()
	action, _, err := r.assembleAction(ctx, cmd, staticInputs)
	return action, err
}

func (r *CachingCommandRunner) fetchOutputsAndResult(ctx context.Context, actionResult *repb.ActionResult, edge *graph.Edge) (*spawn.Result, error) {
	instanceName := remote_flags.RemoteInstanceName()
	digestFunction := filetransfer.DigestFunction
	eg, gctx := errgroup.WithContext(ctx)

	actionOutputs := make([]*bespb.File, 0, len(edge.Outputs()))
	bytestreamURIPrefix := remote_flags.BytestreamURIPrefix()

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

			uri := fmt.Sprintf("%s/%s", bytestreamURIPrefix, casDigest.DownloadString())
			actionOutputs = append(actionOutputs, &bespb.File{
				Name:   outputFile.GetPath(),
				File:   &bespb.File_Uri{Uri: uri},
				Digest: outputFile.GetDigest().GetHash(),
				Length: outputFile.GetDigest().GetSizeBytes(),
			})
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
	return &spawn.Result{
		Status:   exit_status.ExitStatusType(actionResult.GetExitCode()),
		Output:   output,
		Edge:     edge,
		Runner:   "remote-cache",
		CacheHit: true,
		Outputs:  actionOutputs,
	}, nil
}

// isDepsFileResult returns true if the ActionResult is a pointer to another action
// (contains dep paths but no actual outputs).
func isDepsFileResult(ar *repb.ActionResult) bool {
	return len(ar.GetOutputFiles()) == 0 && len(ar.GetOutputDirectories()) == 0
}

// extractDepPathsFromPointer extracts dynamic dep paths from a pointer ActionResult.
func (r *CachingCommandRunner) extractDepPathsFromPointer(ctx context.Context, ar *repb.ActionResult) ([]string, error) {
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

	return decodeDynamicDepPaths(encoded), nil
}

// uploadDepsOnlyResult uploads a deps only ActionResult reference.
// The reference contains the dynamic dep paths, allowing future lookups to
// discover the full set of inputs needed to compute the actual action key and
// look up the full result of an edge.
func (r *CachingCommandRunner) uploadDepsOnlyResult(ctx context.Context, staticAction *repb.Action, dynamicInputs []*graph.Node) error {
	defer span.Record(ctx, "uploadDepsOnlyResult")()

	instanceName := remote_flags.RemoteInstanceName()
	digestFunction := filetransfer.DigestFunction

	staticActionDigest, err := digest.ComputeForMessage(staticAction, digestFunction)
	if err != nil {
		return err
	}

	encoded := encodeDynamicDepPaths(dynamicInputs)
	blobDigest, err := r.uploader.UploadInMemoryBlob(ctx, strings.NewReader(encoded))
	if err != nil {
		return err
	}

	ar := &repb.ActionResult{
		ExitCode: 0,
		StdoutDigest: blobDigest.GetDigest(),
	}

	acrn := digest.NewACResourceName(staticActionDigest, instanceName, digestFunction)
	return r.uploader.UploadActionResult(ctx, acrn, ar)
}

func (r *CachingCommandRunner) StartCommand(edge *graph.Edge) error {
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

	// Check if this edge has depfile support (i.e., may have dynamic deps)
	hasDepfile := edge.GetUnescapedDepfile() != ""

	// Compute the action for current known inputs.
	// If deps have been loaded from .ninja_deps, this will include them.
	// If not, this will only include manifest inputs.
	action, _, err := r.assembleAndHashAction(ctx, edge)
	if err != nil {
		return err
	}

	makeFailureResult := func(err error) *spawn.Result {
		return &spawn.Result{
			Edge:   edge,
			Status: exit_status.ExitFailure,
			Output: err.Error(),
			Events: span.Events(ctx),
		}
	}

	go func() {
		res, lookupErr := r.downloadCompletedEdge(ctx, action, edge)

		if lookupErr == nil && res != nil {
			res.Events = span.Events(ctx)
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

		uploadedOutputs, err := r.uploadCompletedEdge(ctx, edge, exitCode, output, action, hasDepfile)
		if err != nil {
			edgeState.finishedResult <- makeFailureResult(err)
			return
		}

		edgeState.finishedResult <- &spawn.Result{
			Edge:     edge,
			Status:   exitCode,
			Output:   output,
			Runner:   "local",
			CacheHit: false,
			Events:   span.Events(ctx),
			Outputs:  uploadedOutputs,
		}
	}()

	return nil
}

func (r *CachingCommandRunner) downloadCompletedEdge(ctx context.Context, action *repb.Action, edge *graph.Edge) (*spawn.Result, error) {
	defer span.Record(ctx, "remote output download")()

	instanceName := remote_flags.RemoteInstanceName()
	digestFunction := filetransfer.DigestFunction

	d, err := digest.ComputeForMessage(action, digestFunction)
	if err != nil {
		return nil, err
	}

	acrn := digest.NewACResourceName(d, instanceName, digestFunction)
	actionResult, err := r.downloader.DownloadActionResult(ctx, acrn)
	if err != nil {
		return nil, err
	}
	if actionResult == nil || actionResult.GetExitCode() != 0 {
		return nil, statuserr.NotFoundError("ActionResult not found")
	}

	if isDepsFileResult(actionResult) {
		fmt.Printf("looked up depfile only result\n")
		// Extract dynamic dep paths from the pointer
		dynamicDepPaths, err := r.extractDepPathsFromPointer(ctx, actionResult)
		if err != nil {
			return nil, err
		}

		// Build the full input list: current inputs + dynamic deps from pointer
		allInputs := make([]*graph.Node, 0, len(edge.Inputs())+len(dynamicDepPaths))
		allInputs = append(allInputs, edge.Inputs()...)

		// Add dynamic deps from the pointer, verifying they exist
		for _, path := range dynamicDepPaths {
			if _, err := os.Stat(path); err != nil {
				if os.IsNotExist(err) {
					return nil, statuserr.NotFoundError("dynamic dep missing: " + path)
				}
				return nil, err
			}
			allInputs = append(allInputs, graph.NewNode(path, 0))
		}

		// Compute full action with all inputs and follow the pointer
		cmd, err := assembleCommand(edge)
		if err != nil {
			return nil, err
		}
		fullAction, _, err := r.assembleAction(ctx, cmd, allInputs)
		if err != nil {
			return nil, err
		}

		fullDigest, err := digest.ComputeForMessage(fullAction, digestFunction)
		if err != nil {
			return nil, err
		}

		fullAcrn := digest.NewACResourceName(fullDigest, instanceName, digestFunction)
		actionResult, err = r.downloader.DownloadActionResult(ctx, fullAcrn)
		if err != nil {
			return nil, err
		}
		if actionResult == nil || actionResult.GetExitCode() != 0 {
			return nil, statuserr.NotFoundError("ActionResult not found after following pointer")
		}
	} else {
		fmt.Printf("looked up real result\n")
	}

	return r.fetchOutputsAndResult(ctx, actionResult, edge)
}

func (r *CachingCommandRunner) uploadCompletedEdge(ctx context.Context, edge *graph.Edge, exitCode exit_status.ExitStatusType, output string, action *repb.Action, hasDepfile bool) ([]*bespb.File, error) {
	// Skip uploading failed actions.
	if exitCode != exit_status.ExitSuccess {
		return nil, nil
	}
	defer span.Record(ctx, "upload outputs")()

	ar := &repb.ActionResult{
		ExitCode:    int32(exitCode),
		OutputFiles: make([]*repb.OutputFile, 0, len(edge.Outputs())),
	}

	instanceName := remote_flags.RemoteInstanceName()
	digestFunction := filetransfer.DigestFunction
	actionDigest, err := digest.ComputeForMessage(action, digestFunction)
	if err != nil {
		return nil, err
	}

	ul := cachetools.NewBatchCASUploader(ctx, r.uploader, r.uploader, instanceName, digestFunction)
	uploadedOutputs := make([]*bespb.File, 0, len(edge.Outputs()))
	bytestreamURIPrefix := remote_flags.BytestreamURIPrefix()

	// Upload outputs
	for _, output := range edge.Outputs() {
		fi, err := os.Stat(output.Path())
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		d, err := ul.UploadFile(output.Path())
		if err != nil {
			return nil, err
		}
		ar.OutputFiles = append(ar.OutputFiles, &repb.OutputFile{
			Path:         output.Path(),
			Digest:       d,
			IsExecutable: cachetools.IsExecutable(fi),
		})

		rn := digest.NewCASResourceName(d, instanceName, digestFunction)
		uri := fmt.Sprintf("%s/%s", bytestreamURIPrefix, rn.DownloadString())
		uploadedOutputs = append(uploadedOutputs, &bespb.File{
			Name:   output.Path(),
			File:   &bespb.File_Uri{Uri: uri},
			Digest: d.GetHash(),
			Length: d.GetSizeBytes(),
		})
	}

	// Upload stdout
	ar.StdoutDigest, err = ul.UploadBlob([]byte(output))
	if err != nil {
		return nil, err
	}
	if err := ul.Wait(); err != nil {
		return nil, err
	}

	// Upload the actual action result under the current action key.
	acrn := digest.NewACResourceName(actionDigest, instanceName, digestFunction)
	if err := r.uploader.UploadActionResult(ctx, acrn, ar); err != nil {
		return nil, err
	}

	// For edges with depfiles and known dynamic deps, also upload a pointer
	// under the static action key. This allows future builds on machines without
	// .ninja_deps to discover the dynamic deps and follow the pointer to find
	// the actual result.
	if hasDepfile {
		dynamicInputs := edge.DynamicInputs()
		if len(dynamicInputs) > 0 {
			staticAction, err := r.assembleStaticAction(ctx, edge)
			if err != nil {
				util.Warningf("failed to compute static action for pointer: %v", err)
				return uploadedOutputs, nil
			}
			fmt.Printf("Uploaded static action\n")
			if err := r.uploadDepsOnlyResult(ctx, staticAction, dynamicInputs); err != nil {
				// Log but don't fail - the main action result was uploaded successfully
				util.Warningf("failed to upload pointer: %v", err)
			}
			fmt.Printf("Uploaded deps only result\n")
		}
	}

	return uploadedOutputs, nil
}

func (r *CachingCommandRunner) WaitForCommand() *spawn.Result {
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
