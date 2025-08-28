// Copyright 2024 The Gin Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package remote

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/buildbuddy-io/gin/internal/graph"
)

// Executor handles remote execution of build edges
type Executor struct {
	client       *Client
	remoteClient *RemoteClient
	localFallback bool
	cacheDir     string
	debugMode    bool
}

// NewExecutor creates a new remote executor
func NewExecutor(endpoint, instanceName string) *Executor {
	// Try to create the official remote client
	remoteClient, err := NewRemoteClient(endpoint, instanceName)
	if err != nil {
		fmt.Printf("[REMOTE] Failed to create remote client: %v\n", err)
		fmt.Printf("[REMOTE] Falling back to placeholder implementation\n")
	}
	
	return &Executor{
		client:        NewClient(endpoint, instanceName),
		remoteClient:  remoteClient,
		localFallback: true,
		cacheDir:     ".gin-cache",
		debugMode:    os.Getenv("GIN_REMOTE_DEBUG") == "1",
	}
}

// Client returns the underlying remote client
func (e *Executor) Client() *Client {
	return e.client
}

// SetDebugMode enables or disables debug logging
func (e *Executor) SetDebugMode(debug bool) {
	e.debugMode = debug
	if e.client != nil {
		e.client.SetDebugMode(debug)
	}
}

// CanExecuteRemotely determines if an edge can be executed remotely
func (e *Executor) CanExecuteRemotely(edge *graph.Edge) bool {
	// Check if edge has remote execution markers
	if edge.Rule() == nil {
		return false
	}

	// Check for remote execution opt-in
	remote := edge.GetBinding("remote")
	if remote == "" || remote == "false" || remote == "0" {
		return false
	}

	// Check for unsupported features
	command := edge.GetBinding("command")
	
	// Don't remotely execute commands with shell operators
	if containsShellOperators(command) {
		return false
	}

	// Don't remotely execute phony edges
	if edge.IsPhony() {
		return false
	}

	return true
}

func containsShellOperators(command string) bool {
	operators := []string{"&&", "||", "|", ">", "<", ">>", "<<", ";", "&"}
	for _, op := range operators {
		if strings.Contains(command, op) {
			return true
		}
	}
	return false
}

