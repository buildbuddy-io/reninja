package status_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/buildbuddy-io/reninja/internal/build_config"
	"github.com/buildbuddy-io/reninja/internal/status"
	"github.com/stretchr/testify/assert"
)

func TestStatusFormatElapsed(t *testing.T) {
	config := &build_config.Config{}
	status := status.NewPrinter(config)
	status.BuildStarted(time.Now())

	// Before any task is done, the elapsed time must be zero.
	assert.Equal(t, "[%/e0.000]", status.FormatProgressStatus("[%%/e%e]", 0))

	// Before any task is done, the elapsed time must be zero.
	assert.Equal(t, "[%/e00:00]", status.FormatProgressStatus("[%%/e%w]", 0))
}

func TestStatusFormatReplacePlaceholder(t *testing.T) {
	config := &build_config.Config{}
	status := status.NewPrinter(config)

	assert.Equal(t, "[%/s0/t0/r0/u0/f0]", status.FormatProgressStatus("[%%/s%s/t%t/r%r/u%u/f%f]", 0))
}

func TestFormatTableElapsed(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		// Negative → sentinel
		{-1, "??????"},
		// Zero
		{0, "  0.0s"},
		// Sub-second
		{500, "  0.5s"},
		// Seconds, one digit
		{1000, "  1.0s"},
		{6700, "  6.7s"},
		// Seconds, two digits — still right-justified to width 6
		{10000, " 10.0s"},
		{59999, " 59.9s"},
		// Exactly 60s transitions to minutes format
		{60000, "  1m0s"},
		{90000, " 1m30s"},
		// Large value, no truncation needed
		{600000, " 10m0s"},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%dms", c.ms), func(t *testing.T) {
			got := status.FormatTableElapsed(c.ms)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestStatusMaxCommandsDefault(t *testing.T) {
	t.Setenv("NINJA_STATUS_MAX_COMMANDS", "")
	// Empty string is not a valid integer → returns 0.
	assert.Equal(t, 0, status.StatusMaxCommands())
}

func TestStatusMaxCommandsPositive(t *testing.T) {
	t.Setenv("NINJA_STATUS_MAX_COMMANDS", "8")
	assert.Equal(t, 8, status.StatusMaxCommands())
}

func TestStatusMaxCommandsNegative(t *testing.T) {
	t.Setenv("NINJA_STATUS_MAX_COMMANDS", "-5")
	assert.Equal(t, 0, status.StatusMaxCommands())
}

func TestStatusTableDisabledOnDumbTerminal(t *testing.T) {
	// With maxCommands > 0 but a non-smart terminal (test environment),
	// BuildEdgeStarted / BuildEdgeFinished should not panic.
	t.Setenv("NINJA_STATUS_MAX_COMMANDS", "4")
	config := &build_config.Config{Parallelism: 2}
	p := status.NewPrinter(config)
	p.BuildStarted(time.Now())
	p.BuildFinished()
}
