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
	"sort"
	"strings"
	"time"

	"github.com/bazelbuild/remote-apis-sdks/go/pkg/client"
	"github.com/bazelbuild/remote-apis-sdks/go/pkg/digest"
	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/buildbuddy-io/gin/internal/graph"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

// RemoteClient wraps the official remote-apis-sdks client
type RemoteClient struct {
	client       *client.Client
	instanceName string
	debugMode    bool
}

// NewRemoteClient creates a new remote execution client
func NewRemoteClient(endpoint string, instanceName string) (*RemoteClient, error) {
	// Parse endpoint
	normalizedEndpoint, useTLS := parseEndpoint(endpoint)
	
	// Create dial parameters
	dialParams := client.DialParams{
		Service:            normalizedEndpoint,
		NoSecurity:         !useTLS,
		TransportCredsOnly: useTLS,
	}
	
	// Create the client with options
	c, err := client.NewClient(
		context.Background(),
		instanceName,
		dialParams,
		client.ChunkMaxSize(4*1024*1024), // 4MB chunks
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create remote client: %w", err)
	}
	
	return &RemoteClient{
		client:       c,
		instanceName: instanceName,
		debugMode:    os.Getenv("GIN_REMOTE_DEBUG") == "1",
	}, nil
}

// Close closes the client connection
func (rc *RemoteClient) Close() error {
	return rc.client.Close()
}

// ExecuteEdge executes a build edge remotely
func (rc *RemoteClient) ExecuteEdge(ctx context.Context, edge *graph.Edge) (*repb.ActionResult, error) {
	// Prepare command
	command, err := rc.buildCommand(edge)
	if err != nil {
		return nil, fmt.Errorf("failed to build command: %w", err)
	}
	
	// Upload command
	cmdDigest, err := rc.uploadCommand(ctx, command)
	if err != nil {
		return nil, fmt.Errorf("failed to upload command: %w", err)
	}
	
	if rc.debugMode {
		fmt.Printf("[REMOTE] Command digest: %s\n", cmdDigest.Hash)
	}
	
	// Upload input files
	inputRoot, err := rc.uploadInputs(ctx, edge)
	if err != nil {
		return nil, fmt.Errorf("failed to upload inputs: %w", err)
	}
	
	if rc.debugMode {
		fmt.Printf("[REMOTE] Input root digest: %s\n", inputRoot.Hash)
	}
	
	// Create action
	action := &repb.Action{
		CommandDigest:   cmdDigest.ToProto(),
		InputRootDigest: inputRoot.ToProto(),
		Timeout:         durationpb.New(5 * time.Minute),
		DoNotCache:      false,
	}
	
	// Upload action
	actionDigest, err := rc.uploadAction(ctx, action)
	if err != nil {
		return nil, fmt.Errorf("failed to upload action: %w", err)
	}
	
	if rc.debugMode {
		fmt.Printf("[REMOTE] Action digest: %s\n", actionDigest.Hash)
	}
	
	// Execute the action
	op, err := rc.client.ExecuteAndWait(ctx, &repb.ExecuteRequest{
		InstanceName: rc.instanceName,
		ActionDigest: actionDigest.ToProto(),
		SkipCacheLookup: false,
	})
	if err != nil {
		return nil, fmt.Errorf("execution failed: %w", err)
	}
	
	// Extract the action result from the operation
	execResp := &repb.ExecuteResponse{}
	if err := op.GetResponse().UnmarshalTo(execResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}
	
	result := execResp.Result
	if result == nil {
		return nil, fmt.Errorf("no result in execute response")
	}
	
	if rc.debugMode {
		fmt.Printf("[REMOTE] Execution completed with exit code: %d\n", result.ExitCode)
	}
	
	// Download outputs
	if err := rc.downloadOutputs(ctx, edge, result); err != nil {
		return nil, fmt.Errorf("failed to download outputs: %w", err)
	}
	
	return result, nil
}

