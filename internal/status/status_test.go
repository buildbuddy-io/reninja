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
		{0, "; 0s"},
		{500, "; 0s"},
		{1000, "; 1s"},
		{6700, "; 6s"},
		{10000, "; 10s"},
		{59999, "; 59s"},
		{60000, "; 1m0s"},
		{90000, "; 1m30s"},
		{600000, "; 10m0s"},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%dms", c.ms), func(t *testing.T) {
			got := status.FormatTableElapsed(c.ms)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestStatusTableDisabledOnDumbTerminal(t *testing.T) {
	// With maxCommands > 0 but a non-smart terminal (test environment),
	// BuildEdgeStarted / BuildEdgeFinished should not panic.
	config := &build_config.Config{Parallelism: 2}
	p := status.NewPrinter(config)
	p.BuildStarted(time.Now())
	p.BuildFinished()
}
