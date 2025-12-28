package spawn

import (
	"time"

	"github.com/buildbuddy-io/gin/internal/exit_status"
	"github.com/buildbuddy-io/gin/internal/graph"
)

type EdgeEventType = int32

const (
	FindMissingDigests  EdgeEventType = iota // findMissingDigests
	UploadMissingInputs                      // upload missing inputs
	DownloadOutputs                          // download outputs
	UploadOutputs                            // upload
)

type EdgeMetadata struct {
	EventType EdgeEventType
	Start     time.Time
	End       time.Time
}

type Result struct {
	Edge   *graph.Edge
	Status exit_status.ExitStatusType
	Output string

	// Start and end timestamps.
	Start time.Time
	End   time.Time

	Runner   string
	CacheHit bool

	Metadata []EdgeMetadata
}

func (r Result) Success() bool {
	return r.Status == exit_status.ExitSuccess
}
