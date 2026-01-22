package manifest_parser_test

import (
	"runtime"
	"testing"

	"github.com/buildbuddy-io/reninja/internal/disk"
	"github.com/buildbuddy-io/reninja/internal/manifest_parser"
	"github.com/buildbuddy-io/reninja/internal/state"
	"github.com/buildbuddy-io/reninja/internal/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmpty(t *testing.T) {
	s := state.New()
	test.AssertParse(t, "", s)
}

func TestRuleAttributes(t *testing.T) {
	// Check that all of the allowed rule attributes are parsed ok.
	s := state.New()
	test.AssertParse(t, `rule cat
  command = a
  depfile = a
  deps = a
  description = a
  generator = a
  restat = a
  rspfile = a
  rspfile_content = a
`, s)
}

func TestIgnoreIndentedComments(t *testing.T) {
	s := state.New()
	test.AssertParse(t, `  #indented comment
rule cat
  command = cat $in > $out
  #generator = 1
  restat = 1 # comment
  #comment
build result: cat in_1.cc in-2.O
  #comment
`, s)

	rule, ok := s.Bindings().LookupRule("cat")
	require.True(t, ok)
	require.NotNil(t, rule)
	assert.Equal(t, "cat", rule.Name())

	edge := s.GetNode("result").InEdge()
	require.NotNil(t, edge)
	assert.True(t, edge.GetBindingBool("restat"))
	assert.False(t, edge.GetBindingBool("generator"))
}

func TestIgnoreIndentedBlankLines(t *testing.T) {
	s := state.New()
	test.AssertParse(t, `  #indented comment
  
rule cat
  command = cat $in > $out
  
build result: cat in_1.cc in-2.O
  
variable=1
`, s)

	assert.Equal(t, "1", s.Bindings().LookupVariable("variable"))
}

func TestResponseFiles(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule cat_rsp
  command = cat $rspfile > $out
  rspfile = $rspfile
  rspfile_content = $in

build out: cat_rsp in
  rspfile=out.rsp
`, s)
	assert.Equal(t, 2, len(s.Bindings().GetRules()))
	rule, ok := s.Bindings().LookupRule("cat_rsp")
	require.True(t, ok)
	assert.Equal(t, "cat_rsp", rule.Name())
	eval, ok := rule.GetBinding("command")
	require.True(t, ok)
	assert.Equal(t, "[cat ][$rspfile][ > ][$out]", eval.Serialize())
}

func TestInNewline(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule cat_rsp
  command = cat $in_newline > $out

build out: cat_rsp in in2
  rspfile=out.rsp
`, s)
	assert.Equal(t, 2, len(s.Bindings().GetRules()))
	rule, ok := s.Bindings().LookupRule("cat_rsp")
	require.True(t, ok)
	assert.Equal(t, "cat_rsp", rule.Name())
	eval, ok := rule.GetBinding("command")
	require.True(t, ok)
	assert.Equal(t, "[cat ][$in_newline][ > ][$out]", eval.Serialize())

	edge := s.Edges()[0]
	assert.Equal(t, "cat in\nin2 > out", edge.EvaluateCommand(false))
}

func TestVariables(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`l = one-letter-test
rule link
  command = ld $l $extra $with_under -o $out $in

extra = -pthread
with_under = -under
build a: link b c
nested1 = 1
nested2 = $nested1/2
build supernested: link x
  extra = $nested2/3
`, s)
	assert.Equal(t, 2, len(s.Edges()))
	edge := s.Edges()[0]
	assert.Equal(t, "ld one-letter-test -pthread -under -o a b c", edge.EvaluateCommand(false))
	assert.Equal(t, "1/2", s.Bindings().LookupVariable("nested2"))

	edge = s.Edges()[1]
	assert.Equal(t, "ld one-letter-test 1/2/3 -under -o supernested x", edge.EvaluateCommand(false))
}

func TestVariableScope(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`foo = bar
rule cmd
  command = cmd $foo $in $out

build inner: cmd a
  foo = baz
build outer: cmd b

`, s)
	assert.Equal(t, 2, len(s.Edges()))
	edge := s.Edges()[0]
	assert.Equal(t, "cmd baz a inner", edge.EvaluateCommand(false))

	edge = s.Edges()[1]
	assert.Equal(t, "cmd bar b outer", edge.EvaluateCommand(false))
}