// buildCommand creates a Command proto from an edge
func (rc *RemoteClient) buildCommand(edge *graph.Edge) (*repb.Command, error) {
	cmdStr := edge.GetBinding("command")
	if cmdStr == "" {
		return nil, fmt.Errorf("no command specified")
	}
	
	if rc.debugMode {
		fmt.Printf("[REMOTE] Raw command: %s\n", cmdStr)
	}
	
	// Parse command into arguments
	args, err := parseCommandArgs(cmdStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse command: %w", err)
	}
	
	// Fix linking commands that use -l flags with archives
	// This addresses symbol resolution issues in remote execution environments
	args = rc.fixLinkingFlags(args)
	
	if rc.debugMode {
		fmt.Printf("[REMOTE] Command arguments: %v\n", args)
	}
	
	// Collect output files
	var outputFiles []string
	for _, output := range edge.Outputs() {
		outputFiles = append(outputFiles, output.Path())
	}
	
	if rc.debugMode && len(outputFiles) > 0 {
		fmt.Printf("[REMOTE] Expected outputs: %v\n", outputFiles)
	}
	
	// Build environment variables
	var envVars []*repb.Command_EnvironmentVariable
	if envStr := edge.GetBinding("env"); envStr != "" {
		for _, pair := range strings.Fields(envStr) {
			parts := strings.SplitN(pair, "=", 2)
			if len(parts) == 2 {
				envVars = append(envVars, &repb.Command_EnvironmentVariable{
					Name:  parts[0],
					Value: parts[1],
				})
			}
		}
	}
	
	return &repb.Command{
		Arguments:            args,
		EnvironmentVariables: envVars,
		OutputFiles:          outputFiles,
		OutputDirectories:    []string{},
		Platform:             rc.getPlatform(),
		WorkingDirectory:     "", // Empty means root of input root
	}, nil
}

// getPlatform returns platform properties
func (rc *RemoteClient) getPlatform() *repb.Platform {
	return &repb.Platform{
		Properties: []*repb.Platform_Property{
			{Name: "OSFamily", Value: detectOSFamily()},
			{Name: "arch", Value: "amd64"},
		},
	}
}

// uploadCommand uploads a command to CAS
func (rc *RemoteClient) uploadCommand(ctx context.Context, cmd *repb.Command) (digest.Digest, error) {
	cmdData, err := proto.Marshal(cmd)
	if err != nil {
		return digest.Digest{}, err
	}
	
	cmdDigest := digest.NewFromBlob(cmdData)
	
	// Check if already in CAS
	missing, err := rc.client.MissingBlobs(ctx, []digest.Digest{cmdDigest})
	if err != nil || len(missing) > 0 {
		// Upload if missing or check failed
		err = rc.client.WriteBlobs(ctx, map[digest.Digest][]byte{
			cmdDigest: cmdData,
		})
		if err != nil {
			return digest.Digest{}, err
		}
	} else if rc.debugMode {
		fmt.Printf("[REMOTE] Command already in CAS\n")
	}
	
	return cmdDigest, nil
}

// uploadAction uploads an action to CAS
func (rc *RemoteClient) uploadAction(ctx context.Context, action *repb.Action) (digest.Digest, error) {
	actionData, err := proto.Marshal(action)
	if err != nil {
		return digest.Digest{}, err
	}
	
	actionDigest := digest.NewFromBlob(actionData)
	
	// Check if already in CAS
	missing, err := rc.client.MissingBlobs(ctx, []digest.Digest{actionDigest})
	if err != nil || len(missing) > 0 {
		// Upload if missing or check failed
		err = rc.client.WriteBlobs(ctx, map[digest.Digest][]byte{
			actionDigest: actionData,
		})
		if err != nil {
			return digest.Digest{}, err
		}
	} else if rc.debugMode {
		fmt.Printf("[REMOTE] Action already in CAS\n")
	}
	
	return actionDigest, nil
}

