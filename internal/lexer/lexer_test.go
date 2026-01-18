package lexer_test

import (
	"testing"

	"github.com/buildbuddy-io/reninja/internal/lexer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadVarValue(t *testing.T) {
	lexer := lexer.NewWithInput("plain text $var $VaR ${x}\n")
	eval, err := lexer.ReadVarValue()
	require.NoError(t, err)
	assert.Equal(t, "[plain text ][$var][ ][$VaR][ ][$x]", eval.Serialize())
}

func TestReadEvalStringEscapes(t *testing.T) {
	lexer := lexer.NewWithInput("$ $$ab c$: $\ncde\n")
	eval, err := lexer.ReadVarValue()
	require.NoError(t, err)
	assert.Equal(t, "[ $ab c: cde]", eval.Serialize())
}

func TestReadIdent(t *testing.T) {
	lexer := lexer.NewWithInput("foo baR baz_123 foo-bar")
	ident, ok := lexer.ReadIdent()
	require.True(t, ok)
	assert.Equal(t, "foo", ident)

	ident, ok = lexer.ReadIdent()
	require.True(t, ok)
	assert.Equal(t, "baR", ident)

	ident, ok = lexer.ReadIdent()
	require.True(t, ok)
	assert.Equal(t, "baz_123", ident)

	ident, ok = lexer.ReadIdent()
	require.True(t, ok)
	assert.Equal(t, "foo-bar", ident)
}

func TestReadIdentCurlies(t *testing.T) {
	l := lexer.NewWithInput("foo.dots $bar.dots ${bar.dots}\n")

	ident, ok := l.ReadIdent()
	require.True(t, ok)
	assert.Equal(t, "foo.dots", ident)

	eval, err := l.ReadVarValue()
	require.NoError(t, err)
	assert.Equal(t, "[$bar][.dots ][$bar.dots]", eval.Serialize())
}

func TestError(t *testing.T) {
	l := lexer.NewWithInput("foo$\nbad $")
	_, err := l.ReadVarValue()
	require.Error(t, err)

	// The error message should indicate a bad $-escape on line 2
	assert.Contains(t, err.Error(), "2:")
	assert.Contains(t, err.Error(), "$-escape")
}

func TestCommentEOF(t *testing.T) {
	// Verify we don't run off the end of the string when the EOF is mid-comment.
	l := lexer.NewWithInput("# foo")
	token := l.ReadToken()
	assert.Equal(t, lexer.ERROR, token)
}

func TestTabs(t *testing.T) {
	// Verify we print a useful error on a disallowed character.
	l := lexer.NewWithInput("   \tfoobar")
	token := l.ReadToken()
	assert.Equal(t, lexer.INDENT, token)

	// Next token should be ERROR because tabs are not allowed
	token = l.ReadToken()
	assert.Equal(t, lexer.ERROR, token)
	assert.Equal(t, "tabs are not allowed, use spaces", l.DescribeLastError())
}

func TestEscapedNewlines(t *testing.T) {
	l := lexer.NewWithInput("foo$\nbar$^newline foo\n")

	eval, err := l.ReadVarValue()
	require.NoError(t, err)
	assert.Equal(t, "[foobar\nnewline foo]", eval.Serialize())
}
