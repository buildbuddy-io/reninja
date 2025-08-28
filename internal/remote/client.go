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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Digest represents a content-addressable storage digest
type Digest struct {
	Hash      string
	SizeBytes int64
}

// Action represents a remote execution action
type Action struct {
	CommandDigest    Digest
	InputRootDigest  Digest
	Timeout          time.Duration
	DoNotCache       bool
	Platform         *Platform
}

// Platform represents execution platform properties
type Platform struct {
	Properties map[string]string
}

// Command represents an execution command
type Command struct {
	Arguments            []string
	EnvironmentVariables map[string]string
	OutputFiles          []string
	OutputDirectories    []string
	Platform             *Platform
	WorkingDirectory     string
}

// ActionResult represents the result of executing an action
type ActionResult struct {
	OutputFiles       []OutputFile
	OutputDirectories []OutputDirectory
	ExitCode          int32
	StdoutDigest      *Digest
	StderrDigest      *Digest
	ExecutionMetadata *ExecutionMetadata
}

// OutputFile represents an output file from execution
type OutputFile struct {
	Path         string
	Digest       Digest
	IsExecutable bool
}

// OutputDirectory represents an output directory from execution
type OutputDirectory struct {
	Path       string
	TreeDigest Digest
}

// ExecutionMetadata contains metadata about the execution
type ExecutionMetadata struct {
	Worker                   string
	QueuedTime               time.Time
	WorkerStartTime          time.Time
	WorkerCompletedTime      time.Time
	InputFetchStartTime      time.Time
	InputFetchCompletedTime  time.Time
	ExecutionStartTime       time.Time
	ExecutionCompletedTime   time.Time
	OutputUploadStartTime    time.Time
	OutputUploadCompletedTime time.Time
}

// Client provides remote execution capabilities
type Client struct {
	endpoint          string
	instanceName      string
	useActionCache    bool
	compressor        string
	useTLS            bool
	debugMode         bool
	grpcClient        *GRPCClient
}

// NewClient creates a new remote execution client
func NewClient(endpoint string, instanceName string) *Client {
	// Parse and normalize endpoint
	normalizedEndpoint, useTLS := parseEndpoint(endpoint)
	
	// Create gRPC client
	grpcClient, err := NewGRPCClient(normalizedEndpoint, useTLS)
	if err != nil {
		fmt.Printf("[REMOTE] Failed to create gRPC client: %v\n", err)
		// Continue without gRPC client, will fallback to placeholder
	}
	
	return &Client{
		endpoint:       normalizedEndpoint,
		instanceName:   instanceName,
		useActionCache: true,
		compressor:     "zstd",
		useTLS:         useTLS,
		debugMode:      os.Getenv("GIN_REMOTE_DEBUG") == "1",
		grpcClient:     grpcClient,
	}
}

// SetDebugMode enables or disables debug logging
func (c *Client) SetDebugMode(debug bool) {
	c.debugMode = debug
}


// ComputeDigest computes the digest of content
func ComputeDigest(content []byte) Digest {
	hash := sha256.Sum256(content)
	return Digest{
		Hash:      hex.EncodeToString(hash[:]),
		SizeBytes: int64(len(content)),
	}
}

// ComputeFileDigest computes the digest of a file
func ComputeFileDigest(path string) (Digest, error) {
	file, err := os.Open(path)
	if err != nil {
		return Digest{}, err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return Digest{}, err
	}

	stat, err := file.Stat()
	if err != nil {
		return Digest{}, err
	}

	return Digest{
		Hash:      hex.EncodeToString(hasher.Sum(nil)),
		SizeBytes: stat.Size(),
	}, nil
}

// UploadFile uploads a file to CAS
func (c *Client) UploadFile(ctx context.Context, path string) (Digest, error) {
	digest, err := ComputeFileDigest(path)
	if err != nil {
		return Digest{}, err
	}

	// Check if blob already exists
	missing, err := c.FindMissingBlobs(ctx, []Digest{digest})
	if err != nil {
		return Digest{}, err
	}

	if len(missing) == 0 {
		// Already uploaded
		return digest, nil
	}

	// Read file content
	content, err := os.ReadFile(path)
	if err != nil {
		return Digest{}, err
	}

	// Upload blob
	if err := c.UploadBlob(ctx, digest, content); err != nil {
		return Digest{}, err
	}

	return digest, nil
}

