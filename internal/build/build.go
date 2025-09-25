package build

import (
	"github.com/buildbuddy-io/gin/internal/build_config"
	"github.com/buildbuddy-io/gin/internal/build_log"
	"github.com/buildbuddy-io/gin/internal/deps_log"
	"github.com/buildbuddy-io/gin/internal/exit_status"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/state"
)

type Result struct {
	Edge   *graph.Edge
	Status exit_status.ExitStatusType
	Output string
}

func (r Result) Success() bool {
	return r.Status == exit_status.ExitSuccess
}

type Builder struct {
	state *state.State
	config build_config.Config
	buildLog *build_log.BuildLog
	depsLog *deps_log.DepsLog
}
	
