package grpc_client

import (
	"context"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/buildbuddy-io/reninja/internal/statuserr"
	"google.golang.org/grpc"
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
