package lexer_test

import (
	"testing"

	"github.com/buildbuddy-io/gin/internal/lexer"
	//	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadVarValue(t *testing.T) {
	lexer := lexer.NewWithInput("plain text $var $VaR ${x}\n")
	eval, err := lexer.ReadVarValue()
	require.NoError(t, err)
	require.Equal(t, "[plain text ][$var][ ][$VaR][ ][$x]", eval.Serialize())
}

func TestReadEvalStringEscapes(t *testing.T) {
	lexer := lexer.NewWithInput("$ $$ab c$: $\ncde\n")
	eval, err := lexer.ReadVarValue()
	require.NoError(t, err)
	require.Equal(t, "[ $ab c: cde]", eval.Serialize())
}

func TestReadIdent(t *testing.T) {
	lexer := lexer.NewWithInput("foo baR baz_123 foo-bar")
	ident, ok := lexer.ReadIdent()
	require.True(t, ok)
	require.Equal(t, "foo", ident)

	ident, ok = lexer.ReadIdent()
	require.True(t, ok)
	require.Equal(t, "baR", ident)

	ident, ok = lexer.ReadIdent()
	require.True(t, ok)
	require.Equal(t, "baz_123", ident)

	ident, ok = lexer.ReadIdent()
	require.True(t, ok)
	require.Equal(t, "foo-bar", ident)
}

func TestReadIdentCurlies(t *testing.T) {
	l := lexer.NewWithInput("foo.dots $bar.dots ${bar.dots}\n")

	ident, ok := l.ReadIdent()
	require.True(t, ok)
	require.Equal(t, "foo.dots", ident)

	eval, err := l.ReadVarValue()
	require.NoError(t, err)
	require.Equal(t, "[$bar][.dots ][$bar.dots]", eval.Serialize())
}

func TestError(t *testing.T) {
	l := lexer.NewWithInput("foo$\nbad $")
	_, err := l.ReadVarValue()
	require.Error(t, err)
	// The error message should indicate a bad $-escape on line 2
	require.Contains(t, err.Error(), "2:")
	require.Contains(t, err.Error(), "$-escape")
}

func TestCommentEOF(t *testing.T) {
	// Verify we don't run off the end of the string when the EOF is mid-comment.
	l := lexer.NewWithInput("# foo")
	token := l.ReadToken()
	// In the Go implementation, comments are handled in eatWhitespace,
	// so we should get EOF after the comment
	require.Equal(t, lexer.EOF, token)
}

func TestTabs(t *testing.T) {
	// Verify we print a useful error on a disallowed character.
	l := lexer.NewWithInput("   \tfoobar")
	token := l.ReadToken()
	require.Equal(t, lexer.INDENT, token)
	
	// Next token should be ERROR because tabs are not allowed
	token = l.ReadToken()
	require.Equal(t, lexer.ERROR, token)
	require.Equal(t, "tabs are not allowed, use spaces", l.DescribeLastError())
}
