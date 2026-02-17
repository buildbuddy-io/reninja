// package remote_exec provides utility functions for remote execution clients.
package remote_exec

import (
	"context"
	"sync"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	"github.com/buildbuddy-io/reninja/internal/digest"
	"github.com/buildbuddy-io/reninja/internal/grpc_client"
	"github.com/buildbuddy-io/reninja/internal/remote_flags"
	"github.com/buildbuddy-io/reninja/internal/remote_headers"
	"github.com/buildbuddy-io/reninja/internal/retry"
	"github.com/buildbuddy-io/reninja/internal/statuserr"
	"github.com/buildbuddy-io/reninja/internal/util"
	"google.golang.org/grpc/metadata"

	repb "github.com/buildbuddy-io/reninja/genproto/remote_execution"
	gstatus "google.golang.org/grpc/status"
)

var (
	once            sync.Once
	defaultExecutor *Executor
)

func initializeClients() {
	once.Do(func() {
		if remote_flags.RemoteCache() == "" {
			return
		}
		if remote_flags.RemoteExecutor() == "" {
			return
		}
		conn, err := grpc_client.DialSimpleWithPoolSize(context.TODO(), remote_flags.RemoteExecutor(), 10)
		if err != nil {
			util.Errorf("error dialing remote execution service: %s", err)
			return
		}
		reClient := repb.NewExecutionClient(conn)
		defaultExecutor = &Executor{reClient}
	})
}

func DefaultExecutor() *Executor {
	initializeClients()
	return defaultExecutor
}

type Executor struct {
	repb.ExecutionClient
}

func appendHeadersToCtx(ctx context.Context) context.Context {
	extraHeaders := remote_headers.GetPairs()
	if len(extraHeaders) == 0 {
		return ctx
	}
	ctx = metadata.AppendToOutgoingContext(ctx, extraHeaders...)
	return ctx
}

// Start begins an Execute stream for the given remote action.
func (e *Executor) Start(ctx context.Context, r *digest.CASResourceName) (*RetryingStream, error) {
	ctx = appendHeadersToCtx(ctx)
	req := &repb.ExecuteRequest{
		InstanceName:    r.GetInstanceName(),
		ActionDigest:    r.GetDigest(),
		DigestFunction:  r.GetDigestFunction(),
		SkipCacheLookup: true,
	}
	stream, err := e.ExecutionClient.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	return NewRetryingStream(ctx, e.ExecutionClient, stream, ""), nil
}

// Wait waits for command execution to complete, and returns the COMPLETE stage
// operation response.
func Wait(stream *RetryingStream) (*Response, error) {
	for {
		op, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		if op.Done {
			return op, nil
		}
	}
}

// RetryingStream implements a reliable operation stream.
//
// It keeps track of the operation name internally, and provides a Recv() func
// which re-establishes the stream transparently if the operation name has been
// established.
type RetryingStream struct {
	ctx    context.Context
	cancel context.CancelFunc
	client repb.ExecutionClient
	stream repb.Execution_ExecuteClient
	name   string
}

func NewRetryingStream(ctx context.Context, client repb.ExecutionClient, stream repb.Execution_ExecuteClient, name string) *RetryingStream {
	ctx, cancel := context.WithCancel(ctx)
	return &RetryingStream{
		ctx:    ctx,
		cancel: cancel,
		client: client,
		stream: stream,
		name:   name,
	}
}

// Name returns the operation name, if known.
func (s *RetryingStream) Name() string {
	return s.name
}

// Recv attempts to reliably return the next operation on the named stream.
//
// If the stream is disconnected and the operation name has been received, it
// will attempt to reconnect with WaitExecution.
func (s *RetryingStream) Recv() (*Response, error) {
	r := retry.DefaultWithContext(s.ctx)
	for {
		op, err := s.stream.Recv()
		if err == nil {
			if op.GetName() != "" {
				s.name = op.GetName()
			}
			return UnpackOperation(op)
		}
		if !statuserr.IsUnavailableError(err) || s.name == "" {
			return nil, err
		}
		if !r.Next() {
			return nil, s.ctx.Err()
		}
		req := &repb.WaitExecutionRequest{Name: s.name}
		next, err := s.client.WaitExecution(s.ctx, req)
		if err != nil {
			return nil, err
		}
		s.stream.CloseSend()
		s.stream = next
	}
}

func (s *RetryingStream) CloseSend() error {
	var err error
	if s.stream != nil {
		err = s.stream.CloseSend()
		s.stream = nil
	}
	s.client = nil
	s.cancel()
	return err
}

// Response contains an operation along with its execution-specific payload.
type Response struct {
	*longrunningpb.Operation

	// ExecuteOperationMetadata contains any metadata unpacked from the
	// operation.
	ExecuteOperationMetadata *repb.ExecuteOperationMetadata
	// ExecuteResponse contains any response unpacked from the operation.
	ExecuteResponse *repb.ExecuteResponse
	// Err contains any error parsed from the ExecuteResponse status field.
	Err error
}

// UnpackOperation unmarshals all expected execution-specific fields from the
// given operationn.
func UnpackOperation(op *longrunningpb.Operation) (*Response, error) {
	msg := &Response{Operation: op}
	if op.GetResponse() != nil {
		msg.ExecuteResponse = &repb.ExecuteResponse{}
		if err := op.GetResponse().UnmarshalTo(msg.ExecuteResponse); err != nil {
			return nil, err
		}
	}
	if op.GetMetadata() != nil {
		msg.ExecuteOperationMetadata = &repb.ExecuteOperationMetadata{}
		if err := op.GetMetadata().UnmarshalTo(msg.ExecuteOperationMetadata); err != nil {
			return nil, err
		}
	}
	msg.Err = gstatus.FromProto(msg.ExecuteResponse.GetStatus()).Err()
	return msg, nil
}