// uploadInputs uploads input files and returns the input root digest
func (rc *RemoteClient) uploadInputs(ctx context.Context, edge *graph.Edge) (digest.Digest, error) {
	// Group files by directory
	filesByDir := make(map[string][]struct {
		name     string
		path     string
		content  []byte
		isExec   bool
	})
	
	blobs := make(map[digest.Digest][]byte)
	addedFiles := make(map[string]bool)
	
	// For C++ builds, include ALL source files from common directories
	// This ensures all headers and dependencies are available
	sourceDirs := []string{
		"src",
		"include", 
		"third_party",
		"build",  // Build outputs directory
		".",      // Current directory
	}
	
	for _, dir := range sourceDirs {
		// Skip if directory doesn't exist
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		
		// Walk the directory tree
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // Skip files we can't read
			}
			
			// Skip directories and already added files
			if info.IsDir() || addedFiles[path] {
				return nil
			}
			
			// Include source, header files, and build artifacts
			ext := filepath.Ext(path)
			includeFile := false
			
			// Source and header files
			if ext == ".h" || ext == ".hpp" || ext == ".hh" || ext == ".H" ||
			   ext == ".cc" || ext == ".cpp" || ext == ".c" || ext == ".C" ||
			   ext == ".inc" || ext == ".inl" {
				includeFile = true
			}
			
			// Build artifacts in build/ directory
			if strings.HasPrefix(path, "build/") && 
			   (ext == ".o" || ext == ".a" || ext == ".so" || ext == ".dylib") {
				includeFile = true
			}
			
			if includeFile {
				
				content, err := os.ReadFile(path)
				if err != nil {
					return nil // Skip files we can't read
				}
				
				addedFiles[path] = true
				fileDigest := digest.NewFromBlob(content)
				blobs[fileDigest] = content
				
				// Determine directory structure for remote
				dir := filepath.Dir(path)
				if dir == "." {
					dir = ""
				}
				
				filesByDir[dir] = append(filesByDir[dir], struct {
					name     string
					path     string
					content  []byte
					isExec   bool
				}{
					name:    filepath.Base(path),
					path:    path,
					content: content,
					isExec:  info.Mode()&0111 != 0,
				})
			}
			
			return nil
		})
		
		if err != nil && rc.debugMode {
			fmt.Printf("[REMOTE] Warning: error walking %s: %v\n", dir, err)
		}
	}
	
	// IMPORTANT: Add ALL input files that are explicitly required for this edge
	// This includes both source files and intermediate build outputs (.o files, etc.)
	allInputs := edge.Inputs()
	
	if rc.debugMode && len(allInputs) > 0 {
		fmt.Printf("[REMOTE] Required inputs for this action:\n")
		for _, input := range allInputs {
			fmt.Printf("[REMOTE]   - %s\n", input.Path())
		}
	}
	
	for _, input := range allInputs {
		path := input.Path()
		
		if addedFiles[path] {
			continue // Already added
		}
		
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				// Check if this is a build output that should have been created
				if strings.Contains(path, "build/") || strings.HasSuffix(path, ".o") || strings.HasSuffix(path, ".a") {
					// This is a problem - the file should exist from a previous build step
					fmt.Printf("[REMOTE] ERROR: Required input file %s does not exist\n", path)
					fmt.Printf("[REMOTE]        This file should have been created by a previous build step\n")
					return digest.Digest{}, fmt.Errorf("required input file %s does not exist", path)
				}
				if rc.debugMode {
					fmt.Printf("[REMOTE] Warning: input file %s does not exist (might be generated later)\n", path)
				}
				continue // Skip missing inputs - they might be generated
			}
			return digest.Digest{}, fmt.Errorf("failed to read %s: %w", path, err)
		}
		
		info, err := os.Stat(path)
		if err != nil {
			return digest.Digest{}, fmt.Errorf("failed to stat %s: %w", path, err)
		}
		
		addedFiles[path] = true
		fileDigest := digest.NewFromBlob(content)
		blobs[fileDigest] = content
		
		dir := filepath.Dir(path)
		if dir == "." {
			dir = ""
		}
		
		filesByDir[dir] = append(filesByDir[dir], struct {
			name     string
			path     string
			content  []byte
			isExec   bool
		}{
			name:    filepath.Base(path),
			path:    path,
			content: content,
			isExec:  info.Mode()&0111 != 0,
		})
		
		if rc.debugMode {
			if strings.HasSuffix(path, ".o") || strings.HasSuffix(path, ".a") {
				fmt.Printf("[REMOTE]   Successfully added build output: %s (dir: %s, file: %s)\n", path, dir, filepath.Base(path))
			}
		}
	}
	
	// Build directory tree from bottom up
	dirsByPath := make(map[string]*repb.Directory)
	
	// Get all unique directory paths
	allDirs := make(map[string]bool)
	for dir := range filesByDir {
		allDirs[dir] = true
		// Ensure all parent directories exist
		parts := strings.Split(dir, string(filepath.Separator))
		for i := range parts {
			if i > 0 {
				parentDir := strings.Join(parts[:i], string(filepath.Separator))
				allDirs[parentDir] = true
			}
		}
	}
	
	// Create all directories including root
	dirsByPath[""] = &repb.Directory{} // Ensure root exists
	for dir := range allDirs {
		dirsByPath[dir] = &repb.Directory{}
	}
	
	if rc.debugMode && len(allDirs) > 0 {
		fmt.Printf("[REMOTE] Directory structure:\n")
		for dir := range dirsByPath {
			if dir == "" {
				fmt.Printf("[REMOTE]   - / (root)\n")
			} else {
				fmt.Printf("[REMOTE]   - %s/\n", dir)
			}
		}
		
		// Show files in build/ directory if it exists
		if files, ok := filesByDir["build"]; ok && len(files) > 0 {
			fmt.Printf("[REMOTE] Files in build/ directory:\n")
			for _, f := range files {
				fmt.Printf("[REMOTE]   - %s\n", f.name)
			}
		}
	}
	
	// Add files to their directories
	for dir, files := range filesByDir {
		directory := dirsByPath[dir]
		for _, file := range files {
			fileDigest := digest.NewFromBlob(file.content)
			directory.Files = append(directory.Files, &repb.FileNode{
				Name:         file.name,
				Digest:       fileDigest.ToProto(),
				IsExecutable: file.isExec,
			})
		}
	}
	
	// Sort directory paths by depth (deepest first) to build tree bottom-up
	var sortedDirs []string
	for dir := range dirsByPath {
		sortedDirs = append(sortedDirs, dir)
	}
	sort.Slice(sortedDirs, func(i, j int) bool {
		// Count depth - empty string (root) has depth 0
		depthI := 0
		if sortedDirs[i] != "" {
			depthI = strings.Count(sortedDirs[i], string(filepath.Separator)) + 1
		}
		depthJ := 0
		if sortedDirs[j] != "" {
			depthJ = strings.Count(sortedDirs[j], string(filepath.Separator)) + 1
		}
		return depthI > depthJ
	})
	
	if rc.debugMode {
		fmt.Printf("[REMOTE] Processing order:\n")
		for _, dir := range sortedDirs {
			if dir == "" {
				fmt.Printf("[REMOTE]   - / (root)\n")
			} else {
				fmt.Printf("[REMOTE]   - %s/\n", dir)
			}
		}
	}
	
	// Build directory digests bottom-up
	dirDigests := make(map[string]digest.Digest)
	
	for _, dir := range sortedDirs {
		directory := dirsByPath[dir]
		
		// Add child directories
		for childDir := range dirsByPath {
			if childDir == dir {
				continue
			}
			// Check if childDir is a direct child of dir
			// For "build" directory, parent should be ""
			// For "src/foo" directory, parent should be "src"
			var parentDir string
			if strings.Contains(childDir, "/") {
				parentDir = filepath.Dir(childDir)
			} else {
				// Top-level directory like "build" - parent is root ("")
				parentDir = ""
			}
			
			if parentDir == dir {
				if childDigest, ok := dirDigests[childDir]; ok {
					if rc.debugMode {
						fmt.Printf("[REMOTE]   Adding directory %s to parent %s\n", childDir, dir)
					}
					directory.Directories = append(directory.Directories, &repb.DirectoryNode{
						Name:   filepath.Base(childDir),
						Digest: childDigest.ToProto(),
					})
				} else {
					if rc.debugMode {
						fmt.Printf("[REMOTE]   ERROR: Cannot add %s to %s (no digest yet)\n", childDir, dir)
					}
				}
			}
		}
		
		// Debug: Show what's in this directory before marshaling
		if rc.debugMode {
			dirName := dir
			if dirName == "" {
				dirName = "root"
			}
			if len(directory.Files) > 0 || len(directory.Directories) > 0 {
				fmt.Printf("[REMOTE] Directory '%s' contents:\n", dirName)
				for _, f := range directory.Files {
					fmt.Printf("[REMOTE]   - File: %s\n", f.Name)
				}
				for _, d := range directory.Directories {
					fmt.Printf("[REMOTE]   - Dir: %s/\n", d.Name)
				}
			}
		}
		
		// Marshal and store directory
		dirData, err := proto.Marshal(directory)
		if err != nil {
			return digest.Digest{}, fmt.Errorf("failed to marshal directory %s: %w", dir, err)
		}
		
		dirDigest := digest.NewFromBlob(dirData)
		dirDigests[dir] = dirDigest
		blobs[dirDigest] = dirData
	}
	
	if rc.debugMode {
		totalFiles := 0
		for _, files := range filesByDir {
			totalFiles += len(files)
		}
		fmt.Printf("[REMOTE] Uploading %d input files in %d directories\n", totalFiles, len(dirsByPath))
		
		// Only show detailed file list if not too many files
		if totalFiles <= 50 {
			for dir, files := range filesByDir {
				for _, file := range files {
					if dir == "" {
						fmt.Printf("[REMOTE]   - %s (root)\n", file.name)
					} else {
						fmt.Printf("[REMOTE]   - %s/%s\n", dir, file.name)
					}
				}
			}
		}
	}
	
	// Check which blobs are missing in CAS before uploading
	if len(blobs) > 0 {
		digests := make([]digest.Digest, 0, len(blobs))
		for d := range blobs {
			digests = append(digests, d)
		}
		
		missingDigests, err := rc.client.MissingBlobs(ctx, digests)
		if err != nil {
			// If we can't check, just upload everything
			if rc.debugMode {
				fmt.Printf("[REMOTE] Warning: couldn't check missing blobs: %v\n", err)
			}
		} else {
			// Only upload missing blobs
			blobsToUpload := make(map[digest.Digest][]byte)
			for _, d := range missingDigests {
				if data, ok := blobs[d]; ok {
					blobsToUpload[d] = data
				}
			}
			
			if rc.debugMode {
				fmt.Printf("[REMOTE] %d of %d blobs need uploading\n", len(blobsToUpload), len(blobs))
			}
			
			blobs = blobsToUpload
		}
	}
	
	// Upload missing blobs
	if len(blobs) > 0 {
		err := rc.client.WriteBlobs(ctx, blobs)
		if err != nil {
			return digest.Digest{}, fmt.Errorf("failed to upload blobs: %w", err)
		}
	}
	
	// Return root directory digest
	rootDigest, ok := dirDigests[""]
	if !ok {
		// If there's no root directory, create an empty one
		rootDir := &repb.Directory{}
		rootData, err := proto.Marshal(rootDir)
		if err != nil {
			return digest.Digest{}, fmt.Errorf("failed to marshal root directory: %w", err)
		}
		rootDigest = digest.NewFromBlob(rootData)
		blobs[rootDigest] = rootData
		
		// Upload the empty root
		err = rc.client.WriteBlobs(ctx, blobs)
		if err != nil {
			return digest.Digest{}, fmt.Errorf("failed to upload root: %w", err)
		}
	}
	return rootDigest, nil
}

