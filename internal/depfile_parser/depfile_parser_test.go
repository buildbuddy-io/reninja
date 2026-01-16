package depfile_parser_test

import (
	"testing"

	"github.com/buildbuddy-io/reninja/internal/depfile_parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBasic(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("build/ninja.o: ninja.cc ninja.h eval_env.h manifest_parser.h\n")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "build/ninja.o", p.Outs()[0])
	assert.Equal(t, 4, len(p.Ins()))
}

func TestEarlyNewlineAndWhitespace(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse(
		` \\
  out: in
`)
	require.NoError(t, err)
}

func TestContinuation(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("foo.o: \\\n  bar.h baz.h\n")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "foo.o", p.Outs()[0])
	assert.Equal(t, 2, len(p.Ins()))
}

func TestWindowsDrivePaths(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("foo.o: //?/c:/bar.h\n")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "foo.o", p.Outs()[0])
	assert.Equal(t, 1, len(p.Ins()))
	assert.Equal(t, "//?/c:/bar.h", p.Ins()[0])
}

func TestAmpersandsAndQuotes(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("foo&bar.o foo'bar.o foo\"bar.o: foo&bar.h foo'bar.h foo\"bar.h\n")
	require.NoError(t, err)
	assert.Equal(t, 3, len(p.Outs()))
	assert.Equal(t, "foo&bar.o", p.Outs()[0])
	assert.Equal(t, "foo'bar.o", p.Outs()[1])
	assert.Equal(t, "foo\"bar.o", p.Outs()[2])
	assert.Equal(t, 3, len(p.Ins()))
	assert.Equal(t, "foo&bar.h", p.Ins()[0])
	assert.Equal(t, "foo'bar.h", p.Ins()[1])
	assert.Equal(t, "foo\"bar.h", p.Ins()[2])
}

func TestCarriageReturnContinuation(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("foo.o: \\\r\n  bar.h baz.h\r\n")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "foo.o", p.Outs()[0])
	assert.Equal(t, 2, len(p.Ins()))
}

func TestBackSlashes(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse(
		"Project\\Dir\\Build\\Release8\\Foo\\Foo.res : \\\n" +
			"  Dir\\Library\\Foo.rc \\\n" +
			"  Dir\\Library\\Version\\Bar.h \\\n" +
			"  Dir\\Library\\Foo.ico \\\n" +
			"  Project\\Thing\\Bar.tlb \\\n")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "Project\\Dir\\Build\\Release8\\Foo\\Foo.res", p.Outs()[0])
	assert.Equal(t, 4, len(p.Ins()))
}

func TestSpaces(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("a\\ bc\\ def:   a\\ b c d")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "a bc def", p.Outs()[0])
	assert.Equal(t, 3, len(p.Ins()))
	assert.Equal(t, "a b", p.Ins()[0])
	assert.Equal(t, "c", p.Ins()[1])
	assert.Equal(t, "d", p.Ins()[2])
}

func TestMultipleBackslashes(t *testing.T) {
	// Successive 2N+1 backslashes followed by space (' ') are replaced by N >= 0
	// backslashes and the space. A single backslash before hash sign is removed.
	// Other backslashes remain untouched (including 2N backslashes followed by
	// space).
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("a\\ b\\#c.h: \\\\\\\\\\  \\\\\\\\ \\\\share\\info\\\\#1")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "a b#c.h", p.Outs()[0])
	assert.Equal(t, 3, len(p.Ins()))
	assert.Equal(t, "\\\\ ", p.Ins()[0])
	assert.Equal(t, "\\\\\\\\", p.Ins()[1])
	assert.Equal(t, "\\\\share\\info\\#1", p.Ins()[2])
}

func TestEscapes(t *testing.T) {
	// Put backslashes before a variety of characters, see which ones make
	// it through.
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("\\!\\@\\#$$\\%\\^\\&\\[\\]\\\\:")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "\\!\\@#$\\%\\^\\&\\[\\]\\\\", p.Outs()[0])
	assert.Equal(t, 0, len(p.Ins()))
}