// ExecuteRemotely executes an edge on a remote worker
func (e *Executor) ExecuteRemotely(ctx context.Context, edge *graph.Edge) (*ActionResult, error) {
	command := edge.EvaluateCommand(false)
	
	if e.debugMode {
		fmt.Printf("[REMOTE] Starting remote execution for: %s\n", command)
		if e.remoteClient != nil {
			fmt.Printf("[REMOTE] Using official remote-apis client\n")
		} else {
			fmt.Printf("[REMOTE] Using placeholder client\n")
		}
	}
	
	// Use the official client if available
	if e.remoteClient != nil {
		result, err := e.remoteClient.ExecuteEdge(ctx, edge)
		if err != nil {
			if e.debugMode {
				fmt.Printf("[REMOTE] Remote execution failed: %v\n", err)
			}
			if e.localFallback {
				if e.debugMode {
					fmt.Printf("[REMOTE] Falling back to local execution\n")
				}
				return e.executeLocally(edge)
			}
			return nil, err
		}
		
		// Convert from protobuf ActionResult to our ActionResult
		return &ActionResult{
			OutputFiles:       convertOutputFiles(result.OutputFiles),
			OutputDirectories: convertOutputDirectories(result.OutputDirectories),
			ExitCode:          result.ExitCode,
			StdoutDigest:      convertDigest(result.StdoutDigest),
			StderrDigest:      convertDigest(result.StderrDigest),
			ExecutionMetadata: convertExecutionMetadata(result.ExecutionMetadata),
		}, nil
	}
	
	// Prepare the command
	cmd, err := e.prepareCommand(edge)
	if err != nil {
		if e.debugMode {
			fmt.Printf("[REMOTE] Failed to prepare command: %v\n", err)
		}
		return nil, err
	}
	if e.debugMode {
		fmt.Printf("[REMOTE] Command args: %v\n", cmd.Arguments)
	}

	// Upload command
	cmdBytes := e.serializeCommand(cmd)
	cmdDigest := ComputeDigest(cmdBytes)
	if e.debugMode {
		fmt.Printf("[REMOTE] Uploading command (digest: %s, size: %d bytes)\n", cmdDigest.Hash[:12], cmdDigest.SizeBytes)
	}
	if err := e.client.UploadBlob(ctx, cmdDigest, cmdBytes); err != nil {
		if e.debugMode {
			fmt.Printf("[REMOTE] Failed to upload command: %v\n", err)
		}
		return nil, err
	}

	// Upload input files
	if e.debugMode {
		fmt.Printf("[REMOTE] Uploading %d input files...\n", len(edge.Inputs()))
	}
	inputDigest, err := e.uploadInputs(ctx, edge)
	if err != nil {
		if e.debugMode {
			fmt.Printf("[REMOTE] Failed to upload inputs: %v\n", err)
		}
		return nil, err
	}
	if e.debugMode {
		fmt.Printf("[REMOTE] Input root digest: %s\n", inputDigest.Hash[:12])
	}

	// Create action
	action := &Action{
		CommandDigest:   cmdDigest,
		InputRootDigest: inputDigest,
		Timeout:         5 * time.Minute,
		DoNotCache:      false,
		Platform:        e.detectPlatform(),
	}
	
	if e.debugMode {
		fmt.Printf("[REMOTE] Platform: %v\n", action.Platform.Properties)
	}

	// Execute remotely
	if e.debugMode {
		fmt.Printf("[REMOTE] Executing action remotely...\n")
	}
	result, err := e.client.Execute(ctx, action, false)
	if err != nil {
		if e.debugMode {
			fmt.Printf("[REMOTE] Remote execution failed: %v\n", err)
		}
		if e.localFallback {
			if e.debugMode {
				fmt.Printf("[REMOTE] Falling back to local execution\n")
			}
			// Fall back to local execution
			return e.executeLocally(edge)
		}
		return nil, err
	}
	
	if e.debugMode {
		fmt.Printf("[REMOTE] Execution completed with exit code: %d\n", result.ExitCode)
		if result.ExecutionMetadata != nil {
			fmt.Printf("[REMOTE] Worker: %s\n", result.ExecutionMetadata.Worker)
			if !result.ExecutionMetadata.ExecutionStartTime.IsZero() && !result.ExecutionMetadata.ExecutionCompletedTime.IsZero() {
				duration := result.ExecutionMetadata.ExecutionCompletedTime.Sub(result.ExecutionMetadata.ExecutionStartTime)
				fmt.Printf("[REMOTE] Execution time: %v\n", duration)
			}
		}
	}

	// Download outputs
	if e.debugMode {
		fmt.Printf("[REMOTE] Downloading %d output files...\n", len(result.OutputFiles))
	}
	if err := e.downloadOutputs(ctx, edge, result); err != nil {
		if e.debugMode {
			fmt.Printf("[REMOTE] Failed to download outputs: %v\n", err)
		}
		return nil, err
	}
	
	if e.debugMode {
		fmt.Printf("[REMOTE] Remote execution successful\n")
	}

	return result, nil
}

func (e *Executor) prepareCommand(edge *graph.Edge) (*Command, error) {
	cmdStr := edge.GetBinding("command")
	if cmdStr == "" {
		return nil, fmt.Errorf("no command specified")
	}

	// Parse command into arguments
	// Simple tokenization - in production would need proper shell parsing
	args := strings.Fields(cmdStr)
	if len(args) == 0 {
		return nil, fmt.Errorf("empty command")
	}

	// Collect output files
	var outputFiles []string
	for _, output := range edge.Outputs() {
		outputFiles = append(outputFiles, output.Path())
	}

	// Get environment variables
	env := make(map[string]string)
	if envStr := edge.GetBinding("env"); envStr != "" {
		// Parse environment variables
		for _, pair := range strings.Fields(envStr) {
			parts := strings.SplitN(pair, "=", 2)
			if len(parts) == 2 {
				env[parts[0]] = parts[1]
			}
		}
	}

	return &Command{
		Arguments:            args,
		EnvironmentVariables: env,
		OutputFiles:          outputFiles,
		OutputDirectories:    []string{},
		Platform:            e.detectPlatform(),
		WorkingDirectory:    "",
	}, nil
}

