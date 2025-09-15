package dyndep_parser

import (
	"fmt"

	"github.com/buildbuddy-io/gin/internal/disk"
	"github.com/buildbuddy-io/gin/internal/eval_env"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/lexer"
	"github.com/buildbuddy-io/gin/internal/state"
	"github.com/buildbuddy-io/gin/internal/util"
	"github.com/buildbuddy-io/gin/internal/version"
)

// / Store dynamically-discovered dependency information for one edge.
type Dyndeps struct {
	Used            bool
	Restat          bool
	ImplicitInputs  []*graph.Node
	ImplicitOutputs []*graph.Node
}

// / Store data loaded from one dyndep file.  Map from an edge
// / to its dynamically-discovered dependency information.
// / This is a struct rather than a typedef so that we can
// / forward-declare it in other headers.
type DyndepFile = map[*graph.Edge]*Dyndeps

func NewDyndepFile() DyndepFile {
	return make(map[*graph.Edge]*Dyndeps, 0)
}

// DyndepParser parses ninja build files
type DyndepParser struct {
	state      *state.State
	fileReader disk.FileReader
	lexer      *lexer.Lexer
	dyndepFile DyndepFile
	env        *eval_env.BindingEnv
}

func New(state *state.State, fileReader disk.FileReader, dyndepFile DyndepFile) *DyndepParser {
	return &DyndepParser{
		state:      state,
		fileReader: fileReader,
		lexer:      lexer.New(),
		dyndepFile: dyndepFile,
		env:        eval_env.NewBindingEnv(nil),
	}
}

func (d *DyndepParser) ExpectToken(expected lexer.Token) error {
	token := d.lexer.ReadToken()
	if token != expected {
		msg := fmt.Sprintf("expected %s, got %s%s", lexer.TokenName(expected), lexer.TokenName(token), lexer.TokenErrorHint(expected))
		return d.lexer.Error(msg)
	}
	return nil
}

func (d *DyndepParser) Load(filename string) error {
	contents, err := d.fileReader.ReadFile(filename)
	if err != nil {
		return err
	}
	return d.Parse(filename, string(contents))
}

func (d *DyndepParser) ParseTest(input string) error {
	return d.Parse("input", input)
}

func (d *DyndepParser) Parse(filename, input string) error {
	d.lexer.Start(filename, input)
	haveDyndepVersion := false

	for {
		token := d.lexer.ReadToken()
		switch token {
		case lexer.BUILD:
			if !haveDyndepVersion {
				return d.lexer.Error("expected 'ninja_dyndep_version = ...'")
			}
			if err := d.parseEdge(); err != nil {
				return err
			}
		case lexer.IDENT:
			d.lexer.UnreadToken()
			if haveDyndepVersion {
				return d.lexer.Error(fmt.Sprintf("unexpected %s", lexer.TokenName(token)))
			}
			if err := d.parseDyndepVersion(); err != nil {
				return err
			}
			haveDyndepVersion = true
		case lexer.ERROR:
			return d.lexer.Error(d.lexer.DescribeLastError())
		case lexer.EOF:
			if !haveDyndepVersion {
				return d.lexer.Error("expected 'ninja_dyndep_version = ...'")
			}
			return nil
		case lexer.NEWLINE:
			break
		default:
			return d.lexer.Error(fmt.Sprintf("unexpected %s", lexer.TokenName(token)))
		}
	}
}

func (d *DyndepParser) parseDyndepVersion() error {
	name, letValue, err := d.parseLet()
	if err != nil {
		return d.lexer.Error(err.Error())
	}
	if name != "ninja_dyndep_version" {
		return d.lexer.Error("expected 'ninja_dyndep_version = ...'")
	}
	versionStr := letValue.Evaluate(d.env)
	major, minor := version.ParseVersion(versionStr)
	if major != 1 || minor != 0 {
		return d.lexer.Error(fmt.Sprintf("unsupported 'ninja_dyndep_version = %s'", versionStr))
	}
	return nil
}

