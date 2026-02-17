package grpc_client

import (
	"context"
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/buildbuddy-io/reninja/internal/statuserr"
	"github.com/buildbuddy-io/reninja/internal/util"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/experimental"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/mem"
)

// Default protocol to use when a target is missing a protocol.
const defaultProtocol = "grpcs://"

func normalizeTarget(target string) string {
	if strings.Contains(target, "://") {
		return target
	}
	return defaultProtocol + target
}

func DialSimple(ctx context.Context, target string) (*grpc.ClientConn, error) {
	dialOptions := []grpc.DialOption{
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(math.MaxInt32)),
		experimental.WithBufferPool(mem.DefaultBufferPool()),
		grpc.WithSharedWriteBuffer(true),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			// After a duration of this time if the client doesn't see any activity it
			// pings the server to see if the transport is still alive.
			Time: 30 * time.Second,

			// After having pinged for keepalive check, the client waits for a duration
			// of Timeout and if no activity is seen even after that the connection is
			// closed.
			Timeout: 20 * time.Second,

			// If true, client sends keepalive pings even with no active RPCs.
			PermitWithoutStream: true,
		}),
	}
	target = normalizeTarget(target)

	u, err := url.Parse(target)
	if err == nil {
		if u.Scheme == "grpcs" {
			dialOptions = append(dialOptions, grpc.WithTransportCredentials(credentials.NewTLS(nil)))
		} else {
			dialOptions = append(dialOptions, grpc.WithInsecure())
		}

		if u.Scheme == "grpcs" && u.Port() == "" {
			u.Host += ":443"
		}

		if u.Scheme != "unix" {
			target = u.Host
		}
	}

	conn, err := grpc.DialContext(ctx, target, dialOptions...)
	if err != nil {
		return nil, statuserr.WrapError(err, "error dialing")
	}
	return conn, err
}

var (
	idsMu sync.Mutex
	ids   = map[string]int{}
)

// DialSimpleWithPoolSize is like DialSimple, but with a specified pool size
// instead of the default.
func DialSimpleWithPoolSize(ctx context.Context, target string, poolSize int) (*ClientConnPool, error) {
	var mu sync.Mutex
	var conns []*clientConn

	eg, gCtx := errgroup.WithContext(ctx)
	for range poolSize {
		eg.Go(func() error {
			conn, err := DialSimple(gCtx, target)
			if err != nil {
				return err
			}
			mu.Lock()
			conns = append(conns, &clientConn{ClientConn: conn, index: strconv.Itoa(len(conns))})
			mu.Unlock()
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	// Increment an index per-target to disambiguate between multiple
	// connection pools to the same target.
	idsMu.Lock()
	id := ids[target]
	ids[target] = id + 1
	idsMu.Unlock()

	return &ClientConnPool{targetForLogging: target, id: strconv.Itoa(id), conns: conns}, nil
}

type clientConn struct {
	*grpc.ClientConn
	index        string
	wasEverReady atomic.Bool
}

type ClientConnPool struct {
	targetForLogging string
	id               string
	conns            []*clientConn
	idx              atomic.Uint64
}

func (p *ClientConnPool) Check(ctx context.Context) error {
	goodConns := 0
	for _, c := range p.conns {
		connState := c.GetState()
		if connState == connectivity.Ready {
			goodConns++
			continue
		}
		if connState == connectivity.Idle {
			c.Connect()
			goodConns++
			continue
		}
	}
	if goodConns == 0 {
		return statuserr.UnavailableError("No ready connections in gRPC connection pool")
	}
	return nil
}

func (p *ClientConnPool) Close() error {
	for _, c := range p.conns {
		// In practice, this only errors out if you call Close twice.
		if err := c.Close(); err != nil {
			util.Warningf("could not close connection: %s", err)
		}
	}
	return nil
}

func (p *ClientConnPool) getConn() *clientConn {
	idx := p.idx.Add(1)
	return p.conns[idx%uint64(len(p.conns))]
}

func (p *ClientConnPool) WaitForConn() *grpc.ClientConn {
	return p.getConn().ClientConn
}

func (p *ClientConnPool) Invoke(ctx context.Context, method string, args any, reply any, opts ...grpc.CallOption) error {
	conn := p.getConn()
	return conn.Invoke(ctx, method, args, reply, opts...)
}

func (p *ClientConnPool) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	conn := p.getConn()
	stream, err := conn.NewStream(ctx, desc, method, opts...)
	if err != nil {
		return stream, err
	}
	return stream, nil
}
