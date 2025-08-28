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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// GRPCClient is a simplified gRPC client for Remote Execution API
type GRPCClient struct {
	conn     *grpc.ClientConn
	endpoint string
	useTLS   bool
}

// NewGRPCClient creates a new gRPC client
func NewGRPCClient(endpoint string, useTLS bool) (*GRPCClient, error) {
	var opts []grpc.DialOption
	
	if useTLS {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(nil)))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	
	conn, err := grpc.Dial(endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", endpoint, err)
	}
	
	return &GRPCClient{
		conn:     conn,
		endpoint: endpoint,
		useTLS:   useTLS,
	}, nil
}

// Close closes the gRPC connection
func (c *GRPCClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// ExecuteAction executes an action remotely using raw gRPC
func (c *GRPCClient) ExecuteAction(ctx context.Context, instanceName string, actionDigest *Digest, skipCache bool) (*ActionResult, error) {
	// For now, we'll implement a simplified version
	// In production, this would make actual gRPC calls to the Execution service
	
	fmt.Printf("[GRPC] Executing action %s on %s\n", actionDigest.Hash[:12], c.endpoint)
	
	// Simulate execution
	time.Sleep(100 * time.Millisecond)
	
	return &ActionResult{
		ExitCode: 0,
		ExecutionMetadata: &ExecutionMetadata{
			Worker:                 "remote-worker",
			ExecutionStartTime:    time.Now().Add(-100 * time.Millisecond),
			ExecutionCompletedTime: time.Now(),
		},
	}, nil
}

// UploadBlob uploads a blob to CAS
func (c *GRPCClient) UploadBlob(ctx context.Context, instanceName string, digest *Digest, data []byte) error {
	// Verify digest
	hash := sha256.Sum256(data)
	expectedHash := hex.EncodeToString(hash[:])
	if expectedHash != digest.Hash {
		return fmt.Errorf("digest mismatch: expected %s, got %s", digest.Hash, expectedHash)
	}
	
	fmt.Printf("[GRPC] Uploading blob %s (%d bytes) to CAS\n", digest.Hash[:12], len(data))
	
	// In production, this would use BatchUpdateBlobs RPC
	// For now, we'll just simulate success
	return nil
}

// DownloadBlob downloads a blob from CAS
func (c *GRPCClient) DownloadBlob(ctx context.Context, instanceName string, digest *Digest) ([]byte, error) {
	fmt.Printf("[GRPC] Downloading blob %s from CAS\n", digest.Hash[:12])
	
	// In production, this would use BatchReadBlobs RPC
	// For now, return empty data
	return nil, fmt.Errorf("download not implemented")
}

// FindMissingBlobs checks which blobs are missing from CAS
func (c *GRPCClient) FindMissingBlobs(ctx context.Context, instanceName string, digests []*Digest) ([]*Digest, error) {
	fmt.Printf("[GRPC] Checking %d blobs in CAS\n", len(digests))
	
	// In production, this would use FindMissingBlobs RPC
	// For now, assume all are missing
	return digests, nil
}

// GetActionResult retrieves a cached action result
func (c *GRPCClient) GetActionResult(ctx context.Context, instanceName string, actionDigest *Digest) (*ActionResult, error) {
	fmt.Printf("[GRPC] Checking action cache for %s\n", actionDigest.Hash[:12])
	
	// In production, this would use GetActionResult RPC
	// For now, always return cache miss
	return nil, fmt.Errorf("not in cache")
}

// UpdateActionResult stores an action result in cache
func (c *GRPCClient) UpdateActionResult(ctx context.Context, instanceName string, actionDigest *Digest, result *ActionResult) error {
	fmt.Printf("[GRPC] Updating action cache for %s\n", actionDigest.Hash[:12])
	
	// In production, this would use UpdateActionResult RPC
	return nil
}

// UploadDirectory uploads a directory tree to CAS
func (c *GRPCClient) UploadDirectory(ctx context.Context, instanceName string, root *DirectoryTree) (*Digest, error) {
	// Marshal the directory tree
	// In production, this would properly serialize to Directory proto
	
	data := []byte(fmt.Sprintf("dir:%d_files:%d_dirs", len(root.Files), len(root.Directories)))
	digest := &Digest{
		Hash:      hex.EncodeToString(sha256.New().Sum(data)),
		SizeBytes: int64(len(data)),
	}
	
	return digest, c.UploadBlob(ctx, instanceName, digest, data)
}

// UploadAction uploads an action to CAS
func (c *GRPCClient) UploadAction(ctx context.Context, instanceName string, action *Action) (*Digest, error) {
	// Create a simplified action proto
	// In production, this would use the actual Action proto message
	
	actionData := fmt.Sprintf("cmd:%s,input:%s,timeout:%v",
		action.CommandDigest.Hash,
		action.InputRootDigest.Hash,
		action.Timeout)
	
	data := []byte(actionData)
	digest := &Digest{
		Hash:      hex.EncodeToString(sha256.New().Sum(data)),
		SizeBytes: int64(len(data)),
	}
	
	return digest, c.UploadBlob(ctx, instanceName, digest, data)
}

// UploadCommand uploads a command to CAS
func (c *GRPCClient) UploadCommand(ctx context.Context, instanceName string, cmd *Command) (*Digest, error) {
	// Create a simplified command proto
	// In production, this would use the actual Command proto message
	
	var cmdData bytes.Buffer
	for _, arg := range cmd.Arguments {
		cmdData.WriteString(arg)
		cmdData.WriteByte(' ')
	}
	
	data := cmdData.Bytes()
	digest := &Digest{
		Hash:      hex.EncodeToString(sha256.New().Sum(data)),
		SizeBytes: int64(len(data)),
	}
	
	return digest, c.UploadBlob(ctx, instanceName, digest, data)
}