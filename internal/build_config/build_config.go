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

func Create() Config {
	return Config{
		Verbosity:              Normal,
		DryRun:                 false,
		Parallelism:            1,
		DisableJobserverClient: false,
		FailuresAllowed:        1,
		MaxLoadAverage:         0,
	}
}
