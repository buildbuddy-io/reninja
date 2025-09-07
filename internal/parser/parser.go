package parser

import (
	"fmt"
	"log"
	"slices"
	"strconv"

	"github.com/buildbuddy-io/gin/internal/disk"
	"github.com/buildbuddy-io/gin/internal/eval_env"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/lexer"
	"github.com/buildbuddy-io/gin/internal/state"
	"github.com/buildbuddy-io/gin/internal/version"
)

const (
	PhonyCycleActionWarn = iota
	PhonyCycleActionError
)

type ManifestParserOptions struct {
	PhonyCycleAction int
}

func DefaultOptions() ManifestParserOptions {
	return ManifestParserOptions{
		PhonyCycleAction: PhonyCycleActionWarn,
	}
}

// ManifestParser parses ninja build files
type ManifestParser struct {
	state      *state.State
	fileReader disk.FileReader
	lexer      *lexer.Lexer

	env     *eval_env.BindingEnv
	options ManifestParserOptions

	subparser   *ManifestParser
	ins         []*eval_env.EvalString
	outs        []*eval_env.EvalString
	validations []*eval_env.EvalString

	quiet       bool
}

// New creates a new ManifestParser
func New(s *state.State, fileReader disk.FileReader, options ManifestParserOptions) *ManifestParser {
	return &ManifestParser{
		state:      s,
		fileReader: fileReader,
		lexer:      lexer.New(),

		env:     s.Bindings(),
		options: options,
		quiet:   true,
	}
}

// ParseFile parses a ninja build file
func (p *ManifestParser) ParseFile(filename string) error {
	content, err := p.fileReader.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("loading '%s': %w", filename, err)
	}
	return p.Parse(filename, string(content))
}

// Parse parses ninja build file content
func (p *ManifestParser) Parse(filename, input string) error {
	p.lexer.Start(filename, input)
	for {
		token := p.lexer.ReadToken()
		switch token {
		case lexer.POOL:
			if err := p.parsePool(); err != nil {
				return err
			}
		case lexer.BUILD:
			if err := p.parseEdge(); err != nil {
				return err
			}
		case lexer.RULE:
			if err := p.parseRule(); err != nil {
				return err
			}
		case lexer.DEFAULT:
			if err := p.parseDefault(); err != nil {
				return err
			}
		case lexer.IDENT:
			p.lexer.UnreadToken()
			name, let_value, err := p.parseLet()
			if err != nil {
				return err
			}
			value := let_value.Evaluate(p.env)
			if name == "ninja_required_version" {
				version.CheckNinjaVersion(value)
			}
			p.env.AddBinding(name, value)
		case lexer.INCLUDE:
			if err := p.parseFileInclude(false); err != nil {
				return err
			}
		case lexer.SUBNINJA:
			if err := p.parseFileInclude(true); err != nil {
				return err
			}
		case lexer.ERROR:
			return p.lexer.Error(p.lexer.DescribeLastError())
		case lexer.EOF:
			return nil
		case lexer.NEWLINE:
			break
		default:
			return p.lexer.Error(fmt.Sprintf("unexpected %s", lexer.TokenName(token)))
		}
	}

	return nil
}

func (p *ManifestParser) ExpectToken(expected lexer.Token) error {
	token := p.lexer.ReadToken()
	if token != expected {
		msg := fmt.Sprintf("expected %s, got %s%s", lexer.TokenName(expected), lexer.TokenName(token), lexer.TokenErrorHint(expected))
		return p.lexer.Error(msg)
	}
	return nil
}

func (p *ManifestParser) parsePool() error {
	name, ok := p.lexer.ReadIdent()
	if !ok {
		return p.lexer.Error("expected pool name")
	}
	if err := p.ExpectToken(lexer.NEWLINE); err != nil {
		return err
	}
	if p.state.LookupPool(name) != nil {
		return p.lexer.Error(fmt.Sprintf("duplicate pool '%s'", name))
	}

	depth := -1
	for p.lexer.PeekToken(lexer.INDENT) {
		key, value, err := p.parseLet()
		if err != nil {
			return err
		}
		if key == "depth" {
			depthString := value.Evaluate(p.env)
			depth, _ = strconv.Atoi(depthString)
			if depth < 0 {
				return p.lexer.Error("invalid pool depth")
			}
		} else {
			return p.lexer.Error(fmt.Sprintf("unexpected variable '%s'", key))
		}
	}

	if depth < 0 {
		return p.lexer.Error("expected 'depth =' line")
	}
	p.state.AddPool(graph.NewPool(name, depth))
	return nil
}