func TestContinuation(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule link
  command = foo bar $
    baz

build a: link c $
 d e f
`, s)
	assert.Equal(t, 2, len(s.Bindings().GetRules()))
	rule, ok := s.Bindings().LookupRule("link")
	require.True(t, ok)
	assert.Equal(t, "link", rule.Name())
	eval, ok := rule.GetBinding("command")
	require.True(t, ok)
	assert.Equal(t, "[foo bar baz]", eval.Serialize())
}

func TestBackslash(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`foo = bar\\baz
foo2 = bar\\ baz
`, s)
	assert.Equal(t, `bar\\baz`, s.Bindings().LookupVariable("foo"))
	assert.Equal(t, `bar\\ baz`, s.Bindings().LookupVariable("foo2"))
}

func TestComment(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`# this is a comment
foo = not # a comment
`, s)
	assert.Equal(t, `not # a comment`, s.Bindings().LookupVariable("foo"))
}

func TestDollars(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule foo
  command = ${out}bar$$baz$$$
blah
x = $$dollar
build $x: foo y
`, s)
	assert.Equal(t, `$dollar`, s.Bindings().LookupVariable("x"))
	edge := s.Edges()[0]
	edge.Dump("testdollars")
	if runtime.GOOS == "windows" {
		assert.Equal(t, "$dollarbar$baz$blah", edge.EvaluateCommand(false))
	} else {
		assert.Equal(t, "'$dollar'bar$baz$blah", edge.EvaluateCommand(false))
	}
}

func TestRules(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule cat
  command = cat $in > $out

rule date
  command = date > $out

build result: cat in_1.cc in-2.O
`, s)
	assert.Equal(t, 3, len(s.Bindings().GetRules()))
	rule, ok := s.Bindings().LookupRule("cat")
	require.True(t, ok)
	assert.Equal(t, "cat", rule.Name())
	eval, ok := rule.GetBinding("command")
	require.True(t, ok)
	assert.Equal(t, "[cat ][$in][ > ][$out]", eval.Serialize())
}

func TestEscapeSpaces(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule spaces
  command = something
build foo$ bar: spaces $$one two$$$ three
`, s)
	assert.NotNil(t, s.LookupNode("foo bar"))
	assert.Equal(t, "foo bar", s.Edges()[0].Outputs()[0].Path())
	assert.Equal(t, "$one", s.Edges()[0].Inputs()[0].Path())
	assert.Equal(t, "two$ three", s.Edges()[0].Inputs()[1].Path())
	assert.Equal(t, "something", s.Edges()[0].EvaluateCommand(false))
}

func TestCanonicalizeFile(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule cat
  command = cat $in > $out
build out: cat in/1 in//2
build in/1: cat
build in/2: cat
`, s)
	assert.NotNil(t, s.LookupNode("in/1"))
	assert.NotNil(t, s.LookupNode("in/2"))

	assert.Nil(t, s.LookupNode(`in//1`))
	assert.Nil(t, s.LookupNode(`in//2`))
}

func TestPathVariables(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule cat
  command = cat $in > $out
dir = out
build $dir/exe: cat src
`, s)
	assert.Nil(t, s.LookupNode("$dir/exe"))
	assert.NotNil(t, s.LookupNode("out/exe"))
}

func TestCanonicalizePaths(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule cat
  command = cat $in > $out
build ./out.o: cat ./bar/baz/../foo.cc
`, s)
	assert.Nil(t, s.LookupNode("./out.o"))
	assert.NotNil(t, s.LookupNode("out.o"))
	assert.Nil(t, s.LookupNode("./bar/baz/../foo.cc"))
	assert.NotNil(t, s.LookupNode("bar/foo.cc"))
}

