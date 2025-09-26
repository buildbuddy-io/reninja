package status_test

import (
	"testing"

	"github.com/buildbuddy-io/gin/internal/build_config"
	"github.com/buildbuddy-io/gin/internal/status"
	"github.com/stretchr/testify/assert"
)

func TestStatusFormatElapsed(t *testing.T) {
	config := build_config.Config{}
	status := status.NewPrinter(config)
	status.BuildStarted()

	// Before any task is done, the elapsed time must be zero.
	assert.Equal(t, "[%/e0.000]", status.FormatProgressStatus("[%%/e%e]", 0))

	// Before any task is done, the elapsed time must be zero.
	assert.Equal(t, "[%/e00:00]", status.FormatProgressStatus("[%%/e%w]", 0))
}

func TestStatusFormatReplacePlaceholder(t *testing.T) {
	config := build_config.Config{}
	status := status.NewPrinter(config)

	assert.Equal(t, "[%/s0/t0/r0/u0/f0]", status.FormatProgressStatus("[%%/s%s/t%t/r%r/u%u/f%f]", 0))
}