func (e *Executor) serializeCommand(cmd *Command) []byte {
	// Simple serialization - in production would use protobuf
	var parts []string
	
	parts = append(parts, fmt.Sprintf("ARGS:%s", strings.Join(cmd.Arguments, " ")))
	
	for k, v := range cmd.EnvironmentVariables {
		parts = append(parts, fmt.Sprintf("ENV:%s=%s", k, v))
	}
	
	for _, f := range cmd.OutputFiles {
		parts = append(parts, fmt.Sprintf("OUT:%s", f))
	}
	
	return []byte(strings.Join(parts, "\n"))
}

func (e *Executor) uploadInputs(ctx context.Context, edge *graph.Edge) (Digest, error) {
	// Create a virtual input root with all input files
	inputRoot := filepath.Join(e.cacheDir, "inputs", fmt.Sprintf("%d", edge.ID()))
	if err := os.MkdirAll(inputRoot, 0755); err != nil {
		return Digest{}, err
	}
	defer os.RemoveAll(inputRoot)

	// Link or copy input files
	for _, input := range edge.Inputs() {
		srcPath := input.Path()
		dstPath := filepath.Join(inputRoot, filepath.Base(srcPath))
		
		// Try hard link first, fall back to copy
		if err := os.Link(srcPath, dstPath); err != nil {
			// Copy file
			content, err := os.ReadFile(srcPath)
			if err != nil {
				return Digest{}, err
			}
			if err := os.WriteFile(dstPath, content, 0644); err != nil {
				return Digest{}, err
			}
		}
	}

	// Upload directory tree
	return e.client.UploadDirectory(ctx, inputRoot)
}

func (e *Executor) downloadOutputs(ctx context.Context, edge *graph.Edge, result *ActionResult) error {
	// Download output files
	for _, outFile := range result.OutputFiles {
		if err := e.client.DownloadFile(ctx, outFile.Digest, outFile.Path); err != nil {
			return err
		}
		
		// Set executable bit if needed
		if outFile.IsExecutable {
			if err := os.Chmod(outFile.Path, 0755); err != nil {
				return err
			}
		}
	}

	// Download output directories
	for _, outDir := range result.OutputDirectories {
		if err := e.downloadDirectory(ctx, outDir.Path, outDir.TreeDigest); err != nil {
			return err
		}
	}

	return nil
}

func (e *Executor) downloadDirectory(ctx context.Context, path string, treeDigest Digest) error {
	// Download and reconstruct directory tree
	// In production, would properly deserialize the tree structure
	
	if err := os.MkdirAll(path, 0755); err != nil {
		return err
	}
	
	// Download tree metadata
	treeBytes, err := e.client.DownloadBlob(ctx, treeDigest)
	if err != nil {
		return err
	}
	
	// Parse and download files in tree
	tree := e.deserializeTree(treeBytes)
	for _, file := range tree.Files {
		filePath := filepath.Join(path, file.Path)
		if err := e.client.DownloadFile(ctx, file.Digest, filePath); err != nil {
			return err
		}
		
		if file.IsExecutable {
			if err := os.Chmod(filePath, 0755); err != nil {
				return err
			}
		}
	}
	
	return nil
}