func TestDuplicateEdgeWithMultipleOutputsError(t *testing.T) {
	s := state.New()
	input := `rule cat
  command = cat $in > $out
build out1 out2: cat in1
build out1: cat in2
build final: cat out1
`
	manifestParser := manifest_parser.New(s, disk.NewMockDiskInterface(), manifest_parser.DefaultOptions())
	err := manifestParser.Parse("", input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple rules generate out1")
}

func TestDuplicateEdgeInIncludedFile(t *testing.T) {
	s := state.New()
	fs := disk.NewMockDiskInterface()
	fs.Create("sub.ninja", []byte(
		`rule cat
  command = cat $in > $out
build out1 out2: cat in1
build out1: cat in2
build final: cat out1
`))
	input := `subninja sub.ninja
`
	manifestParser := manifest_parser.New(s, fs, manifest_parser.DefaultOptions())
	err := manifestParser.Parse("", input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple rules generate out1")
}

func TestPhonySelfReferenceIgnored(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`build a: phony a
`, s)
	node := s.LookupNode("a")
	edge := node.InEdge()
	assert.Empty(t, edge.Inputs())
}

func TestPhonySelfReferenceKept(t *testing.T) {
	s := state.New()
	input := `build a: phony a
`
	opts := manifest_parser.DefaultOptions()
	opts.PhonyCycleAction = manifest_parser.PhonyCycleActionError
	manifestParser := manifest_parser.New(s, disk.NewMockDiskInterface(), opts)
	err := manifestParser.Parse("", input)
	require.NoError(t, err)

	node := s.LookupNode("a")
	edge := node.InEdge()
	assert.Equal(t, 1, len(edge.Inputs()))
	assert.Equal(t, node, edge.Inputs()[0])
}

func TestReservedWords(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule build
  command = rule run $out
build subninja: build include default foo.cc
default subninja
`, s)
}

func TestErrors(t *testing.T) {
	testCases := []struct {
		input string
		err   string
	}{
		{
			input: "subn",
			err:   "expected '=', got eof",
		},
		{
			input: "foobar",
			err:   "expected '=', got eof",
		},
		{
			input: "x 3",
			err:   "expected '=', got identifier",
		},
		{
			input: "x = 3",
			err:   "unexpected EOF",
		},
		{
			input: "x = 3\ny 2",
			err:   "expected '=', got identifier",
		},
		{
			input: "x = $",
			err:   "bad $-escape (literal $ must be written as $$)",
		},
		{
			input: "x = $\n $[\n",
			err:   "bad $-escape (literal $ must be written as $$)",
		},
		{
			input: "x = a$\n b$\n $\n",
			err:   "unexpected EOF",
		},
		{
			input: "build\n",
			err:   "expected path",
		},
		{
			input: "build x: y z\n",
			err:   "unknown build rule 'y'",
		},
		{
			input: "build x:: y z\n",
			err:   "expected build command name",
		},
		{
			input: "rule cat\n  command = cat ok\nbuild x: cat $\n :\n",
			err:   "expected newline, got ':'",
		},
		{
			input: "rule cat\n",
			err:   "expected 'command =' line",
		},
		{
			input: "rule cat\n  command = echo\nrule cat\n  command = echo\n",
			err:   "duplicate rule 'cat'",
		},
		{
			input: "rule cat\n  command = echo\n  rspfile = cat.rsp\n",
			err:   "rspfile and rspfile_content need to be both specified",
		},
		{
			input: "rule cat\n  command = ${fafsd\nfoo = bar\n",
			err:   "bad $-escape (literal $ must be written as $$)",
		},
		{
			input: "rule cat\n  command = cat\nbuild $.: cat foo\n",
			err:   "bad $-escape (literal $ must be written as $$)",
		},
		{
			input: "rule cat\n  command = cat\nbuild $: cat foo\n",
			err:   "expected ':', got newline ($ also escapes ':')",
		},
		{
			input: "rule %foo\n",
			err:   "expected rule name",
		},
		{
			input: "rule cc\n  command = foo\n  othervar = bar\n",
			err:   "unexpected variable 'othervar'",
		},
		{
			input: "rule cc\n  command = foo\nbuild $.: cc bar.cc\n",
			err:   "bad $-escape (literal $ must be written as $$)",
		},
		{
			input: "rule cc\n  command = foo\n  && bar",
			err:   "expected variable name",
		},
		{
			input: "rule cc\n  command = foo\nbuild $: cc bar.cc\n",
			err:   "expected ':', got newline ($ also escapes ':')",
		},
		{
			input: "default\n",
			err:   "expected target name",
		},
		{
			input: "default nonexistent\n",
			err:   "unknown target 'nonexistent'",
		},
		{
			input: "rule r\n  command = r\nbuild b: r\ndefault b:\n",
			err:   "expected newline, got ':'",
		},
		{
			input: "default $a\n",
			err:   "empty path",
		},
		{
			input: "rule r\n  command = r\nbuild $a: r $c\n",
			err:   "empty path",
		},
		{
			input: "rule r\n  command = r\n  \n  generator = 1\n",
			err:   "unexpected indent",
		},
		{
			input: "pool\n",
			err:   "expected pool name",
		},
		{
			input: "pool foo\n",
			err:   "expected 'depth =' line",
		},
		{
			input: "pool foo\n  depth = 4\npool foo\n",
			err:   "duplicate pool 'foo'",
		},
		{
			input: "pool foo\n  depth = -1\n",
			err:   "invalid pool depth",
		},
		{
			input: "pool foo\n  bar = 1\n",
			err:   "unexpected variable 'bar'",
		},
		{
			input: "rule run\n  command = echo\n  pool = unnamed_pool\nbuild out: run in\n",
			err:   "unknown pool name 'unnamed_pool'",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.err, func(t *testing.T) {
			s := state.New()
			manifestParser := manifest_parser.New(s, disk.NewMockDiskInterface(), manifest_parser.DefaultOptions())
			err := manifestParser.Parse("", tc.input)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.err)
		})
	}
}

func TestMissingInput(t *testing.T) {
	s := state.New()
	fs := disk.NewMockDiskInterface()
	manifestParser := manifest_parser.New(s, fs, manifest_parser.DefaultOptions())
	err := manifestParser.ParseFile("build.ninja")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading 'build.ninja'")
	assert.Contains(t, err.Error(), "file does not exist")
}

func TestMultipleOutputs(t *testing.T) {
	s := state.New()
	manifestParser := manifest_parser.New(s, disk.NewMockDiskInterface(), manifest_parser.DefaultOptions())
	err := manifestParser.Parse("", `rule cc
  command = foo
  depfile = bar
build a.o b.o: cc c.cc
`)
	require.NoError(t, err)
}

func TestMultipleOutputsWithDeps(t *testing.T) {
	s := state.New()
	manifestParser := manifest_parser.New(s, disk.NewMockDiskInterface(), manifest_parser.DefaultOptions())
	err := manifestParser.Parse("", `rule cc
  command = foo
  deps = gcc
build a.o b.o: cc c.cc
`)
	require.NoError(t, err)
}

func TestSubNinja(t *testing.T) {
	s := state.New()
	fs := disk.NewMockDiskInterface()
	fs.Create("test.ninja", []byte(
		`var = inner
build $builddir/inner: varref
`))
	manifestParser := manifest_parser.New(s, fs, manifest_parser.DefaultOptions())
	assert.NoError(t, manifestParser.Parse("", `builddir = some_dir/
rule varref
  command = varref $var
var = outer
build $builddir/outer: varref
subninja test.ninja
build $builddir/outer2: varref
`))
	test.VerifyGraph(t, s)

	assert.Equal(t, 1, len(fs.FilesRead()))
	assert.Equal(t, "test.ninja", fs.FilesRead()[0])
	assert.NotNil(t, s.LookupNode("some_dir/outer"))
	// Verify our builddir setting is inherited.
	assert.NotNil(t, s.LookupNode("some_dir/inner"))

	assert.Equal(t, 3, len(s.Edges()))
	assert.Equal(t, "varref outer", s.Edges()[0].EvaluateCommand(false))
	assert.Equal(t, "varref inner", s.Edges()[1].EvaluateCommand(false))
	assert.Equal(t, "varref outer", s.Edges()[2].EvaluateCommand(false))
}

func TestMissingSubNinja(t *testing.T) {
	s := state.New()
	fs := disk.NewMockDiskInterface()
	manifestParser := manifest_parser.New(s, fs, manifest_parser.DefaultOptions())
	err := manifestParser.Parse("", "subninja foo.ninja\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading 'foo.ninja'")
	assert.Contains(t, err.Error(), "file does not exist")
}

func TestDuplicateRuleInDifferentSubninjas(t *testing.T) {
	// Test that rules are scoped to subninjas.
	s := state.New()
	fs := disk.NewMockDiskInterface()
	fs.Create("test.ninja", []byte(`rule cat
  command = cat
`))
	manifestParser := manifest_parser.New(s, fs, manifest_parser.DefaultOptions())
	err := manifestParser.Parse("", `rule cat
  command = cat
subninja test.ninja
`)
	require.NoError(t, err)
}

func TestDuplicateRuleInDifferentSubninjasWithInclude(t *testing.T) {
	// Test that rules are scoped to subninjas even with includes.
	s := state.New()
	fs := disk.NewMockDiskInterface()
	fs.Create("rules.ninja", []byte(`rule cat
  command = cat
`))
	fs.Create("test.ninja", []byte(`include rules.ninja
build x : cat
`))
	manifestParser := manifest_parser.New(s, fs, manifest_parser.DefaultOptions())
	err := manifestParser.Parse("", `include rules.ninja
subninja test.ninja
build y : cat
`)
	require.NoError(t, err)
}

func TestInclude(t *testing.T) {
	s := state.New()
	fs := disk.NewMockDiskInterface()
	fs.Create("include.ninja", []byte("var = inner\n"))

	manifestParser := manifest_parser.New(s, fs, manifest_parser.DefaultOptions())
	assert.NoError(t, manifestParser.Parse("", `var = outer
include include.ninja
`))
	test.VerifyGraph(t, s)
	assert.Equal(t, 1, len(fs.FilesRead()))
	assert.Equal(t, "include.ninja", fs.FilesRead()[0])
	assert.Equal(t, "inner", s.Bindings().LookupVariable("var"))
}

func TestBrokenInclude(t *testing.T) {
	s := state.New()
	fs := disk.NewMockDiskInterface()
	fs.Create("include.ninja", []byte("build\n"))
	manifestParser := manifest_parser.New(s, fs, manifest_parser.DefaultOptions())
	err := manifestParser.Parse("", "include include.ninja\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected path")
}

func TestImplicit(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule cat
  command = cat $in > $out
build foo: cat bar | baz
`, s)
	edge := s.LookupNode("foo").InEdge()
	require.Equal(t, 1, len(edge.ExplicitInputs()))
	require.Equal(t, 1, len(edge.ImplicitInputs()))
	assert.Equal(t, "baz", edge.ImplicitInputs()[0].Path())
}

