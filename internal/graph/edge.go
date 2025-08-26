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

// VisitMark represents the visitation state during graph traversal
type VisitMark int

const (
	// VisitNone means the edge hasn't been visited
	VisitNone VisitMark = iota
	// VisitInStack means the edge is currently being processed
	VisitInStack
	// VisitDone means the edge has been fully processed
	VisitDone
)

// Edge represents a build rule connecting input and output nodes
type Edge struct {
	rule        *Rule
	pool        *Pool
	inputs      []*Node
	outputs     []*Node
	validations []*Node
	dyndep      *Node
	env         *BindingEnv
	mark        VisitMark
	id          int
	
	// Critical path weight for build scheduling priority
	criticalPathWeight int64
	
	// Build state flags
	outputsReady         bool
	depsLoaded          bool
	depsMissing         bool
	generatedByDepLoader bool
	commandStartTime    TimeStamp
	
	// Job server slot
	jobSlot interface{} // Will be properly typed when we implement jobserver
	
	// Historical timing info from ninja log
	prevElapsedTimeMillis int64
	
	// Input categorization
	implicitDeps  int // Number of implicit dependencies
	orderOnlyDeps int // Number of order-only dependencies
	
	// Output categorization
	implicitOuts int // Number of implicit outputs
}

// NewEdge creates a new Edge
func NewEdge() *Edge {
	return &Edge{
		mark:                  VisitNone,
		id:                    0,
		criticalPathWeight:    -1,
		prevElapsedTimeMillis: -1,
	}
}

// Rule returns the edge's rule
func (e *Edge) Rule() *Rule {
	return e.rule
}

// SetRule sets the edge's rule
func (e *Edge) SetRule(rule *Rule) {
	e.rule = rule
}

// Pool returns the edge's pool
func (e *Edge) Pool() *Pool {
	return e.pool
}

// SetPool sets the edge's pool
func (e *Edge) SetPool(pool *Pool) {
	e.pool = pool
}

// Inputs returns all input nodes
func (e *Edge) Inputs() []*Node {
	return e.inputs
}

// Outputs returns all output nodes
func (e *Edge) Outputs() []*Node {
	return e.outputs
}

// Validations returns all validation nodes
func (e *Edge) Validations() []*Node {
	return e.validations
}

// Dyndep returns the dynamic dependency node
func (e *Edge) Dyndep() *Node {
	return e.dyndep
}

// SetDyndep sets the dynamic dependency node
func (e *Edge) SetDyndep(node *Node) {
	e.dyndep = node
}

// Env returns the binding environment
func (e *Edge) Env() *BindingEnv {
	return e.env
}

// SetEnv sets the binding environment
func (e *Edge) SetEnv(env *BindingEnv) {
	e.env = env
}

// Mark returns the visitation mark
func (e *Edge) Mark() VisitMark {
	return e.mark
}

// SetMark sets the visitation mark
func (e *Edge) SetMark(mark VisitMark) {
	e.mark = mark
}

// ID returns the edge ID
func (e *Edge) ID() int {
	return e.id
}

// SetID sets the edge ID
func (e *Edge) SetID(id int) {
	e.id = id
}

// CriticalPathWeight returns the critical path weight
func (e *Edge) CriticalPathWeight() int64 {
	if e.criticalPathWeight == -1 {
		return 0
	}
	return e.criticalPathWeight
}

// SetCriticalPathWeight sets the critical path weight
func (e *Edge) SetCriticalPathWeight(weight int64) {
	e.criticalPathWeight = weight
}

// OutputsReady returns whether outputs are ready
func (e *Edge) OutputsReady() bool {
	return e.outputsReady
}

// SetOutputsReady sets whether outputs are ready
func (e *Edge) SetOutputsReady(ready bool) {
	e.outputsReady = ready
}

// DepsLoaded returns whether dependencies have been loaded
func (e *Edge) DepsLoaded() bool {
	return e.depsLoaded
}

// SetDepsLoaded sets whether dependencies have been loaded
func (e *Edge) SetDepsLoaded(loaded bool) {
	e.depsLoaded = loaded
}

// DepsMissing returns whether dependencies are missing
func (e *Edge) DepsMissing() bool {
	return e.depsMissing
}

// SetDepsMissing sets whether dependencies are missing
func (e *Edge) SetDepsMissing(missing bool) {
	e.depsMissing = missing
}

// Weight returns the edge weight (always 1 for now)
func (e *Edge) Weight() int {
	return 1
}

// IsImplicit checks if an input at the given index is implicit
func (e *Edge) IsImplicit(index int) bool {
	return index >= len(e.inputs)-e.orderOnlyDeps-e.implicitDeps &&
		!e.IsOrderOnly(index)
}

// IsOrderOnly checks if an input at the given index is order-only
func (e *Edge) IsOrderOnly(index int) bool {
	return index >= len(e.inputs)-e.orderOnlyDeps
}

// IsImplicitOut checks if an output at the given index is implicit
func (e *Edge) IsImplicitOut(index int) bool {
	return index >= len(e.outputs)-e.implicitOuts
}

// ExplicitInputs returns only the explicit input nodes
func (e *Edge) ExplicitInputs() []*Node {
	explicitCount := len(e.inputs) - e.implicitDeps - e.orderOnlyDeps
	if explicitCount <= 0 {
		return nil
	}
	return e.inputs[:explicitCount]
}

