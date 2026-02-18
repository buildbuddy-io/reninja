package spawn

import (
	"context"
	"time"

	"github.com/buildbuddy-io/reninja/internal/exit_status"
	"github.com/buildbuddy-io/reninja/internal/graph"

	repb "github.com/buildbuddy-io/reninja/genproto/remote_execution"
)

type Result struct {
	Edge   *graph.Edge
	Status exit_status.ExitStatusType
	Output string

	// Start and end timestamps.
	Start time.Time
	End   time.Time

	// The runner ("local", "remote-cache", "remote"), and
	Runner string
	// whether or not this Result was read from cache.
	CacheHit bool

	// Context to be used for IO and tracing.
	Context context.Context

	// Files generated from this Edge and Stdout, only present
	// for cached or remotely run edges.
	Outputs        []*repb.OutputFile
	OutputSymlinks []*repb.OutputSymlink
	StdoutDigest   *repb.Digest
}

func (r Result) Success() bool {
	return r.Status == exit_status.ExitSuccess
}