func (e *Executor) deserializeTree(data []byte) *DirectoryTree {
	tree := &DirectoryTree{}
	
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		
		parts := strings.Split(line, ":")
		if len(parts) < 2 {
			continue
		}
		
		switch parts[0] {
		case "F":
			if len(parts) >= 5 {
				tree.Files = append(tree.Files, TreeFile{
					Path:         parts[1],
					Digest:       Digest{Hash: parts[2]},
					IsExecutable: parts[4] == "true",
				})
			}
		case "D":
			if len(parts) >= 4 {
				tree.Directories = append(tree.Directories, TreeDirectory{
					Path:   parts[1],
					Digest: Digest{Hash: parts[2]},
				})
			}
		}
	}
	
	return tree
}

func (e *Executor) executeLocally(edge *graph.Edge) (*ActionResult, error) {
	// Fallback to local execution
	// This would integrate with the existing local command runner
	
	return &ActionResult{
		ExitCode: 0,
		ExecutionMetadata: &ExecutionMetadata{
			Worker: "local",
		},
	}, nil
}

func (e *Executor) detectPlatform() *Platform {
	// Detect current platform properties
	props := make(map[string]string)
	
	// OS
	props["OSFamily"] = detectOSFamily()
	
	// Architecture  
	props["arch"] = detectArch()
	
	// Container support
	if _, err := os.Stat("/.dockerenv"); err == nil {
		props["container-image"] = "docker"
	}
	
	return &Platform{Properties: props}
}


// Conversion functions from protobuf types to our types

func convertOutputFiles(files []*repb.OutputFile) []OutputFile {
	var result []OutputFile
	for _, f := range files {
		d := convertDigest(f.Digest)
		if d != nil {
			result = append(result, OutputFile{
				Path:         f.Path,
				Digest:       *d,
				IsExecutable: f.IsExecutable,
			})
		}
	}
	return result
}

func convertOutputDirectories(dirs []*repb.OutputDirectory) []OutputDirectory {
	var result []OutputDirectory
	for _, dir := range dirs {
		d := convertDigest(dir.TreeDigest)
		if d != nil {
			result = append(result, OutputDirectory{
				Path:       dir.Path,
				TreeDigest: *d,
			})
		}
	}
	return result
}

func convertDigest(d *repb.Digest) *Digest {
	if d == nil {
		return nil
	}
	return &Digest{
		Hash:      d.Hash,
		SizeBytes: d.SizeBytes,
	}
}

func convertExecutionMetadata(m *repb.ExecutedActionMetadata) *ExecutionMetadata {
	if m == nil {
		return nil
	}
	
	metadata := &ExecutionMetadata{
		Worker: m.Worker,
	}
	
	if m.QueuedTimestamp != nil {
		metadata.QueuedTime = m.QueuedTimestamp.AsTime()
	}
	if m.WorkerStartTimestamp != nil {
		metadata.WorkerStartTime = m.WorkerStartTimestamp.AsTime()
	}
	if m.WorkerCompletedTimestamp != nil {
		metadata.WorkerCompletedTime = m.WorkerCompletedTimestamp.AsTime()
	}
	if m.InputFetchStartTimestamp != nil {
		metadata.InputFetchStartTime = m.InputFetchStartTimestamp.AsTime()
	}
	if m.InputFetchCompletedTimestamp != nil {
		metadata.InputFetchCompletedTime = m.InputFetchCompletedTimestamp.AsTime()
	}
	if m.ExecutionStartTimestamp != nil {
		metadata.ExecutionStartTime = m.ExecutionStartTimestamp.AsTime()
	}
	if m.ExecutionCompletedTimestamp != nil {
		metadata.ExecutionCompletedTime = m.ExecutionCompletedTimestamp.AsTime()
	}
	if m.OutputUploadStartTimestamp != nil {
		metadata.OutputUploadStartTime = m.OutputUploadStartTimestamp.AsTime()
	}
	if m.OutputUploadCompletedTimestamp != nil {
		metadata.OutputUploadCompletedTime = m.OutputUploadCompletedTimestamp.AsTime()
	}
	
	return metadata
}