func (p *ManifestParser) parseEdge() error {
	p.ins = p.ins[:0]
	p.outs = p.outs[:0]
	p.validations = p.validations[:0]

	{
		// TODO(tylerw): should this be a do/while?
		out, err := p.lexer.ReadPath()
		if err != nil {
			return err
		}
		for !out.Empty() {
			// STOPSHIP(tylerw): check std::move semantics are correct in the go version.
			// If we're clearing objects that we've saved, we're going to have a bad time.
			p.outs = append(p.outs, out.Clone())
			out, err = p.lexer.ReadPath()
			if err != nil {
				return err
			}
		}
	}

	implicitOuts := 0
	if p.lexer.PeekToken(lexer.PIPE) {
		for {
			out, err := p.lexer.ReadPath()
			if err != nil {
				return err
			}
			if out.Empty() {
				break
			}
			p.outs = append(p.outs, out)
			implicitOuts++
		}
	}

	if len(p.outs) == 0 {
		return p.lexer.Error("OH SHIT    expected path")
	}

	if err := p.ExpectToken(lexer.COLON); err != nil {
		return err
	}

	ruleName, ok := p.lexer.ReadIdent()
	if !ok {
		return p.lexer.Error("expected build command name")
	}
	rule, ok := p.env.LookupRule(ruleName)
	if !ok {
		return p.lexer.Error(fmt.Sprintf("unknown build rule '%s'", ruleName))
	}

	for {
		in, err := p.lexer.ReadPath()
		if err != nil {
			return err
		}
		if in.Empty() {
			break
		}
		p.ins = append(p.ins, in)
	}

	// Add all implicit deps, counting how many as we go.
	implicit := 0
	if p.lexer.PeekToken(lexer.PIPE) {
		for {
			in, err := p.lexer.ReadPath()
			if err != nil {
				return err
			}
			if in.Empty() {
				break
			}
			p.ins = append(p.ins, in)
			implicit++
		}
	}

	// Add all order-only deps, counting how many as we go.
	orderOnly := 0
	if p.lexer.PeekToken(lexer.PIPE2) {
		for {
			in, err := p.lexer.ReadPath()
			if err != nil {
				return err
			}
			if in.Empty() {
				break
			}
			p.ins = append(p.ins, in)
			orderOnly++
		}
	}

	if p.lexer.PeekToken(lexer.PIPEAT) {
		for {
			validation, err := p.lexer.ReadPath()
			if err != nil {
				return err
			}
			if validation.Empty() {
				break
			}
			p.validations = append(p.validations, validation)
		}
	}

	if err := p.ExpectToken(lexer.NEWLINE); err != nil {
		return err
	}

	// Bindings on edges are rare, so allocate per-edge envs only when needed.
	hasIndentToken := p.lexer.PeekToken(lexer.INDENT)
	var env *eval_env.BindingEnv
	if hasIndentToken {
		env = eval_env.NewBindingEnv(p.env)
	} else {
		env = p.env
	}
	for hasIndentToken {
		key, val, err := p.parseLet()
		if err != nil {
			return err
		}
		env.AddBinding(key, val.Evaluate(p.env))
		hasIndentToken = p.lexer.PeekToken(lexer.INDENT)
	}

	edge := p.state.AddEdge(rule)
	edge.SetEnv(env)

	poolName := edge.GetBinding("pool")
	if poolName != "" {
		pool := p.state.LookupPool(poolName)
		if pool == nil {
			return p.lexer.Error(fmt.Sprintf("unknown pool name '%s'", poolName))
		}
		edge.SetPool(pool)
	}

	for i := range len(p.outs) {
		path := p.outs[i].Evaluate(env)
		if path == "" {
			return p.lexer.Error("empty path")
		}
		path, _ = graph.CanonicalizePath(path)
		if err := p.state.AddOut(path, edge); err != nil {
			return err
		}
	}

	// All outputs of the edge are already created by other edges. Don't add
	// this edge.  Do this check before input nodes are connected to the edge.
	if len(edge.Outputs()) == 0 {
		p.state.RemoveLastEdge()
		return nil
	}
	edge.SetImplicitOuts(implicitOuts)

	for i := range len(p.ins) {
		path := p.ins[i].Evaluate(env)
		if path == "" {
			return p.lexer.Error("empty path")
		}
		path, _ = graph.CanonicalizePath(path)
		p.state.AddIn(path, edge)
	}
	edge.SetImplicitDeps(implicit)
	edge.SetOrderOnlyDeps(orderOnly)

	for i := range len(p.validations) {
		path := p.validations[i].Evaluate(env)
		if path == "" {
			return p.lexer.Error("empty path")
		}
		path, _ = graph.CanonicalizePath(path)
		p.state.AddValidation(path, edge)
	}

	if p.options.PhonyCycleAction == PhonyCycleActionWarn && edge.MaybePhonycycleDiagnostic() {
		// CMake 2.8.12.x and 3.0.x incorrectly write phony build statements
		// that reference themselves.  Ninja used to tolerate these in the
		// build graph but that has since been fixed.  Filter them out to
		// support users of those old CMake versions.
		out := edge.Outputs()[0]
		edge.RemoveInput(out)
		edge.RemoveOutput(out)
		if !p.quiet {
			log.Printf("phony target '%s' names itself as an input; ignoring [-w phonycycle=warn]", out.Path())
		}
	}

	// Lookup, validate, and save any dyndep binding.  It will be used later
	// to load generated dependency information dynamically, but it must
	// be one of our manifest-specified inputs.
	dyndep := edge.GetUnescapedDyndep()
	if dyndep != "" {
		dyndep, _ = graph.CanonicalizePath(dyndep)
		node := p.state.GetNode(dyndep)
		node.SetDyndepPending(true)
		edge.SetDyndep(node)
		if !slices.Contains(edge.Inputs(), node) {
			return p.lexer.Error(fmt.Sprintf("dyndep '%s' is not an input", dyndep))
		}
		if node.GeneratedByDepLoader() {
			panic("dyndep should not have been generated by dep loaded")
		}
	}
	return nil
}

