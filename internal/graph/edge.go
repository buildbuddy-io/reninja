package graph

import (
	"fmt"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/buildbuddy-io/reninja/internal/eval_env"
	"github.com/buildbuddy-io/reninja/internal/jobserver"
	"github.com/buildbuddy-io/reninja/internal/timestamp"
	"github.com/buildbuddy-io/reninja/internal/util"
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

func EdgePriorityLess(e1, e2 *Edge) bool {
	cw1 := e1.CriticalPathWeight()
	cw2 := e2.CriticalPathWeight()

	if cw1 != cw2 {
		return cw1 < cw2
	}
	return e1.ID() > e2.ID()
}

func EdgePriorityGreater(e1, e2 *Edge) bool {
	return EdgePriorityLess(e2, e1)
}

// Edge represents a build rule connecting input and output nodes
type Edge struct {
	rule *eval_env.Rule
	pool *Pool

	// Input slices separated by type
	explicitInputs  []*Node
	implicitInputs  []*Node
	orderOnlyInputs []*Node

	outputs       []*Node
	validations   []*Node
	dyndep        *Node
	dyndepPending bool
	env           *eval_env.BindingEnv
	mark          VisitMark
	id            int

	// Critical path weight for build scheduling priority
	criticalPathWeight int64

	// Build state flags
	outputsReady         bool
	depsLoaded           bool
	depsMissing          bool
	generatedByDepLoader bool
	commandStartTime     timestamp.TimeStamp

	// Job server slot
	jobSlot jobserver.Slot

	// Historical timing info from ninja log
	prevElapsedTimeMillis int64

	// Output categorization
	implicitOuts int // Number of implicit outputs
}

// NewEdge creates a new Edge
func NewEdge() *Edge {
	e := &Edge{
		mark:                  VisitNone,
		id:                    0,
		criticalPathWeight:    -1,
		prevElapsedTimeMillis: -1,
	}
	return e
}

// Rule returns the edge's rule
func (e *Edge) Rule() *eval_env.Rule {
	return e.rule
}

// SetRule sets the edge's rule
func (e *Edge) SetRule(rule *eval_env.Rule) {
	e.rule = rule
}

func (e *Edge) JobSlot() jobserver.Slot {
	return e.jobSlot
}

func (e *Edge) SetJobSlot(slot jobserver.Slot) {
	e.jobSlot = slot
}

func (e *Edge) PrevElapsedTimeMillis() int64 {
	return e.prevElapsedTimeMillis
}

func (e *Edge) SetPrevElapsedTimeMillis(i int64) {
	e.prevElapsedTimeMillis = i
}

func (e *Edge) CommandStartTime() timestamp.TimeStamp {
	return e.commandStartTime
}

func (e *Edge) SetCommandStartTime(t timestamp.TimeStamp) {
	e.commandStartTime = t
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
	return slices.Concat(e.explicitInputs, e.implicitInputs, e.orderOnlyInputs)
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

// SetDyndep sets the dynamic dependency node
func (e *Edge) SetDyndepPending(t bool) {
	e.dyndepPending = t
}

// Env returns the binding environment
func (e *Edge) Env() *eval_env.BindingEnv {
	return e.env
}

// SetEnv sets the binding environment
func (e *Edge) SetEnv(env *eval_env.BindingEnv) {
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

// IsImplicitOut checks if an output at the given index is implicit
func (e *Edge) IsImplicitOut(index int) bool {
	return index >= len(e.outputs)-e.implicitOuts
}

// ExplicitInputs returns only the explicit input nodes
func (e *Edge) ExplicitInputs() []*Node {
	return e.explicitInputs
}

// ImplicitInputs returns only the implicit input nodes
func (e *Edge) ImplicitInputs() []*Node {
	return e.implicitInputs
}

// OrderOnlyInputs returns only the order-only input nodes
func (e *Edge) OrderOnlyInputs() []*Node {
	return e.orderOnlyInputs
}

func (e *Edge) NonOrderOnlyInputs() []*Node {
	return slices.Concat(e.explicitInputs, e.implicitInputs)
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

// AddExplicitInput adds an explicit input node
func (e *Edge) AddExplicitInput(node *Node) {
	e.explicitInputs = append(e.explicitInputs, node)
}

// AddImplicitInput adds an implicit input node
func (e *Edge) AddImplicitInput(node *Node) {
	e.implicitInputs = append(e.implicitInputs, node)
}

// AddOrderOnlyInput adds an order-only input node
func (e *Edge) AddOrderOnlyInput(node *Node) {
	e.orderOnlyInputs = append(e.orderOnlyInputs, node)
}

// AddImplicitInputs adds multiple implicit input nodes
func (e *Edge) AddImplicitInputs(nodes []*Node) {
	e.implicitInputs = append(e.implicitInputs, nodes...)
}

func (e *Edge) RemoveInput(node *Node) bool {
	deletedSomething := false
	deleteFunc := func(n *Node) bool {
		match := n == node
		if match {
			deletedSomething = true
		}
		return match
	}
	e.explicitInputs = slices.DeleteFunc(e.explicitInputs, deleteFunc)
	e.implicitInputs = slices.DeleteFunc(e.implicitInputs, deleteFunc)
	e.orderOnlyInputs = slices.DeleteFunc(e.orderOnlyInputs, deleteFunc)
	return deletedSomething
}

// StaticInputs returns inputs that are known from the manifest only, not depfiles.
// This includes explicit and implicit inputs not generated by the dep loader.
func (e *Edge) StaticInputs() []*Node {
	static := make([]*Node, 0, len(e.ExplicitInputs()))
	for _, n := range e.ExplicitInputs() {
		static = append(static, n)
	}
	for _, n := range e.ImplicitInputs() {
		if !n.GeneratedByDepLoader() {
			static = append(static, n)
		}
	}
	// order-only inputs don't affect content, so exclude them?
	return static
}

// DynamicInputs returns inputs that were discovered from depfiles.
func (e *Edge) DynamicInputs() []*Node {
	dynamic := make([]*Node, 0)
	for _, n := range e.ImplicitInputs() {
		if n.GeneratedByDepLoader() {
			dynamic = append(dynamic, n)
		}
	}
	return dynamic
}

// AddOutput adds an output node
func (e *Edge) AddOutput(node *Node) {
	e.outputs = append(e.outputs, node)
}

func (e *Edge) RemoveOutput(node *Node) {
	e.outputs = slices.DeleteFunc(e.outputs, func(n *Node) bool {
		return n == node
	})
}

// AddValidation adds a validation node
func (e *Edge) AddValidation(node *Node) {
	e.validations = append(e.validations, node)
}

// SetImplicitOuts sets the number of implicit outputs
func (e *Edge) SetImplicitOuts(count int) {
	e.implicitOuts = count
}

// AllInputsReady returns true if all inputs' in-edges are ready
func (e *Edge) AllInputsReady() bool {
	checkReady := func(inputs []*Node) bool {
		for _, input := range inputs {
			if input.InEdge() != nil && !input.InEdge().OutputsReady() {
				return false
			}
		}
		return true
	}
	return checkReady(e.explicitInputs) && checkReady(e.implicitInputs) && checkReady(e.orderOnlyInputs)
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
	edgeEnv := NewEdgeEnv(e, shellEscape)
	return edgeEnv.LookupVariable(key)
}

// GetBindingBool returns a binding value as a boolean
func (e *Edge) GetBindingBool(key string) bool {
	value := e.GetBinding(key)
	return value != "" && value != "0" && strings.ToLower(value) != "false"
}

// GetUnescapedDepfile returns the depfile binding without shell escaping
func (e *Edge) GetUnescapedDepfile() string {
	edgeEnv := NewEdgeEnv(e, doNotEscape)
	return edgeEnv.LookupVariable("depfile")
}

// GetUnescapedDyndep returns the dyndep binding without shell escaping
func (e *Edge) GetUnescapedDyndep() string {
	edgeEnv := NewEdgeEnv(e, doNotEscape)
	return edgeEnv.LookupVariable("dyndep")
}

// GetUnescapedRspfile returns the rspfile binding without shell escaping
func (e *Edge) GetUnescapedRspfile() string {
	edgeEnv := NewEdgeEnv(e, doNotEscape)
	return edgeEnv.LookupVariable("rspfile")
}

// EvaluateCommand expands all variables in a command and returns it as a string
func (e *Edge) EvaluateCommand(inclRspFile bool) string {
	command := e.GetBinding("command")
	if inclRspFile {
		rspfileContent := e.GetBinding("rspfile_content")
		if rspfileContent != "" {
			command += ";rspfile=" + rspfileContent
		}
	}
	return command
}

// MaybePhonycycleDiagnostic returns true if phony cycle diagnostics should be shown
func (e *Edge) MaybePhonycycleDiagnostic() bool {
	// This would be implemented based on the actual rule's settings
	return e.IsPhony()
}

func (e *Edge) Dump(prefix string) {
	fmt.Printf("%s[ ", prefix)
	for _, node := range e.Inputs() {
		fmt.Printf("%s ", node.Path())
	}
	fmt.Printf("--%s-> ", e.rule.Name())
	for _, node := range e.outputs {
		fmt.Printf("%s ", node.Path())
	}
	if len(e.validations) > 0 {
		fmt.Printf(" validations")
		for _, node := range e.validations {
			fmt.Printf("%s ", node.Path())
		}
	}
	if e.pool != nil {
		if e.pool.Name() != "" {
			fmt.Printf("(in pool '%s')", e.pool.Name())
		}
	} else {
		fmt.Printf("(null pool?)")
	}
	fmt.Printf("] %p\n", e)
}

func (e *Edge) ActionID() string {
	// TODO(tylerw): make a digest?
	return fmt.Sprintf("edge-%d", e.ID())
}

func (e *Edge) ActionMnemonic() string {
	if betterMnemonic, _, ok := strings.Cut(e.Rule().Name(), "__"); ok {
		return betterMnemonic
	} else {
		mnemonic, _, _ := strings.Cut(e.EvaluateCommand(false), " ")
		return mnemonic
	}
}

func (e *Edge) TargetLabel() string {
	for _, output := range e.Outputs() {
		return output.Path()
	}
	return ""
}

type escapeKind int

const (
	shellEscape escapeKind = iota
	doNotEscape
)

type EdgeEnv struct {
	lookups     []string
	edge        *Edge
	escapeInOut escapeKind
	recursive   bool
}

func NewEdgeEnv(edge *Edge, escape escapeKind) *EdgeEnv {
	return &EdgeEnv{
		edge:        edge,
		escapeInOut: escape,
		recursive:   false,
	}
}

func (e *EdgeEnv) MakePathList(span []*Node, sep rune) string {
	var result strings.Builder

	for i, node := range span {
		if i > 0 {
			result.WriteRune(sep)
		}
		path := node.PathDecanonicalized()

		if e.escapeInOut == shellEscape {
			if runtime.GOOS == "windows" {
				result.WriteString(util.GetWin32EscapedString(path))
			} else {
				result.WriteString(util.GetShellEscapedString(path))
			}
		} else {
			result.WriteString(path)
		}
	}
	return result.String()
}

func (e *EdgeEnv) LookupVariable(v string) string {
	if v == "in" || v == "in_newline" {
		sep := '\n'
		if v == "in" {
			sep = ' '
		}
		return e.MakePathList(e.edge.ExplicitInputs(), sep)
	} else if v == "out" {
		explicitOutsCount := len(e.edge.outputs) - e.edge.implicitOuts
		return e.MakePathList(e.edge.Outputs()[:explicitOutsCount], ' ')
	}

	// Technical note about the lookups_ vector.
	//
	// This is used to detect cycles during recursive variable expansion
	// which can be seen as a graph traversal problem. Consider the following
	// example:
	//
	//    rule something
	//      command = $foo $foo $var1
	//      var1 = $var2
	//      var2 = $var3
	//      var3 = $var1
	//      foo = FOO
	//
	// Each variable definition can be seen as a node in a graph that looks
	// like the following:
	//
	//   command --> foo
	//      |
	//      v
	//    var1 <-----.
	//      |        |
	//      v        |
	//    var2 ---> var3
	//
	// The lookups_ vector is used as a stack of visited nodes/variables
	// during recursive expansion. Entering a node adds an item to the
	// stack, leaving the node removes it.
	//
	// The recursive_ flag is used as a small performance optimization
	// to never record the starting node in the stack when beginning a new
	// expansion, since in most cases, expansions are not recursive
	// at all.
	//
	if e.recursive {
		i := sort.SearchStrings(e.lookups, v)
		if i != len(e.lookups) {
			cycle := ""
			for _, s := range e.lookups[i:] {
				cycle += s + " -> "
			}
			cycle += v
			util.Fatal("cycle in rule variables: " + cycle)
		}
	}

	// See notes on BindingEnv::LookupWithFallback.
	eval, ok := e.edge.rule.GetBinding(v)
	recordVarname := e.recursive && ok && eval != nil
	if recordVarname {
		e.lookups = append(e.lookups, v)
	}

	// In practice, variables defined on rules never use another rule variable.
	// For performance, only start checking for cycles after the first lookup.
	e.recursive = true
	result := e.edge.env.LookupWithFallback(v, eval, e)
	if recordVarname {
		e.lookups = e.lookups[:len(e.lookups)-1]
	}
	return result
}
