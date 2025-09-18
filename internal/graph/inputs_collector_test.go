package graph_test

import (
	"runtime"
	"testing"

	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/state"
	"github.com/buildbuddy-io/gin/internal/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGraphInputsCollector(t *testing.T) {
	// Build plan for the following graph:
	//
	//      in1
	//       |___________
	//       |           |
	//      ===         ===
	//       |           |
	//      out1        mid1
	//       |       ____|_____
	//       |      |          |
	//       |     ===      =======
	//       |      |       |     |
	//       |     out2    out3  out4
	//       |      |       |
	//      =======phony======
	//              |
	//             all
	//

	s := state.New()
	test.AddCatRule(t, s)
	test.AssertParse(t, `
build out1: cat in1
build mid1: cat in1
build out2: cat mid1
build out3 out4: cat mid1
build all: phony out1 out2 out3
`, s)
	collector := graph.NewInputsCollector()

	// Start visit from out1, this should add in1 to the inputs.
	collector.Reset()
	collector.VisitNode(s.GetNode("out1"))

	inputs := collector.GetInputsAsStrings(false)
	require.Equal(t, 1, len(inputs))
	assert.Equal(t, "in1", inputs[0])

	// Add a visit from out2, this should add mid1.
	collector.VisitNode(s.GetNode("out2"))
	inputs = collector.GetInputsAsStrings(false)
	require.Equal(t, 2, len(inputs))
	assert.Equal(t, "in1", inputs[0])
	assert.Equal(t, "mid1", inputs[1])

	// Another visit from all, this should add out1, out2 and out3,
	// but not out4.
	collector.VisitNode(s.GetNode("all"))
	inputs = collector.GetInputsAsStrings(false)
	require.Equal(t, 5, len(inputs))
	assert.Equal(t, "in1", inputs[0])
	assert.Equal(t, "mid1", inputs[1])
	assert.Equal(t, "out1", inputs[2])
	assert.Equal(t, "out2", inputs[3])
	assert.Equal(t, "out3", inputs[4])

	collector.Reset()

	// Starting directly from all, will add out1 before mid1 compared
	// to the previous example above.
	collector.VisitNode(s.GetNode("all"))
	inputs = collector.GetInputsAsStrings(false)
	require.Equal(t, 5, len(inputs))
	assert.Equal(t, "in1", inputs[0])
	assert.Equal(t, "out1", inputs[1])
	assert.Equal(t, "mid1", inputs[2])
	assert.Equal(t, "out2", inputs[3])
	assert.Equal(t, "out3", inputs[4])
}

func TestGraphInputsCollectorWithEscapes(t *testing.T) {
	s := state.New()
	test.AddCatRule(t, s)
	test.AssertParse(t, "build out$ 1: cat in1 in2 in$ with$ space | implicit || order_only\n", s)

	collector := graph.NewInputsCollector()
	collector.VisitNode(s.GetNode("out 1"))
	inputs := collector.GetInputsAsStrings(false)
	require.Equal(t, 5, len(inputs))
	assert.Equal(t, "in1", inputs[0])
	assert.Equal(t, "in2", inputs[1])
	assert.Equal(t, "in with space", inputs[2])
	assert.Equal(t, "implicit", inputs[3])
	assert.Equal(t, "order_only", inputs[4])

	inputs = collector.GetInputsAsStrings(true)
	require.Equal(t, 5, len(inputs))
	assert.Equal(t, "in1", inputs[0])
	assert.Equal(t, "in2", inputs[1])
	if runtime.GOOS == "windows" {
		assert.Equal(t, "\"in with space\"", inputs[2])
	} else {
		assert.Equal(t, "'in with space'", inputs[2])
	}
	assert.Equal(t, "implicit", inputs[3])
	assert.Equal(t, "order_only", inputs[4])
}
