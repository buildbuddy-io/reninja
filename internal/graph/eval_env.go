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

// EvalString represents a string that may contain variable references
type EvalString struct {
	// Parsed representation: alternating literals and variable names
	// Even indices are literals, odd indices are variable names
	parsed []string
}

// NewEvalString creates a new EvalString from a raw string
func NewEvalString(raw string) EvalString {
	es := EvalString{}
	es.Parse(raw)
	return es
}

// Parse parses a string with variable references
func (es *EvalString) Parse(raw string) {
	es.parsed = nil
	if raw == "" {
		return
	}
	
	// Build parsed array with alternating literals and variables
	// Even indices are literals, odd indices are variable names
	var current strings.Builder
	
	i := 0
	for i < len(raw) {
		if raw[i] == '$' {
			if i+1 < len(raw) {
				next := raw[i+1]
				if next == '$' {
					// Escaped dollar sign
					current.WriteByte('$')
					i += 2
					continue
				} else if next == '{' {
					// Start of ${var} reference
					// Add current literal
					es.parsed = append(es.parsed, current.String())
					current.Reset()
					
					// Find closing brace
					j := i + 2
					for j < len(raw) && raw[j] != '}' {
						j++
					}
					if j < len(raw) {
						varName := raw[i+2:j]
						es.parsed = append(es.parsed, varName) // Add variable name
						i = j + 1
					} else {
						// Unclosed variable reference
						current.WriteString(raw[i:])
						break
					}
					continue
				} else if isVarNameChar(next) {
					// Start of $var reference
					// Add current literal
					es.parsed = append(es.parsed, current.String())
					current.Reset()
					
					// Find end of variable name
					j := i + 1
					for j < len(raw) && isVarNameChar(raw[j]) {
						j++
					}
					varName := raw[i+1:j]
					es.parsed = append(es.parsed, varName) // Add variable name
					i = j
					continue
				}
			}
		}
		
		current.WriteByte(raw[i])
		i++
	}
	
	// Add any remaining literal
	if current.Len() > 0 || len(es.parsed)%2 == 1 {
		es.parsed = append(es.parsed, current.String())
	}
	
	// Ensure we always end with a literal (even if empty)
	if len(es.parsed)%2 == 0 {
		es.parsed = append(es.parsed, "")
	}
}

func isVarNameChar(c byte) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') ||
		c == '_'
}

// Evaluate expands variables using the given environment
func (es *EvalString) Evaluate(env *BindingEnv) string {
	if len(es.parsed) == 0 {
		return ""
	}
	
	var result strings.Builder
	for i := 0; i < len(es.parsed); i += 2 {
		// Even indices are literals
		result.WriteString(es.parsed[i])
		
		// Odd indices are variable names
		if i+1 < len(es.parsed) && es.parsed[i+1] != "" {
			varValue := env.LookupVariable(es.parsed[i+1])
			result.WriteString(varValue)
		}
	}
	
	return result.String()
}

// Serialize returns the original string representation
func (es *EvalString) Serialize() string {
	if len(es.parsed) == 0 {
		return ""
	}
	
	var result strings.Builder
	for i := 0; i < len(es.parsed); i += 2 {
		// Even indices are literals
		literal := es.parsed[i]
		// Escape dollar signs in literals
		literal = strings.ReplaceAll(literal, "$", "$$")
		result.WriteString(literal)
		
		// Odd indices are variable names
		if i+1 < len(es.parsed) && es.parsed[i+1] != "" {
			result.WriteByte('$')
			varName := es.parsed[i+1]
			
			// Use ${} syntax if variable name contains special chars
			needsBraces := false
			for _, c := range varName {
				if !isVarNameChar(byte(c)) {
					needsBraces = true
					break
				}
			}
			
			if needsBraces {
				result.WriteString("{")
				result.WriteString(varName)
				result.WriteString("}")
			} else {
				result.WriteString(varName)
			}
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

// Expand expands all variables in a string
func (env *BindingEnv) Expand(str string) string {
	es := NewEvalString(str)
	return es.Evaluate(env)
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