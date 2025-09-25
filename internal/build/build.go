package build

import (
	"github.com/buildbuddy-io/gin/internal/depfile_parser"
	"github.com/buildbuddy-io/gin/internal/exit_status"
	"github.com/buildbuddy-io/gin/internal/graph"
)

type VerbosityLevel int

const (
	Quiet VerbosityLevel = iota
	NoStatusUpdate
	Normal
	Verbose
)

// Config represents build configuration
type Config struct {
	Verbosity              VerbosityLevel
	DryRun                 bool
	Parallelism            int
	DisableJobserverClient bool
	FailuresAllowed        int
	MaxLoadAverage         float64
	DepfileParserOptions   depfile_parser.DepfileParserOptions
}

type Result struct {
	Edge   *graph.Edge
	Status exit_status.ExitStatusType
	Output string
}

func (r Result) Success() bool {
	return r.Status == exit_status.ExitSuccess
}
