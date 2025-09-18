package eval_env

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

func (es *EvalString) Clone() *EvalString {
	clone := &EvalString{
		parsed:      append([]Token{}, es.parsed...),
		singleToken: es.singleToken,
	}
	return clone
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

func (es *EvalString) Empty() bool {
	if es != nil {
		return len(es.parsed) == 0 && es.singleToken == ""
	}
	return true
}

func (es *EvalString) Clear() {
	es.singleToken = ""
	es.parsed = es.parsed[:0]
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
func (es *EvalString) Evaluate(env Env) string {
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

type Env interface {
	LookupVariable(key string) string
}

// Rule represents a build rule template
type Rule struct {
	name     string
	bindings map[string]*EvalString
	phony    bool
}

// NewRule creates a new Rule
func NewRule(name string) *Rule {
	return &Rule{
		name:     name,
		bindings: make(map[string]*EvalString),
		phony:    false,
	}
}

// Name returns the rule name
func (r *Rule) Name() string {
	return r.name
}

// IsPhony returns whether this is a phony rule
func (r *Rule) IsPhony() bool {
	return r.phony
}

// SetPhony sets whether this is a phony rule
func (r *Rule) SetPhony(phony bool) {
	r.phony = phony
}

// AddBinding adds a variable binding to the rule
func (r *Rule) AddBinding(key string, value *EvalString) {
	r.bindings[key] = value
}

// GetBinding returns a binding value
func (r *Rule) GetBinding(key string) (*EvalString, bool) {
	val, ok := r.bindings[key]
	return val, ok
}

// Bindings returns all bindings
func (r *Rule) Bindings() map[string]*EvalString {
	return r.bindings
}

func IsReservedBinding(key string) bool {
	return key == "command" ||
		key == "depfile" ||
		key == "dyndep" ||
		key == "description" ||
		key == "deps" ||
		key == "generator" ||
		key == "pool" ||
		key == "restat" ||
		key == "rspfile" ||
		key == "rspfile_content" ||
		key == "msvc_deps_prefix"
}

var _ Env = &BindingEnv{}

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
	if _, ok := env.LookupRuleCurrentScope(rule.Name()); ok {
		panic("AddRule attempting to duplicate rule in current scope")
	}
	env.rules[rule.Name()] = rule
}

// LookupVariable looks up a variable value, checking parent scopes
func (env *BindingEnv) LookupVariable(key string) string {
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

func (env *BindingEnv) LookupWithFallback(k string, eval *EvalString, otherEnv Env) string {
	if v, ok := env.Bindings[k]; ok {
		return v
	}

	if eval != nil {
		return eval.Evaluate(otherEnv)
	}

	if env.parent != nil {
		return env.parent.LookupVariable(k)
	}
	return ""
}

func (env *BindingEnv) LookupRuleCurrentScope(name string) (*Rule, bool) {
	if rule, ok := env.rules[name]; ok {
		return rule, true
	}

	return nil, false
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

// ShellEscape escapes a string for shell execution
func ShellEscape(s string) string {
	// Simple escaping for now - would need platform-specific implementation
	if strings.ContainsAny(s, " \t\n'\"\\$") {
		return fmt.Sprintf("'%s'", strings.ReplaceAll(s, "'", "'\\''"))
	}
	return s
}
