package spawn

import (
	"time"

	"github.com/buildbuddy-io/gin/internal/exit_status"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/span"
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
}

func (r Result) Success() bool {
	return r.Status == exit_status.ExitSuccess
}
