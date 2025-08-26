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

package parser

import (
	"fmt"
	"io/ioutil"
	"path/filepath"

	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/lexer"
	"github.com/buildbuddy-io/gin/internal/state"
)

// ManifestParser parses ninja build files
type ManifestParser struct {
	state    *state.State
	scanner  *lexer.Scanner
	env      *graph.BindingEnv
	filename string
	quiet    bool
}

// New creates a new ManifestParser
func New(s *state.State) *ManifestParser {
	return &ManifestParser{
		state:   s,
		scanner: lexer.NewScanner(),
		env:     s.Bindings(),
	}
}

// ParseFile parses a ninja build file
func (p *ManifestParser) ParseFile(filename string) error {
	content, err := ioutil.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("loading '%s': %w", filename, err)
	}

	p.filename = filename
	return p.Parse(filename, string(content))
}

// Parse parses ninja build file content
func (p *ManifestParser) Parse(filename, input string) error {
	p.filename = filename
	p.scanner.Init(filename, input)

	for !p.scanner.IsEOF() {
		token := p.scanner.NextToken()

		switch token {
		case lexer.BUILD:
			if err := p.parseBuild(); err != nil {
				return err
			}

		case lexer.RULE:
			if err := p.parseRule(); err != nil {
				return err
			}

		case lexer.POOL:
			if err := p.parsePool(); err != nil {
				return err
			}

		case lexer.DEFAULT:
			if err := p.parseDefault(); err != nil {
				return err
			}

		case lexer.IDENT:
			// We've already consumed the IDENT token, need to unread it
			p.scanner.Lexer.UnreadToken()
			if err := p.parseVariable(); err != nil {
				return err
			}

		case lexer.INCLUDE:
			if err := p.parseInclude(); err != nil {
				return err
			}

		case lexer.SUBNINJA:
			if err := p.parseSubninja(); err != nil {
				return err
			}

		case lexer.NEWLINE:
			// Empty line, continue

		case lexer.EOF:
			return nil

		default:
			return p.scanner.Error(fmt.Sprintf("unexpected token %s", lexer.TokenName(token)))
		}
	}

	return nil
}

// parseBuild parses a build edge
func (p *ManifestParser) parseBuild() error {
	// build outputs: rule inputs
	var outputs []string
	var implicitOuts int

	// Parse outputs
	for {
		path, err := p.scanner.ReadPath()
		if err != nil {
			return err
		}

		evaluated := path.Evaluate(p.env)
		if evaluated == "" {
			// No more outputs
			break
		}

		outputs = append(outputs, evaluated)

		// Check for implicit outputs
		if p.scanner.PeekToken(lexer.PIPE) {
			p.scanner.NextToken() // consume |
			implicitOuts = p.parseOutputs(&outputs)
			break
		}

		if p.scanner.PeekToken(lexer.COLON) {
			break
		}
	}

	if len(outputs) == 0 {
		return p.scanner.Error("expected output files")
	}

	// Expect colon
	if err := p.scanner.ExpectToken(lexer.COLON); err != nil {
		return err
	}

	// Parse rule name
	ruleName, err := p.scanner.ExpectIdent()
	if err != nil {
		return err
	}

	rule := p.state.LookupRule(ruleName)
	if rule == nil {
		return p.scanner.Error(fmt.Sprintf("unknown rule '%s'", ruleName))
	}

	// Create edge
	edge := graph.NewEdge()
	edge.SetRule(rule)
	edge.SetImplicitOuts(implicitOuts)

	// Add outputs to edge
	for _, output := range outputs {
		node := p.state.GetNode(output)
		edge.AddOutput(node)
	}

	// Parse inputs
	var implicitDeps, orderOnlyDeps int
	for {
		// Check for end of line or special tokens first
		if p.scanner.PeekToken(lexer.NEWLINE) || p.scanner.IsEOF() {
			break
		}
		
		if p.scanner.PeekToken(lexer.PIPE) || p.scanner.PeekToken(lexer.PIPE2) || p.scanner.PeekToken(lexer.PIPEAT) {
			// Will be handled below
			break
		}
		
		path, err := p.scanner.ReadPath()
		if err != nil {
			return err
		}

		evaluated := path.Evaluate(p.env)
		if evaluated == "" {
			// No more inputs
			break
		}

		node := p.state.GetNode(evaluated)
		edge.AddInput(node)
	}
	
	// Check for implicit deps (|) or order-only deps (||)
	if p.scanner.PeekToken(lexer.PIPE) {
		p.scanner.NextToken()
		implicitDeps = p.parseInputs(edge)
	}

	if p.scanner.PeekToken(lexer.PIPE2) {
		p.scanner.NextToken()
		orderOnlyDeps = p.parseInputs(edge)
	}

	if p.scanner.PeekToken(lexer.PIPEAT) {
		p.scanner.NextToken()
		if err := p.parseValidations(edge); err != nil {
			return err
		}
	}

	edge.SetImplicitDeps(implicitDeps)
	edge.SetOrderOnlyDeps(orderOnlyDeps)

	// Expect newline
	if err := p.expectNewline(); err != nil {
		return err
	}

	// Parse edge bindings
	edgeEnv := graph.NewBindingEnv(p.env)
	if err := p.parseBindings(edgeEnv); err != nil {
		return err
	}
	edge.SetEnv(edgeEnv)

	// Check for pool - first check edge bindings, then rule bindings
	poolName := ""
	if name := edgeEnv.LookupVariable("pool"); name != "" {
		poolName = name
	} else if rule != nil {
		// Check rule bindings for pool
		if poolBinding, ok := rule.GetBinding("pool"); ok {
			poolName = poolBinding.Evaluate(edgeEnv)
		}
	}
	
	if poolName != "" {
		pool := p.state.LookupPool(poolName)
		if pool == nil {
			return fmt.Errorf("unknown pool '%s'", poolName)
		}
		edge.SetPool(pool)
	}

	// Add edge to state
	p.state.AddEdge(edge)

	return nil
}