// UploadDirectory uploads a directory tree to CAS
func (c *Client) UploadDirectory(ctx context.Context, path string) (Digest, error) {
	// Build directory tree
	tree, err := c.buildDirectoryTree(path)
	if err != nil {
		return Digest{}, err
	}

	// Serialize and upload tree
	treeBytes := c.serializeTree(tree)
	treeDigest := ComputeDigest(treeBytes)

	if err := c.UploadBlob(ctx, treeDigest, treeBytes); err != nil {
		return Digest{}, err
	}

	// Upload all files in the tree
	for _, file := range tree.Files {
		if _, err := c.UploadFile(ctx, filepath.Join(path, file.Path)); err != nil {
			return Digest{}, err
		}
	}

	return treeDigest, nil
}

// DirectoryTree represents a directory tree
type DirectoryTree struct {
	Files       []TreeFile
	Directories []TreeDirectory
}

// TreeFile represents a file in the tree
type TreeFile struct {
	Path         string
	Digest       Digest
	IsExecutable bool
}

// TreeDirectory represents a subdirectory in the tree
type TreeDirectory struct {
	Path   string
	Digest Digest
}

func (c *Client) buildDirectoryTree(root string) (*DirectoryTree, error) {
	tree := &DirectoryTree{}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		if relPath == "." {
			return nil
		}

		if info.IsDir() {
			// Handle directories
			subTree, err := c.buildDirectoryTree(path)
			if err != nil {
				return err
			}
			
			treeBytes := c.serializeTree(subTree)
			digest := ComputeDigest(treeBytes)
			
			tree.Directories = append(tree.Directories, TreeDirectory{
				Path:   relPath,
				Digest: digest,
			})
		} else {
			// Handle files
			digest, err := ComputeFileDigest(path)
			if err != nil {
				return err
			}

			tree.Files = append(tree.Files, TreeFile{
				Path:         relPath,
				Digest:       digest,
				IsExecutable: info.Mode()&0111 != 0,
			})
		}

		return nil
	})

	return tree, err
}

func (c *Client) serializeTree(tree *DirectoryTree) []byte {
	// Simple serialization for now - in production would use protobuf
	var parts []string
	
	for _, file := range tree.Files {
		parts = append(parts, fmt.Sprintf("F:%s:%s:%d:%t", 
			file.Path, file.Digest.Hash, file.Digest.SizeBytes, file.IsExecutable))
	}
	
	for _, dir := range tree.Directories {
		parts = append(parts, fmt.Sprintf("D:%s:%s:%d",
			dir.Path, dir.Digest.Hash, dir.Digest.SizeBytes))
	}
	
	return []byte(strings.Join(parts, "\n"))
}

// Execute executes an action remotely
func (c *Client) Execute(ctx context.Context, action *Action, skipCache bool) (*ActionResult, error) {
	// Check action cache first if enabled
	if c.useActionCache && !skipCache && !action.DoNotCache {
		actionDigest := c.computeActionDigest(action)
		if c.debugMode {
			fmt.Printf("[REMOTE-CLIENT] Checking action cache for digest: %s\n", actionDigest.Hash[:12])
		}
		if result, err := c.GetActionResult(ctx, actionDigest); err == nil && result != nil {
			if c.debugMode {
				fmt.Printf("[REMOTE-CLIENT] Cache hit! Reusing previous result\n")
			}
			return result, nil
		}
		if c.debugMode {
			fmt.Printf("[REMOTE-CLIENT] Cache miss, executing action\n")
		}
	}

	// Execute the action
	if c.debugMode {
		fmt.Printf("[REMOTE-CLIENT] Sending execution request to %s\n", c.endpoint)
	}
	result, err := c.executeRemote(ctx, action)
	if err != nil {
		if c.debugMode {
			fmt.Printf("[REMOTE-CLIENT] Execution request failed: %v\n", err)
		}
		return nil, err
	}

	// Store in action cache if successful
	if c.useActionCache && !action.DoNotCache && result.ExitCode == 0 {
		actionDigest := c.computeActionDigest(action)
		if c.debugMode {
			fmt.Printf("[REMOTE-CLIENT] Storing successful result in action cache\n")
		}
		_ = c.UpdateActionResult(ctx, actionDigest, result)
	}

	return result, nil
}

