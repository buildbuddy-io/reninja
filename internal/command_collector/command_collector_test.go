package command_collector_test

import (
	"testing"

	"github.com/buildbuddy-io/reninja/internal/command_collector"
	"github.com/buildbuddy-io/reninja/internal/state"
	"github.com/buildbuddy-io/reninja/internal/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandCollector(t *testing.T) {
	s := state.New()
	test.AddCatRule(t, s)
	test.AssertParse(t, `
build out1: cat in1
build mid1: cat in1
build out2: cat mid1
build out3 out4: cat mid1
build all: phony out1 out2 out3
`, s)
	{
		collector := command_collector.New()

		// Start visit from out2; this should add `build mid1` and `build out2` to
		// the edge list.
		collector.CollectFrom(s.GetNode("out2"))
		require.Equal(t, 2, len(collector.InEdges()))
		edges := collector.InEdges()
		edges[0].Dump("edge-0")
		assert.Equal(t, "cat in1 > mid1", edges[0].EvaluateCommand(false))
		assert.Equal(t, "cat mid1 > out2", edges[1].EvaluateCommand(false))

		// Add a visit from out1, this should append `build out1`
		collector.CollectFrom(s.GetNode("out1"))
		require.Equal(t, 3, len(collector.InEdges()))
		edges = collector.InEdges()
		assert.Equal(t, "cat in1 > out1", edges[2].EvaluateCommand(false))

		// Another visit from all; this should add edges for out1, out2 and out3,
		// but not all (because it's phony).
		collector.CollectFrom(s.GetNode("all"))
		require.Equal(t, 4, len(collector.InEdges()))
		edges = collector.InEdges()
		assert.Equal(t, "cat in1 > mid1", edges[0].EvaluateCommand(false))
		assert.Equal(t, "cat mid1 > out2", edges[1].EvaluateCommand(false))
		assert.Equal(t, "cat in1 > out1", edges[2].EvaluateCommand(false))
		assert.Equal(t, "cat mid1 > out3 out4", edges[3].EvaluateCommand(false))
	}

	{
		collector := command_collector.New()

		// Starting directly from all, will add `build out1` before `build mid1`
		// compared to the previous example above.
		collector.CollectFrom(s.GetNode("all"))
		require.Equal(t, 4, len(collector.InEdges()))
		edges := collector.InEdges()
		assert.Equal(t, "cat in1 > out1", edges[0].EvaluateCommand(false))
		assert.Equal(t, "cat in1 > mid1", edges[1].EvaluateCommand(false))
		assert.Equal(t, "cat mid1 > out2", edges[2].EvaluateCommand(false))
		assert.Equal(t, "cat mid1 > out3 out4", edges[3].EvaluateCommand(false))
	}
}