// ImplicitInputs returns only the implicit input nodes
func (e *Edge) ImplicitInputs() []*Node {
	if e.implicitDeps == 0 {
		return nil
	}
	start := len(e.inputs) - e.orderOnlyDeps - e.implicitDeps
	end := len(e.inputs) - e.orderOnlyDeps
	return e.inputs[start:end]
}

// OrderOnlyInputs returns only the order-only input nodes
func (e *Edge) OrderOnlyInputs() []*Node {
	if e.orderOnlyDeps == 0 {
		return nil
	}
	return e.inputs[len(e.inputs)-e.orderOnlyDeps:]
}

// ExplicitOutputs returns only the explicit output nodes
func (e *Edge) ExplicitOutputs() []*Node {
	explicitCount := len(e.outputs) - e.implicitOuts
	if explicitCount <= 0 {
		return nil
	}
	return e.outputs[:explicitCount]
}

// ImplicitOutputs returns only the implicit output nodes
func (e *Edge) ImplicitOutputs() []*Node {
	if e.implicitOuts == 0 {
		return nil
	}
	return e.outputs[len(e.outputs)-e.implicitOuts:]
}

// AddInput adds an input node
func (e *Edge) AddInput(node *Node) {
	e.inputs = append(e.inputs, node)
}

// AddOutput adds an output node
func (e *Edge) AddOutput(node *Node) {
	e.outputs = append(e.outputs, node)
}

// AddValidation adds a validation node
func (e *Edge) AddValidation(node *Node) {
	e.validations = append(e.validations, node)
}

// SetImplicitDeps sets the number of implicit dependencies
func (e *Edge) SetImplicitDeps(count int) {
	e.implicitDeps = count
}

// SetOrderOnlyDeps sets the number of order-only dependencies
func (e *Edge) SetOrderOnlyDeps(count int) {
	e.orderOnlyDeps = count
}

// SetImplicitOuts sets the number of implicit outputs
func (e *Edge) SetImplicitOuts(count int) {
	e.implicitOuts = count
}

// AllInputsReady returns true if all inputs' in-edges are ready
func (e *Edge) AllInputsReady() bool {
	for _, input := range e.inputs {
		if input.InEdge() != nil && !input.InEdge().OutputsReady() {
			return false
		}
	}
	return true
}

// IsPhony returns true if this is a phony edge
func (e *Edge) IsPhony() bool {
	return e.rule != nil && e.rule.IsPhony()
}

// UseConsole returns true if this edge should use the console pool
func (e *Edge) UseConsole() bool {
	return e.pool != nil && e.pool.Name() == "console"
}

// GetBinding returns the shell-escaped value of a binding
func (e *Edge) GetBinding(key string) string {
	// First check edge-specific bindings
	if e.env != nil {
		if val := e.env.LookupVariable(key); val != "" {
			return val
		}
	}
	
	// Then check rule bindings
	if e.rule != nil {
		if evalStr, ok := e.rule.GetBinding(key); ok {
			// Create evaluation environment with edge context for $in, $out variables
			evalEnv := EdgeEnv(e.env, e)
			return evalStr.Evaluate(evalEnv)
		}
	}
	
	return ""
}

// GetBindingBool returns a binding value as a boolean
func (e *Edge) GetBindingBool(key string) bool {
	value := e.GetBinding(key)
	return value != "" && value != "0" && strings.ToLower(value) != "false"
}

// GetUnescapedDepfile returns the depfile binding without shell escaping
func (e *Edge) GetUnescapedDepfile() string {
	return e.getUnescapedBinding("depfile")
}

// GetUnescapedDyndep returns the dyndep binding without shell escaping
func (e *Edge) GetUnescapedDyndep() string {
	return e.getUnescapedBinding("dyndep")
}

// GetUnescapedRspfile returns the rspfile binding without shell escaping
func (e *Edge) GetUnescapedRspfile() string {
	return e.getUnescapedBinding("rspfile")
}

func (e *Edge) getUnescapedBinding(key string) string {
	// First check edge bindings
	if e.env != nil {
		if val := e.env.LookupVariable(key); val != "" {
			// TODO: Implement proper unescaping if needed
			return val
		}
	}
	
	// Then check rule bindings and evaluate them
	if e.rule != nil {
		if evalStr, ok := e.rule.GetBinding(key); ok {
			// Create evaluation environment with edge context
			evalEnv := EdgeEnv(e.env, e)
			return evalStr.Evaluate(evalEnv)
		}
	}
	
	return ""
}

// EvaluateCommand expands all variables in a command and returns it as a string
func (e *Edge) EvaluateCommand(inclRspFile bool) string {
	if e.rule == nil {
		return ""
	}
	
	// Get the command binding from the rule
	commandEval, ok := e.rule.GetBinding("command")
	if !ok {
		return ""
	}
	
	// Create evaluation environment with built-in variables
	evalEnv := EdgeEnv(e.env, e)
	
	// Evaluate the command with the proper environment
	expanded := commandEval.Evaluate(evalEnv)
	
	if inclRspFile {
		rspfile := e.GetUnescapedRspfile()
		if rspfile != "" {
			rspfileContent := e.GetBinding("rspfile_content")
			if rspfileContent != "" {
				expanded = fmt.Sprintf("%s\n[%s]:\n%s", expanded, rspfile, rspfileContent)
			}
		}
	}
	
	return expanded
}

// MaybePhonycycleDiagnostic returns true if phony cycle diagnostics should be shown
func (e *Edge) MaybePhonycycleDiagnostic() bool {
	// This would be implemented based on the actual rule's settings
	return e.IsPhony()
}