func (p *ManifestParser) parseRule() error {
	name, ok := p.lexer.ReadIdent()
	if !ok {
		return p.lexer.Error("expected rule name")
	}
	if err := p.ExpectToken(lexer.NEWLINE); err != nil {
		return err
	}
	if _, ok := p.env.LookupRuleCurrentScope(name); ok {
		return p.lexer.Error(fmt.Sprintf("duplicate rule '%s'", name))
	}
	rule := eval_env.NewRule(name)
	for p.lexer.PeekToken(lexer.INDENT) {
		key, value, err := p.parseLet()
		if err != nil {
			return err
		}
		if eval_env.IsReservedBinding(key) {
			rule.AddBinding(key, value)
		} else {
			return p.lexer.Error(fmt.Sprintf("unexpected variable '%s'", key))
		}
	}

	rspFile, rspFileOK := rule.GetBinding("rspfile")
	rspFileContent, rspFileContentOK := rule.GetBinding("rspfile_content")
	if rspFileOK != rspFileContentOK || rspFile.Empty() != rspFileContent.Empty() {
		return p.lexer.Error("rspfile and rspfile_content need to be both specified")
	}
	if cmd, ok := rule.GetBinding("command"); !ok || cmd.Empty() {
		return p.lexer.Error("expected 'command =' line")
	}
	p.env.AddRule(rule) // env takes ownership of rule
	return nil
}

// returns key, value, error
func (p *ManifestParser) parseLet() (string, *eval_env.EvalString, error) {
	var key string
	if k, ok := p.lexer.ReadIdent(); !ok {
		return "", nil, p.lexer.Error("expected variable name")
	} else {
		key = k
	}
	if err := p.ExpectToken(lexer.EQUALS); err != nil {
		return "", nil, err
	}
	value, err := p.lexer.ReadVarValue()
	return key, value, err
}

func (p *ManifestParser) parseDefault() error {
	eval, err := p.lexer.ReadPath()
	if err != nil {
		return err
	}
	if eval.Empty() {
		return p.lexer.Error("expected target name")
	}

	for ok := true; ok; ok = !eval.Empty() {
		path := eval.Evaluate(p.env)
		if path == "" {
			return p.lexer.Error("empty path")
		}
		path, _ = graph.CanonicalizePath(path)
		if err := p.state.AddDefault(path); err != nil {
			return p.lexer.Error(err.Error())
		}
		eval.Clear()
		if nextEval, err := p.lexer.ReadPath(); err != nil {
			return err
		} else {
			eval = nextEval
		}
	}
	return p.ExpectToken(lexer.NEWLINE)
}

func (p *ManifestParser) parseVariable() error {
	return nil
}
func (p *ManifestParser) parseFileInclude(newScope bool) error {
	eval, err := p.lexer.ReadPath()
	if err != nil {
		return err
	}
	path := eval.Evaluate(p.env)
	if p.subparser == nil {
		p.subparser = New(p.state, p.fileReader, p.options)
	}
	if newScope {
		p.subparser.env = eval_env.NewBindingEnv(p.env)
	} else {
		p.subparser.env = p.env
	}

	if err := p.subparser.ParseFile(path); err != nil {
		return err
	}
	if err := p.ExpectToken(lexer.NEWLINE); err != nil {
		return err
	}
	return nil
}