// parseOutputs parses additional outputs after |
func (p *ManifestParser) parseOutputs(outputs *[]string) int {
	count := 0
	for {
		path, err := p.scanner.ReadPath()
		if err != nil {
			break
		}

		evaluated := path.Evaluate(p.env)
		if evaluated == "" {
			break
		}

		*outputs = append(*outputs, evaluated)
		count++

		if p.scanner.PeekToken(lexer.COLON) {
			break
		}
	}
	return count
}

// parseInputs parses additional inputs after | or ||
func (p *ManifestParser) parseInputs(edge *graph.Edge) int {
	count := 0
	for {
		path, err := p.scanner.ReadPath()
		if err != nil {
			break
		}

		evaluated := path.Evaluate(p.env)
		if evaluated == "" {
			break
		}

		node := p.state.GetNode(evaluated)
		edge.AddInput(node)
		count++

		if p.scanner.PeekToken(lexer.PIPE) || p.scanner.PeekToken(lexer.PIPE2) ||
			p.scanner.PeekToken(lexer.PIPEAT) || p.scanner.PeekToken(lexer.NEWLINE) {
			break
		}
	}
	return count
}

// parseValidations parses validation outputs after |@
func (p *ManifestParser) parseValidations(edge *graph.Edge) error {
	for {
		path, err := p.scanner.ReadPath()
		if err != nil {
			return err
		}

		evaluated := path.Evaluate(p.env)
		if evaluated == "" {
			break
		}

		node := p.state.GetNode(evaluated)
		edge.AddValidation(node)

		if p.scanner.PeekToken(lexer.NEWLINE) || p.scanner.IsEOF() {
			break
		}
	}
	return nil
}

// parseRule parses a rule definition
func (p *ManifestParser) parseRule() error {
	name, err := p.scanner.ExpectIdent()
	if err != nil {
		return err
	}

	if err := p.expectNewline(); err != nil {
		return err
	}

	rule := graph.NewRule(name)

	// Parse rule bindings - these should NOT be evaluated yet
	if err := p.parseRuleBindings(rule); err != nil {
		return err
	}
	
	// Validate rspfile and rspfile_content are both specified or neither
	_, hasRspfile := rule.GetBinding("rspfile")
	_, hasRspfileContent := rule.GetBinding("rspfile_content")
	if hasRspfile != hasRspfileContent {
		return p.scanner.Error("rspfile and rspfile_content need to be both specified or neither")
	}

	if err := p.state.AddRule(rule); err != nil {
		return err
	}

	return nil
}

// parsePool parses a pool definition
func (p *ManifestParser) parsePool() error {
	name, err := p.scanner.ExpectIdent()
	if err != nil {
		return err
	}

	if err := p.expectNewline(); err != nil {
		return err
	}

	poolEnv := graph.NewBindingEnv(p.env)
	if err := p.parseBindings(poolEnv); err != nil {
		return err
	}

	depthStr := poolEnv.LookupVariable("depth")
	if depthStr == "" {
		return p.scanner.Error("pool is missing 'depth' variable")
	}

	var depth int
	if _, err := fmt.Sscanf(depthStr, "%d", &depth); err != nil {
		return p.scanner.Error(fmt.Sprintf("invalid pool depth '%s'", depthStr))
	}

	pool := graph.NewPool(name, depth)
	if err := p.state.AddPool(pool); err != nil {
		return err
	}

	return nil
}