// returns key, value, error
func (d *DyndepParser) parseLet() (string, *eval_env.EvalString, error) {
	var key string
	if k, ok := d.lexer.ReadIdent(); !ok {
		return "", nil, d.lexer.Error("expected variable name")
	} else {
		key = k
	}
	if err := d.ExpectToken(lexer.EQUALS); err != nil {
		return "", nil, err
	}
	value, err := d.lexer.ReadVarValue()
	return key, value, err
}

func (d *DyndepParser) parseEdge() error {
	// Parse one explicit output.  We expect it to already have an edge.
	// We will record its dynamically-discovered dependency information.
	dyndeps := &Dyndeps{}
	{
		out0, err := d.lexer.ReadPath()
		if err != nil {
			return err
		}
		if out0.Empty() {
			return d.lexer.Error("expected path")
		}

		path := out0.Evaluate(d.env)
		if path == "" {
			return d.lexer.Error("empty path")
		}
		path, _ = util.CanonicalizePath(path)
		node := d.state.LookupNode(path)
		if node == nil || node.InEdge() == nil {
			return d.lexer.Error(fmt.Sprintf("no build statement exists for '%s'", path))
		}
		edge := node.InEdge()
		if _, ok := d.dyndepFile[edge]; ok {
			return d.lexer.Error(fmt.Sprintf("multiple statements for '%s'", path))
		}
		d.dyndepFile[edge] = dyndeps
	}

	// Disallow explicit outputs.
	{
		out, err := d.lexer.ReadPath()
		if err != nil {
			return err
		}
		if !out.Empty() {
			return d.lexer.Error("explicit outputs not supported")
		}
	}

	// Parse implicit outputs, if any.
	outs := make([]*eval_env.EvalString, 0)
	if d.lexer.PeekToken(lexer.PIPE) {
		for {
			out, err := d.lexer.ReadPath()
			if err != nil {
				return err
			}
			if out.Empty() {
				break
			}
			outs = append(outs, out)
		}
	}

	if err := d.ExpectToken(lexer.COLON); err != nil {
		return err
	}

	ruleName, ok := d.lexer.ReadIdent()
	if !ok || ruleName != "dyndep" {
		return d.lexer.Error("expected build command name 'dyndep'")
	}

	// Disallow explicit inputs.
	{
		in, err := d.lexer.ReadPath()
		if err != nil {
			return err
		}
		if !in.Empty() {
			return d.lexer.Error("explicit inputs not supported")
		}
	}

	// Parse implicit inputs, if any.
	ins := make([]*eval_env.EvalString, 0)
	if d.lexer.PeekToken(lexer.PIPE) {
		for {
			in, err := d.lexer.ReadPath()
			if err != nil {
				return err
			}
			if in.Empty() {
				break
			}
			ins = append(ins, in)
		}
	}

	// Disallow order-only inputs.
	if d.lexer.PeekToken(lexer.PIPE2) {
		return d.lexer.Error("order-only inputs not supported")
	}

	if err := d.ExpectToken(lexer.NEWLINE); err != nil {
		return err
	}

	if d.lexer.PeekToken(lexer.INDENT) {
		key, val, err := d.parseLet()
		if err != nil {
			return err
		}
		if key != "restat" {
			return d.lexer.Error("binding is not 'restat'")
		}
		value := val.Evaluate(d.env)
		dyndeps.Restat = value != ""
	}

	dyndeps.ImplicitInputs = make([]*graph.Node, 0, len(ins))
	for _, in := range ins {
		path := in.Evaluate(d.env)
		if path == "" {
			return d.lexer.Error("empty path")
		}
		path, _ = util.CanonicalizePath(path)
		n := d.state.GetNode(path)
		dyndeps.ImplicitInputs = append(dyndeps.ImplicitInputs, n)
	}

	dyndeps.ImplicitOutputs = make([]*graph.Node, 0, len(ins))
	for _, out := range outs {
		path := out.Evaluate(d.env)
		if path == "" {
			return d.lexer.Error("empty path")
		}
		path, _ = util.CanonicalizePath(path)
		n := d.state.GetNode(path)
		dyndeps.ImplicitOutputs = append(dyndeps.ImplicitOutputs, n)
	}
	return nil
}