// downloadOutputs downloads output files from the action result
func (rc *RemoteClient) downloadOutputs(ctx context.Context, edge *graph.Edge, result *repb.ActionResult) error {
	if rc.debugMode {
		fmt.Printf("[REMOTE] Downloading %d output files\n", len(result.OutputFiles))
		for _, outputFile := range result.OutputFiles {
			fmt.Printf("[REMOTE]   Output: %s\n", outputFile.Path)
		}
	}
	
	for _, outputFile := range result.OutputFiles {
		// Check if file already exists locally with matching content
		if info, err := os.Stat(outputFile.Path); err == nil && !info.IsDir() {
			// File exists, check if it matches the expected digest
			d, err := digest.NewFromProto(outputFile.Digest)
			if err != nil {
				return fmt.Errorf("failed to parse digest for %s: %w", outputFile.Path, err)
			}
			
			// Read existing file
			existingContent, err := os.ReadFile(outputFile.Path)
			if err == nil {
				existingDigest := digest.NewFromBlob(existingContent)
				if existingDigest.Hash == d.Hash && existingDigest.Size == d.Size {
					if rc.debugMode {
						fmt.Printf("[REMOTE] Output %s already exists locally with correct content\n", outputFile.Path)
					}
					continue // Skip download
				}
			}
		}
		
		// Parse digest
		d, err := digest.NewFromProto(outputFile.Digest)
		if err != nil {
			return fmt.Errorf("failed to parse digest for %s: %w", outputFile.Path, err)
		}
		
		var content []byte
		if outputFile.Contents != nil {
			// Content is inlined
			content = outputFile.Contents
		} else {
			// Download from CAS
			blob, _, err := rc.client.ReadBlob(ctx, d)
			if err != nil {
				return fmt.Errorf("failed to download %s: %w", outputFile.Path, err)
			}
			content = blob
		}
		
		// Create parent directory
		dir := filepath.Dir(outputFile.Path)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", dir, err)
			}
		}
		
		// Write the file
		perm := os.FileMode(0644)
		if outputFile.IsExecutable {
			perm = 0755
		}
		
		if err := os.WriteFile(outputFile.Path, content, perm); err != nil {
			return fmt.Errorf("failed to write %s: %w", outputFile.Path, err)
		}
		
		if rc.debugMode {
			fmt.Printf("[REMOTE] Downloaded %s (%d bytes) to local path: %s\n", outputFile.Path, len(content), outputFile.Path)
		}
	}
	
	// Download output directories
	for _, outputDir := range result.OutputDirectories {
		if err := rc.downloadDirectory(ctx, outputDir); err != nil {
			return fmt.Errorf("failed to download directory %s: %w", outputDir.Path, err)
		}
	}
	
	// Handle stdout/stderr
	if result.StdoutRaw != nil {
		if rc.debugMode {
			fmt.Printf("[REMOTE] stdout: %s\n", string(result.StdoutRaw))
		}
	} else if result.StdoutDigest != nil {
		d, err := digest.NewFromProto(result.StdoutDigest)
		if err == nil {
			stdout, _, err := rc.client.ReadBlob(ctx, d)
			if err == nil && rc.debugMode {
				fmt.Printf("[REMOTE] stdout: %s\n", string(stdout))
			}
		}
	}
	
	if result.StderrRaw != nil {
		if rc.debugMode {
			fmt.Printf("[REMOTE] stderr: %s\n", string(result.StderrRaw))
		}
	} else if result.StderrDigest != nil {
		d, err := digest.NewFromProto(result.StderrDigest)
		if err == nil {
			stderr, _, err := rc.client.ReadBlob(ctx, d)
			if err == nil && rc.debugMode {
				fmt.Printf("[REMOTE] stderr: %s\n", string(stderr))
			}
		}
	}
	
	return nil
}

