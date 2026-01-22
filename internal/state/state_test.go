package state_test

import (
	"testing"

	"github.com/buildbuddy-io/reninja/internal/eval_env"
	"github.com/buildbuddy-io/reninja/internal/state"
	"github.com/stretchr/testify/assert"
)

func TestBasic(t *testing.T) {
	s := state.New()

	command := &eval_env.EvalString{}
	command.AddText("cat ")
	command.AddSpecial("in")
	command.AddText(" > ")
	command.AddSpecial("out")

	rule := eval_env.NewRule("cat")
	rule.AddBinding("command", command)
	s.Bindings().AddRule(rule)

	edge := s.AddEdge(rule)
	s.AddExplicitIn("in1", edge)
	s.AddExplicitIn("in2", edge)
	s.AddOut("out", edge)

	assert.Equal(t, "cat in1 in2 > out", edge.EvaluateCommand(false))
	assert.False(t, s.GetNode("in1").Dirty())
	assert.False(t, s.GetNode("in2").Dirty())
	assert.False(t, s.GetNode("out").Dirty())
}