func TestOrderOnly(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule cat
  command = cat $in > $out
build foo: cat bar || baz
`, s)
	edge := s.LookupNode("foo").InEdge()
	require.Equal(t, 1, len(edge.ExplicitInputs()))
	require.Equal(t, 1, len(edge.OrderOnlyInputs()))
	assert.Equal(t, "baz", edge.OrderOnlyInputs()[0].Path())
}

func TestValidations(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule cat
  command = cat $in > $out
build foo: cat bar |@ baz
`, s)
	edge := s.LookupNode("foo").InEdge()
	assert.Equal(t, 1, len(edge.Validations()))
	assert.Equal(t, "baz", edge.Validations()[0].Path())
}

func TestImplicitOutput(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule cat
  command = cat $in > $out
build foo | imp: cat bar
`, s)
	edge := s.LookupNode("imp").InEdge()
	assert.Equal(t, 2, len(edge.Outputs()))
	assert.True(t, edge.IsImplicitOut(1))
}

func TestImplicitOutputEmpty(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule cat
  command = cat $in > $out
build foo | : cat bar
`, s)
	edge := s.LookupNode("foo").InEdge()
	assert.Equal(t, 1, len(edge.Outputs()))
	assert.False(t, edge.IsImplicitOut(0))
}

