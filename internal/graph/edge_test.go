package graph_test

import (
	"testing"

	"github.com/buildbuddy-io/reninja/internal/graph"
	"github.com/buildbuddy-io/reninja/internal/priority_queue"
	"github.com/buildbuddy-io/reninja/internal/state"
	"github.com/buildbuddy-io/reninja/internal/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEdgeQueuePriority(t *testing.T) {
	s := state.New()
	test.AddCatRule(t, s)
	test.AssertParse(t, "build out1: cat in\nbuild out2: cat in\nbuild out3: cat in\n", s)

	e1 := s.GetNode("out1").InEdge()
	e2 := s.GetNode("out2").InEdge()
	e3 := s.GetNode("out3").InEdge()

	// Set critical path weights: higher weight = higher priority (comes out first).
	e1.SetCriticalPathWeight(1)
	e2.SetCriticalPathWeight(3)
	e3.SetCriticalPathWeight(2)

	queue := priority_queue.New[*graph.Edge](graph.EdgePriorityLess)
	queue.Push(e1)
	queue.Push(e2)
	queue.Push(e3)

	v, ok := queue.Pop()
	require.True(t, ok)
	assert.Equal(t, e2, v)

	v, ok = queue.Pop()
	require.True(t, ok)
	assert.Equal(t, e3, v)

	v, ok = queue.Pop()
	require.True(t, ok)
	assert.Equal(t, e1, v)

	_, ok = queue.Pop()
	assert.False(t, ok)
}