// downloadDirectory downloads an output directory
func (rc *RemoteClient) downloadDirectory(ctx context.Context, outputDir *repb.OutputDirectory) error {
	if rc.debugMode {
		fmt.Printf("[REMOTE] Downloading directory %s\n", outputDir.Path)
	}
	
	// Download the tree
	treeDigest, err := digest.NewFromProto(outputDir.TreeDigest)
	if err != nil {
		return fmt.Errorf("failed to parse tree digest: %w", err)
	}
	
	// Create base directory
	if err := os.MkdirAll(outputDir.Path, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	
	// Download the Tree proto
	treeProto := &repb.Tree{}
	treeData, _, err := rc.client.ReadBlob(ctx, treeDigest)
	if err != nil {
		return fmt.Errorf("failed to download tree: %w", err)
	}
	if err := proto.Unmarshal(treeData, treeProto); err != nil {
		return fmt.Errorf("failed to unmarshal tree: %w", err)
	}
	
	// Use client.FlattenTree to get a map of paths to TreeOutput
	outputs, err := rc.client.FlattenTree(treeProto, outputDir.Path)
	if err != nil {
		return fmt.Errorf("failed to flatten tree: %w", err)
	}
	
	// Process all outputs
	for path, output := range outputs {
		// Download and write each file
		if output.IsEmptyDirectory {
			// Just create the empty directory
			if err := os.MkdirAll(path, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", path, err)
			}
			continue
		}
		
		// Create parent directory
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
		
		if output.SymlinkTarget != "" {
			// Create symlink
			if err := os.Symlink(output.SymlinkTarget, path); err != nil {
				return fmt.Errorf("failed to create symlink %s: %w", path, err)
			}
		} else if output.Digest.Hash != "" {
			// Download file content
			content, _, err := rc.client.ReadBlob(ctx, output.Digest)
			if err != nil {
				return fmt.Errorf("failed to download %s: %w", path, err)
			}
			
			// Write file
			perm := os.FileMode(0644)
			if output.IsExecutable {
				perm = 0755
			}
			
			if err := os.WriteFile(path, content, perm); err != nil {
				return fmt.Errorf("failed to write %s: %w", path, err)
			}
		}
	}
	
	return nil
}

// GetCapabilities queries the server capabilities
func (rc *RemoteClient) GetCapabilities(ctx context.Context) (*repb.ServerCapabilities, error) {
	return rc.client.GetCapabilities(ctx)
}

// CheckActionCache checks if an action result is cached
func (rc *RemoteClient) CheckActionCache(ctx context.Context, actionDigest digest.Digest) (*repb.ActionResult, error) {
	return rc.client.GetActionResult(ctx, &repb.GetActionResultRequest{
		InstanceName: rc.instanceName,
		ActionDigest: actionDigest.ToProto(),
	})
}

// UpdateActionCache stores an action result in the cache
func (rc *RemoteClient) UpdateActionCache(ctx context.Context, actionDigest digest.Digest, result *repb.ActionResult) error {
	_, err := rc.client.UpdateActionResult(ctx, &repb.UpdateActionResultRequest{
		InstanceName: rc.instanceName,
		ActionDigest: actionDigest.ToProto(),
		ActionResult: result,
	})
	return err
}

// parseCommandArgs parses a command string into arguments, handling quotes properly
func parseCommandArgs(cmd string) ([]string, error) {
	var args []string
	var current strings.Builder
	inDoubleQuote := false
	inSingleQuote := false
	escapeNext := false
	
	for _, r := range cmd {
		if escapeNext {
			current.WriteRune(r)
			escapeNext = false
			continue
		}
		
		if r == '\\' && !inSingleQuote {
			// Backslash escapes in double quotes but not single quotes
			escapeNext = true
			continue
		}
		
		if r == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
			// Don't include the quote character itself
			continue
		}
		
		if r == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
			// Don't include the quote character itself
			continue
		}
		
		if r == ' ' && !inDoubleQuote && !inSingleQuote {
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
			continue
		}
		
		current.WriteRune(r)
	}
	
	// Add any remaining content
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	
	if inDoubleQuote || inSingleQuote {
		return nil, fmt.Errorf("unclosed quote in command")
	}
	
	return args, nil
}

