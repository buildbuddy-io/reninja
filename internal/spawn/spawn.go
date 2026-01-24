package spawn

import (
	"time"

	"github.com/buildbuddy-io/reninja/internal/exit_status"
	"github.com/buildbuddy-io/reninja/internal/graph"
	"github.com/buildbuddy-io/reninja/internal/span"

	repb "github.com/buildbuddy-io/reninja/genproto/remote_execution"
)

type Result struct {
	Edge   *graph.Edge
	Status exit_status.ExitStatusType
	Output string

	// Start and end timestamps.
	Start time.Time
	End   time.Time

	Runner   string
	CacheHit bool

	Events []span.Event

	Outputs      []*repb.OutputFile
	StdoutDigest *repb.Digest
}

func (r Result) Success() bool {
	return r.Status == exit_status.ExitSuccess
}