func TestImplicitOutputDupeError(t *testing.T) {
	s := state.New()
	input := `rule cat
  command = cat $in > $out
build foo baz | foo baq foo: cat bar
`
	manifestParser := manifest_parser.New(s, disk.NewMockDiskInterface(), manifest_parser.DefaultOptions())
	err := manifestParser.Parse("", input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "foo is defined as an output multiple times")
}

func TestImplicitOutputDupesError(t *testing.T) {
	s := state.New()
	input := `rule cat
  command = cat $in > $out
build foo foo foo | foo foo foo foo: cat bar
`
	manifestParser := manifest_parser.New(s, disk.NewMockDiskInterface(), manifest_parser.DefaultOptions())
	err := manifestParser.Parse("", input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "foo is defined as an output multiple times")
}

func TestNoExplicitOutput(t *testing.T) {
	s := state.New()
	manifestParser := manifest_parser.New(s, disk.NewMockDiskInterface(), manifest_parser.DefaultOptions())
	err := manifestParser.Parse("", `rule cat
  command = cat $in > $out
build | imp : cat bar
`)
	require.NoError(t, err)
}

func TestDefaultDefault(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule cat
  command = cat $in > $out
build a: cat foo
build b: cat foo
build c: cat foo
build d: cat foo
`, s)
	nodes, err := s.DefaultNodes()
	require.NoError(t, err)
	assert.Equal(t, 4, len(nodes))
}

func TestDefaultDefaultCycle(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule cat
  command = cat $in > $out
build a: cat a
`, s)
	nodes, err := s.DefaultNodes()
	assert.Error(t, err)
	assert.Equal(t, 0, len(nodes))
	assert.Contains(t, err.Error(), "could not determine root nodes of build graph")
}