// fixLinkingFlags modifies linking commands to use --whole-archive for better symbol resolution
func (rc *RemoteClient) fixLinkingFlags(args []string) []string {
	if len(args) == 0 {
		return args
	}
	
	// Check if this is a C++ linking command with -l flags
	if args[0] != "c++" && args[0] != "g++" && args[0] != "gcc" {
		return args
	}
	
	// Look for -l flags
	hasLFlags := false
	for _, arg := range args {
		if strings.HasPrefix(arg, "-l") {
			hasLFlags = true
			break
		}
	}
	
	if !hasLFlags {
		return args
	}
	
	if rc.debugMode {
		fmt.Printf("[REMOTE] Detected linking command with -l flags, adding --whole-archive\n")
	}
	
	// Find the position to insert --whole-archive (before the first -l flag)
	// and --no-whole-archive (after the last -l flag)
	newArgs := make([]string, 0, len(args)+2)
	wholeArchiveInserted := false
	lastLFlagIndex := -1
	
	// First pass: find the last -l flag
	for i, arg := range args {
		if strings.HasPrefix(arg, "-l") {
			lastLFlagIndex = i
		}
	}
	
	// Second pass: build the new args
	for i, arg := range args {
		if strings.HasPrefix(arg, "-l") && !wholeArchiveInserted {
			// Insert --whole-archive before the first -l flag
			newArgs = append(newArgs, "-Wl,--whole-archive")
			wholeArchiveInserted = true
		}
		
		newArgs = append(newArgs, arg)
		
		if i == lastLFlagIndex && wholeArchiveInserted {
			// Insert --no-whole-archive after the last -l flag
			newArgs = append(newArgs, "-Wl,--no-whole-archive")
		}
	}
	
	if rc.debugMode && wholeArchiveInserted {
		fmt.Printf("[REMOTE] Modified command arguments: %v\n", newArgs)
	}
	
	return newArgs
}