// parseDefault parses default targets
func (p *ManifestParser) parseDefault() error {
	for {
		path, err := p.scanner.ReadPath()
		if err != nil {
			return err
		}

		evaluated := path.Evaluate(p.env)
		if evaluated == "" {
			break
		}

		node := p.state.GetNode(evaluated)
		p.state.AddDefault(node)

		if p.scanner.PeekToken(lexer.NEWLINE) || p.scanner.IsEOF() {
			break
		}
	}

	return p.expectNewline()
}

// parseVariable parses a variable assignment
func (p *ManifestParser) parseVariable() error {
	name, err := p.scanner.ExpectIdent()
	if err != nil {
		return err
	}

	if err := p.scanner.ExpectToken(lexer.EQUALS); err != nil {
		return err
	}

	value, err := p.scanner.ReadVarValue()
	if err != nil {
		return err
	}

	evaluated := value.Evaluate(p.env)
	p.env.AddBinding(name, evaluated)

	return p.expectNewline()
}

// parseRuleBindings parses indented variable bindings for a rule
func (p *ManifestParser) parseRuleBindings(rule *graph.Rule) error {
	for p.scanner.PeekToken(lexer.INDENT) {
		p.scanner.NextToken() // consume indent

		name, err := p.scanner.ExpectIdent()
		if err != nil {
			return err
		}

		if err := p.scanner.ExpectToken(lexer.EQUALS); err != nil {
			return err
		}

		value, err := p.scanner.ReadVarValue()
		if err != nil {
			return err
		}

		// Store the raw EvalString without evaluating it
		rule.AddBinding(name, *value)

		if err := p.expectNewline(); err != nil {
			return err
		}
	}

	return nil
}

// parseBindings parses indented variable bindings
func (p *ManifestParser) parseBindings(env *graph.BindingEnv) error {
	for p.scanner.PeekToken(lexer.INDENT) {
		p.scanner.NextToken() // consume indent

		name, err := p.scanner.ExpectIdent()
		if err != nil {
			return err
		}

		if err := p.scanner.ExpectToken(lexer.EQUALS); err != nil {
			return err
		}

		value, err := p.scanner.ReadVarValue()
		if err != nil {
			return err
		}

		evaluated := value.Evaluate(p.env)
		env.AddBinding(name, evaluated)

		if err := p.expectNewline(); err != nil {
			return err
		}
	}

	return nil
}

// parseInclude parses an include directive
func (p *ManifestParser) parseInclude() error {
	path, err := p.scanner.ReadPath()
	if err != nil {
		return err
	}

	evaluated := path.Evaluate(p.env)
	if evaluated == "" {
		return p.scanner.Error("empty include path")
	}

	if err := p.expectNewline(); err != nil {
		return err
	}

	// Make path relative to current file's directory
	includeFile := evaluated
	if !filepath.IsAbs(includeFile) {
		dir := filepath.Dir(p.filename)
		includeFile = filepath.Join(dir, includeFile)
	}

	// Parse included file with same environment
	subParser := &ManifestParser{
		state: p.state,
		scanner: lexer.NewScanner(),
		env: p.env, // Share environment
		quiet: p.quiet,
	}

	return subParser.ParseFile(includeFile)
}

// parseSubninja parses a subninja directive
func (p *ManifestParser) parseSubninja() error {
	path, err := p.scanner.ReadPath()
	if err != nil {
		return err
	}

	evaluated := path.Evaluate(p.env)
	if evaluated == "" {
		return p.scanner.Error("empty subninja path")
	}

	if err := p.expectNewline(); err != nil {
		return err
	}

	// Make path relative to current file's directory
	subninjaFile := evaluated
	if !filepath.IsAbs(subninjaFile) {
		dir := filepath.Dir(p.filename)
		subninjaFile = filepath.Join(dir, subninjaFile)
	}

	// Parse subninja file with new environment scope
	subParser := &ManifestParser{
		state: p.state,
		scanner: lexer.NewScanner(),
		env: graph.NewBindingEnv(p.env), // New scope
		quiet: p.quiet,
	}

	return subParser.ParseFile(subninjaFile)
}

// SetQuiet sets quiet mode
func (p *ManifestParser) SetQuiet(quiet bool) {
	p.quiet = quiet
}

// expectNewline expects a newline or EOF
func (p *ManifestParser) expectNewline() error {
	if !p.scanner.PeekToken(lexer.NEWLINE) && !p.scanner.IsEOF() {
		return p.scanner.Error("expected newline")
	}
	if p.scanner.PeekToken(lexer.NEWLINE) {
		p.scanner.NextToken() // consume it
	}
	return nil
}