func TestEscapedColons(t *testing.T) {
	// Tests for correct parsing of depfiles produced on Windows
	// by both Clang, GCC pre 10 and GCC 10
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse(
		"c\\:\\gcc\\x86_64-w64-mingw32\\include\\stddef.o: \\\n" +
			" c:\\gcc\\x86_64-w64-mingw32\\include\\stddef.h \n")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "c:\\gcc\\x86_64-w64-mingw32\\include\\stddef.o", p.Outs()[0])
	assert.Equal(t, 1, len(p.Ins()))
	assert.Equal(t, "c:\\gcc\\x86_64-w64-mingw32\\include\\stddef.h", p.Ins()[0])
}

func TestEscapedTargetColon(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse(
		"foo1\\: x\n" +
			"foo1\\:\n" +
			"foo1\\:\r\n" +
			"foo1\\:\t\n" +
			"foo1\\:")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "foo1\\", p.Outs()[0])
	assert.Equal(t, 1, len(p.Ins()))
	assert.Equal(t, "x", p.Ins()[0])
}

func TestSpecialChars(t *testing.T) {
	// See filenames like istreambuf.iterator_op!= in
	// https://github.com/google/libcxx/tree/master/test/iterators/stream.iterators/istreambuf.iterator/
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse(
		"C:/Program\\ Files\\ (x86)/Microsoft\\ crtdefs.h: \\\n" +
			" en@quot.header~ t+t-x!=1 \\\n" +
			" openldap/slapd.d/cn=config/cn=schema/cn={0}core.ldif\\\n" +
			" Fu\303\244ball\\\n" +
			" a[1]b@2%c")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "C:/Program Files (x86)/Microsoft crtdefs.h", p.Outs()[0])
	assert.Equal(t, 5, len(p.Ins()))
	assert.Equal(t, "en@quot.header~", p.Ins()[0])
	assert.Equal(t, "t+t-x!=1", p.Ins()[1])
	assert.Equal(t, "openldap/slapd.d/cn=config/cn=schema/cn={0}core.ldif", p.Ins()[2])
	assert.Equal(t, "Fu\303\244ball", p.Ins()[3])
	assert.Equal(t, "a[1]b@2%c", p.Ins()[4])
}

func TestUnifyMultipleOutputs(t *testing.T) {
	// check that multiple duplicate targets are properly unified
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("foo foo: x y z")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "foo", p.Outs()[0])
	assert.Equal(t, 3, len(p.Ins()))
	assert.Equal(t, "x", p.Ins()[0])
	assert.Equal(t, "y", p.Ins()[1])
	assert.Equal(t, "z", p.Ins()[2])
}

func TestMultipleDifferentOutputs(t *testing.T) {
	// check that multiple different outputs are accepted by the parser
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("foo bar: x y z")
	require.NoError(t, err)
	assert.Equal(t, 2, len(p.Outs()))
	assert.Equal(t, "foo", p.Outs()[0])
	assert.Equal(t, "bar", p.Outs()[1])
	assert.Equal(t, 3, len(p.Ins()))
	assert.Equal(t, "x", p.Ins()[0])
	assert.Equal(t, "y", p.Ins()[1])
	assert.Equal(t, "z", p.Ins()[2])
}

func TestMultipleEmptyRules(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("foo: x\nfoo: \nfoo:\n")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "foo", p.Outs()[0])
	assert.Equal(t, 1, len(p.Ins()))
	assert.Equal(t, "x", p.Ins()[0])
}

func TestUnifyMultipleRulesLF(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("foo: x\nfoo: y\nfoo \\\nfoo: z\n")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "foo", p.Outs()[0])
	assert.Equal(t, 3, len(p.Ins()))
	assert.Equal(t, "x", p.Ins()[0])
	assert.Equal(t, "y", p.Ins()[1])
	assert.Equal(t, "z", p.Ins()[2])
}

func TestUnifyMultipleRulesCRLF(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("foo: x\r\nfoo: y\r\nfoo \\\r\nfoo: z\r\n")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "foo", p.Outs()[0])
	assert.Equal(t, 3, len(p.Ins()))
	assert.Equal(t, "x", p.Ins()[0])
	assert.Equal(t, "y", p.Ins()[1])
	assert.Equal(t, "z", p.Ins()[2])
}

