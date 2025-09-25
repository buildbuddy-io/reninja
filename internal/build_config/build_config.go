package build_config

import (
	"github.com/buildbuddy-io/gin/internal/depfile_parser"
)

type VerbosityLevel int

const (
	Quiet VerbosityLevel = iota
	NoStatusUpdate
	Normal
	Verbose
)

// Config represents build configuration. This struct is here so it's
// accessible from status without causing dependency cycles.
type Config struct {
	Verbosity              VerbosityLevel
	DryRun                 bool
	Parallelism            int
	DisableJobserverClient bool
	FailuresAllowed        int
	MaxLoadAverage         float64
	DepfileParserOptions   depfile_parser.DepfileParserOptions
}
