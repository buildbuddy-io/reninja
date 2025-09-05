package eval_env_test

import (
	"testing"

	"github.com/buildbuddy-io/gin/internal/eval_env"
	"github.com/stretchr/testify/require"
)

func TestEvalStringAddTextAddSpecial(t *testing.T) {
	tests := []struct {
		name     string
		actions  []func(*eval_env.EvalString)
		expected string
	}{
		{
			name: "single text token",
			actions: []func(*eval_env.EvalString){
				func(es *eval_env.EvalString) { es.AddText("hello world") },
			},
			expected: "[hello world]",
		},
		{
			name: "text then variable",
			actions: []func(*eval_env.EvalString){
				func(es *eval_env.EvalString) { es.AddText("plain text ") },
				func(es *eval_env.EvalString) { es.AddSpecial("var") },
			},
			expected: "[plain text ][$var]",
		},
		{
			name: "variable then text",
			actions: []func(*eval_env.EvalString){
				func(es *eval_env.EvalString) { es.AddSpecial("var") },
				func(es *eval_env.EvalString) { es.AddText(" plain text") },
			},
			expected: "[$var][ plain text]",
		},
		{
			name: "multiple texts merge",
			actions: []func(*eval_env.EvalString){
				func(es *eval_env.EvalString) { es.AddText("hello") },
				func(es *eval_env.EvalString) { es.AddText(" ") },
				func(es *eval_env.EvalString) { es.AddText("world") },
			},
			expected: "[hello world]",
		},
		{
			name: "complex pattern matching C++ test",
			actions: []func(*eval_env.EvalString){
				func(es *eval_env.EvalString) { es.AddText("plain text ") },
				func(es *eval_env.EvalString) { es.AddSpecial("var") },
				func(es *eval_env.EvalString) { es.AddText(" ") },
				func(es *eval_env.EvalString) { es.AddSpecial("VaR") },
				func(es *eval_env.EvalString) { es.AddText(" ") },
				func(es *eval_env.EvalString) { es.AddSpecial("x") },
			},
			expected: "[plain text ][$var][ ][$VaR][ ][$x]",
		},
		{
			name: "escape sequences matching C++ test",
			actions: []func(*eval_env.EvalString){
				func(es *eval_env.EvalString) { es.AddText(" $ab c: cde") },
			},
			expected: "[ $ab c: cde]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			es := &eval_env.EvalString{}
			for _, action := range tt.actions {
				action(es)
			}
			require.Equal(t, tt.expected, es.Serialize())
		})
	}
}

func TestEvalStringEvaluate(t *testing.T) {
	env := eval_env.NewBindingEnv(nil)
	env.AddBinding("foo", "bar")
	env.AddBinding("baz", "qux")

	tests := []struct {
		name     string
		actions  []func(*eval_env.EvalString)
		expected string
	}{
		{
			name: "single text",
			actions: []func(*eval_env.EvalString){
				func(es *eval_env.EvalString) { es.AddText("hello") },
			},
			expected: "hello",
		},
		{
			name: "text with variable",
			actions: []func(*eval_env.EvalString){
				func(es *eval_env.EvalString) { es.AddText("value: ") },
				func(es *eval_env.EvalString) { es.AddSpecial("foo") },
			},
			expected: "value: bar",
		},
		{
			name: "multiple variables",
			actions: []func(*eval_env.EvalString){
				func(es *eval_env.EvalString) { es.AddSpecial("foo") },
				func(es *eval_env.EvalString) { es.AddText(" and ") },
				func(es *eval_env.EvalString) { es.AddSpecial("baz") },
			},
			expected: "bar and qux",
		},
		{
			name: "undefined variable",
			actions: []func(*eval_env.EvalString){
				func(es *eval_env.EvalString) { es.AddText("value: ") },
				func(es *eval_env.EvalString) { es.AddSpecial("undefined") },
			},
			expected: "value: ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			es := &eval_env.EvalString{}
			for _, action := range tt.actions {
				action(es)
			}
			require.Equal(t, tt.expected, es.Evaluate(env))
		})
	}
}