func TestUnifyMixedRulesLF(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("foo: x\\\n     y\nfoo \\\nfoo: z\n")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "foo", p.Outs()[0])
	assert.Equal(t, 3, len(p.Ins()))
	assert.Equal(t, "x", p.Ins()[0])
	assert.Equal(t, "y", p.Ins()[1])
	assert.Equal(t, "z", p.Ins()[2])
}

func TestUnifyMixedRulesCRLF(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("foo: x\\\r\n     y\r\nfoo \\\r\nfoo: z\r\n")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "foo", p.Outs()[0])
	assert.Equal(t, 3, len(p.Ins()))
	assert.Equal(t, "x", p.Ins()[0])
	assert.Equal(t, "y", p.Ins()[1])
	assert.Equal(t, "z", p.Ins()[2])
}

func TestIndentedRulesLF(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse(" foo: x\n foo: y\n foo: z\n")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "foo", p.Outs()[0])
	assert.Equal(t, 3, len(p.Ins()))
	assert.Equal(t, "x", p.Ins()[0])
	assert.Equal(t, "y", p.Ins()[1])
	assert.Equal(t, "z", p.Ins()[2])
}

func TestIndentedRulesCRLF(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse(" foo: x\r\n foo: y\r\n foo: z\r\n")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "foo", p.Outs()[0])
	assert.Equal(t, 3, len(p.Ins()))
	assert.Equal(t, "x", p.Ins()[0])
	assert.Equal(t, "y", p.Ins()[1])
	assert.Equal(t, "z", p.Ins()[2])
}

func TestTolerateMP(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("foo: x y z\nx:\ny:\nz:\n")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "foo", p.Outs()[0])
	assert.Equal(t, 3, len(p.Ins()))
	assert.Equal(t, "x", p.Ins()[0])
	assert.Equal(t, "y", p.Ins()[1])
	assert.Equal(t, "z", p.Ins()[2])
}

func TestMultipleRulesTolerateMP(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("foo: x\nx:\nfoo: y\ny:\nfoo: z\nz:\n")
	require.NoError(t, err)
	assert.Equal(t, 1, len(p.Outs()))
	assert.Equal(t, "foo", p.Outs()[0])
	assert.Equal(t, 3, len(p.Ins()))
	assert.Equal(t, "x", p.Ins()[0])
	assert.Equal(t, "y", p.Ins()[1])
	assert.Equal(t, "z", p.Ins()[2])
}

func TestMultipleRulesDifferentOutputs(t *testing.T) {
	// check that multiple different outputs are accepted by the parser
	// when spread across multiple rules
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("foo: x y\nbar: y z\n")
	require.NoError(t, err)
	assert.Equal(t, 2, len(p.Outs()))
	assert.Equal(t, "foo", p.Outs()[0])
	assert.Equal(t, "bar", p.Outs()[1])
	assert.Equal(t, 3, len(p.Ins()))
	assert.Equal(t, "x", p.Ins()[0])
	assert.Equal(t, "y", p.Ins()[1])
	assert.Equal(t, "z", p.Ins()[2])
}

func TestBuggyMP(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("foo: x y z\nx: alsoin\ny:\nz:\n")
	require.Error(t, err)
	assert.Equal(t, "inputs may not also have inputs", err.Error())
}

func TestEmptyFile(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("")
	require.NoError(t, err)
	assert.Equal(t, 0, len(p.Outs()))
	assert.Equal(t, 0, len(p.Ins()))
}

func TestEmptyLines(t *testing.T) {
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("\n\n")
	require.NoError(t, err)
	assert.Equal(t, 0, len(p.Outs()))
	assert.Equal(t, 0, len(p.Ins()))
}

func TestMissingColon(t *testing.T) {
	// The file is not empty but is missing a colon separator.
	p := depfile_parser.New(depfile_parser.DefaultOptions())
	err := p.Parse("foo.o foo.c\n")
	require.Error(t, err)
	assert.Equal(t, "expected ':' in depfile", err.Error())
}