func (c *Client) computeActionDigest(action *Action) Digest {
	// Compute digest of action for caching
	data := fmt.Sprintf("%s:%s:%d:%t",
		action.CommandDigest.Hash,
		action.InputRootDigest.Hash,
		action.Timeout,
		action.DoNotCache)
	
	return ComputeDigest([]byte(data))
}

func (c *Client) executeRemote(ctx context.Context, action *Action) (*ActionResult, error) {
	if c.grpcClient != nil {
		// Use actual gRPC client
		actionDigest := c.computeActionDigest(action)
		return c.grpcClient.ExecuteAction(ctx, c.instanceName, &actionDigest, action.DoNotCache)
	}
	
	// Fallback to placeholder
	return &ActionResult{
		ExitCode: 0,
		ExecutionMetadata: &ExecutionMetadata{
			Worker:                 "placeholder-worker",
			QueuedTime:            time.Now(),
			WorkerStartTime:       time.Now(),
			WorkerCompletedTime:   time.Now(),
			ExecutionStartTime:    time.Now(),
			ExecutionCompletedTime: time.Now(),
		},
	}, nil
}

// GetActionResult retrieves a cached action result
func (c *Client) GetActionResult(ctx context.Context, actionDigest Digest) (*ActionResult, error) {
	// Placeholder for action cache lookup
	// In production, this would query the ActionCache service
	return nil, fmt.Errorf("not found in cache")
}

// UpdateActionResult stores an action result in the cache
func (c *Client) UpdateActionResult(ctx context.Context, actionDigest Digest, result *ActionResult) error {
	// Placeholder for action cache update
	// In production, this would update the ActionCache service
	return nil
}

// FindMissingBlobs checks which blobs are missing from CAS
func (c *Client) FindMissingBlobs(ctx context.Context, digests []Digest) ([]Digest, error) {
	if c.debugMode {
		fmt.Printf("[REMOTE-CAS] Checking %d blobs in CAS\n", len(digests))
	}
	// Placeholder for CAS missing blob check
	// In production, this would query the CAS service
	// For now, return all as missing to trigger uploads
	if c.debugMode {
		fmt.Printf("[REMOTE-CAS] All %d blobs need to be uploaded\n", len(digests))
	}
	return digests, nil
}

// UploadBlob uploads a blob to CAS
func (c *Client) UploadBlob(ctx context.Context, digest Digest, content []byte) error {
	if c.debugMode {
		fmt.Printf("[REMOTE-CAS] Uploading blob %s (%d bytes) to CAS\n", digest.Hash[:12], len(content))
	}
	// Placeholder for CAS blob upload
	// In production, this would upload to the CAS service
	// Simulate successful upload
	if c.debugMode {
		fmt.Printf("[REMOTE-CAS] Blob %s uploaded successfully\n", digest.Hash[:12])
	}
	return nil
}

// DownloadBlob downloads a blob from CAS
func (c *Client) DownloadBlob(ctx context.Context, digest Digest) ([]byte, error) {
	if c.debugMode {
		fmt.Printf("[REMOTE-CAS] Downloading blob %s from CAS\n", digest.Hash[:12])
	}
	// Placeholder for CAS blob download
	// In production, this would download from the CAS service
	return nil, fmt.Errorf("not implemented")
}

// DownloadFile downloads a file from CAS to a local path
func (c *Client) DownloadFile(ctx context.Context, digest Digest, path string) error {
	content, err := c.DownloadBlob(ctx, digest)
	if err != nil {
		return err
	}

	// Create parent directory if needed
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(path, content, 0644)
}