func TestDefaultStatements(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule cat
  command = cat $in > $out
build a: cat foo
build b: cat foo
build c: cat foo
build d: cat foo
third = c
default a b
default $third
`, s)
	nodes, err := s.DefaultNodes()
	require.NoError(t, err)
	assert.Equal(t, 3, len(nodes))
	assert.Equal(t, "a", nodes[0].Path())
	assert.Equal(t, "b", nodes[1].Path())
	assert.Equal(t, "c", nodes[2].Path())
}

func TestUTF8(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		"rule utf8\n"+
			"  command = true\n"+
			"  description = compilaci\xC3\xB3\n", s)
}

func TestCRLF(t *testing.T) {
	s := state.New()
	manifestParser := manifest_parser.New(s, disk.NewMockDiskInterface(), manifest_parser.DefaultOptions())
	err := manifestParser.Parse("", "# comment with crlf\r\n")
	require.NoError(t, err)
	err = manifestParser.Parse("", "foo = foo\nbar = bar\r\n")
	require.NoError(t, err)
	err = manifestParser.Parse("",
		"pool link_pool\r\n"+
			"  depth = 15\r\n\r\n"+
			"rule xyz\r\n"+
			"  command = something$expand \r\n"+
			"  description = YAY!\r\n")
	require.NoError(t, err)
}

func TestDyndepNotSpecified(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule cat
  command = cat $in > $out
build result: cat in
`, s)
	edge := s.GetNode("result").InEdge()
	assert.Nil(t, edge.Dyndep())
}

func TestDyndepNotInput(t *testing.T) {
	s := state.New()
	manifestParser := manifest_parser.New(s, disk.NewMockDiskInterface(), manifest_parser.DefaultOptions())
	err := manifestParser.Parse("", `rule touch
  command = touch $out
build result: touch
  dyndep = notin
`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dyndep 'notin' is not an input")
}

func TestDyndepExplicitInput(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule cat
  command = cat $in > $out
build result: cat in
  dyndep = in
`, s)
	edge := s.GetNode("result").InEdge()
	require.NotNil(t, edge.Dyndep())
	assert.True(t, edge.Dyndep().DyndepPending())
	assert.Equal(t, "in", edge.Dyndep().Path())
}

func TestDyndepImplicitInput(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule cat
  command = cat $in > $out
build result: cat in | dd
  dyndep = dd
`, s)
	edge := s.GetNode("result").InEdge()
	require.NotNil(t, edge.Dyndep())
	assert.True(t, edge.Dyndep().DyndepPending())
	assert.Equal(t, "dd", edge.Dyndep().Path())
}

func TestDyndepOrderOnlyInput(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule cat
  command = cat $in > $out
build result: cat in || dd
  dyndep = dd
`, s)
	edge := s.GetNode("result").InEdge()
	require.NotNil(t, edge.Dyndep())
	assert.True(t, edge.Dyndep().DyndepPending())
	assert.Equal(t, "dd", edge.Dyndep().Path())
}

func TestDyndepRuleInput(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule cat
  command = cat $in > $out
  dyndep = $in
build result: cat in
`, s)
	edge := s.GetNode("result").InEdge()
	require.NotNil(t, edge.Dyndep())
	assert.True(t, edge.Dyndep().DyndepPending())
	assert.Equal(t, "in", edge.Dyndep().Path())
}
