// Copyright 2024 The Ninja-Go Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package graph

import (
	"fmt"
	"strings"
)

// TokenType represents the type of token in an EvalString
type TokenType int

const (
	RAW TokenType = iota
	SPECIAL
)

// Token represents a token in an EvalString
type Token struct {
	Value string
	Type  TokenType
}

// EvalString represents a string that may contain variable references
type EvalString struct {
	// Parsed representation: list of tokens
	parsed []Token
	// If we hold only a single RAW token with no variables, keep it here
	// for optimization (mirrors C++ implementation)
	singleToken string
}

// AddText adds literal text to the EvalString
func (es *EvalString) AddText(text string) {
	if len(es.parsed) == 0 {
		// First token and it's text - append to single token
		es.singleToken += text
	} else if len(es.parsed) > 0 && es.parsed[len(es.parsed)-1].Type == RAW {
		// Append to the last RAW token
		es.parsed[len(es.parsed)-1].Value += text
	} else {
		// Need to add a new RAW token
		es.parsed = append(es.parsed, Token{Value: text, Type: RAW})
	}
}

// AddSpecial adds a variable reference to the EvalString
func (es *EvalString) AddSpecial(varName string) {
	if len(es.parsed) == 0 && es.singleToken != "" {
		// Going from one to two tokens, so we can no longer apply
		// our single_token_ optimization and need to push everything
		// onto the vector.
		es.parsed = append(es.parsed, Token{Value: es.singleToken, Type: RAW})
		es.singleToken = ""
	}
	es.parsed = append(es.parsed, Token{Value: varName, Type: SPECIAL})
}

// Evaluate expands variables using the given environment
func (es *EvalString) Evaluate(env *BindingEnv) string {
	if len(es.parsed) == 0 {
		return es.singleToken
	}

	var result strings.Builder
	for _, token := range es.parsed {
		if token.Type == RAW {
			result.WriteString(token.Value)
		} else {
			result.WriteString(env.LookupVariable(token.Value))
		}
	}

	return result.String()
}

// Serialize returns the original string representation
func (es *EvalString) Serialize() string {
	var result strings.Builder
	if len(es.parsed) == 0 && es.singleToken != "" {
		result.WriteString("[")
		result.WriteString(es.singleToken)
		result.WriteString("]")
	} else {
		for _, token := range es.parsed {
			result.WriteString("[")
			if token.Type == SPECIAL {
				result.WriteByte('$')
			}
			result.WriteString(token.Value)
			result.WriteString("]")
		}
	}
	return result.String()
}

// BindingEnv represents a variable binding environment with scoping
type BindingEnv struct {
	parent   *BindingEnv
	Bindings map[string]string // Exported for parser access
	rules    map[string]*Rule
}

// NewBindingEnv creates a new binding environment
func NewBindingEnv(parent *BindingEnv) *BindingEnv {
	return &BindingEnv{
		parent:   parent,
		Bindings: make(map[string]string),
		rules:    make(map[string]*Rule),
	}
}

// AddBinding adds a variable binding
func (env *BindingEnv) AddBinding(key, value string) {
	env.Bindings[key] = value
}

// AddRule adds a rule to the environment
func (env *BindingEnv) AddRule(rule *Rule) {
	env.rules[rule.Name()] = rule
}

// LookupVariable looks up a variable value, checking parent scopes
func (env *BindingEnv) LookupVariable(key string) string {
	// Check special built-in variables
	switch key {
	case "in":
		return env.getBuiltinIn()
	case "out":
		return env.getBuiltinOut()
	case "in_newline":
		return env.getBuiltinInNewline()
	}

	// Check local bindings
	if value, ok := env.Bindings[key]; ok {
		return value
	}

	// Check parent scope
	if env.parent != nil {
		return env.parent.LookupVariable(key)
	}

	return ""
}

func (env *BindingEnv) GetRules() map[string]*Rule {
	return env.rules
}

// LookupRule looks up a rule by name
func (env *BindingEnv) LookupRule(name string) (*Rule, bool) {
	if rule, ok := env.rules[name]; ok {
		return rule, true
	}

	if env.parent != nil {
		return env.parent.LookupRule(name)
	}

	return nil, false
}

// getBuiltinIn returns the $in variable value (space-separated inputs)
func (env *BindingEnv) getBuiltinIn() string {
	// This would be set by the edge when evaluating commands
	if value, ok := env.Bindings["in"]; ok {
		return value
	}
	return ""
}

// getBuiltinOut returns the $out variable value (space-separated outputs)
func (env *BindingEnv) getBuiltinOut() string {
	// This would be set by the edge when evaluating commands
	if value, ok := env.Bindings["out"]; ok {
		return value
	}
	return ""
}

// getBuiltinInNewline returns the $in_newline variable value (newline-separated inputs)
func (env *BindingEnv) getBuiltinInNewline() string {
	in := env.getBuiltinIn()
	if in == "" {
		return ""
	}
	return strings.ReplaceAll(in, " ", "\n")
}

// EdgeEnv creates a new environment for edge evaluation that includes rule bindings
func EdgeEnv(parent *BindingEnv, edge *Edge) *BindingEnv {
	env := NewBindingEnv(parent)

	// Add edge-specific built-in variables
	var inputs []string
	for _, input := range edge.ExplicitInputs() {
		inputs = append(inputs, input.Path())
	}
	env.AddBinding("in", strings.Join(inputs, " "))

	var outputs []string
	for _, output := range edge.ExplicitOutputs() {
		outputs = append(outputs, output.Path())
	}
	env.AddBinding("out", strings.Join(outputs, " "))

	// Add all rule bindings to the environment
	// This makes them available for variable expansion
	if edge.Rule() != nil {
		for name, evalStr := range edge.Rule().Bindings() {
			// Skip command binding to avoid recursion
			if name != "command" {
				// Evaluate rule binding with current environment
				// This allows $rspfile to reference $out
				value := evalStr.Evaluate(env)
				env.AddBinding(name, value)
			}
		}
	}

	return env
}

// ShellEscape escapes a string for shell execution
func ShellEscape(s string) string {
	// Simple escaping for now - would need platform-specific implementation
	if strings.ContainsAny(s, " \t\n'\"\\$") {
		return fmt.Sprintf("'%s'", strings.ReplaceAll(s, "'", "'\\''"))
	}
	return s
}
