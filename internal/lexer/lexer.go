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

package lexer

import (
	"fmt"
	"strings"

	"github.com/buildbuddy-io/gin/internal/graph"
)

// Token represents a lexical token
type Token int

const (
	ERROR Token = iota
	BUILD
	COLON
	DEFAULT
	EQUALS
	IDENT
	INCLUDE
	INDENT
	NEWLINE
	PIPE
	PIPE2
	PIPEAT
	POOL
	RULE
	SUBNINJA
	EOF
)

// TokenName returns a human-readable form of a token
func TokenName(t Token) string {
	switch t {
	case ERROR:
		return "error"
	case BUILD:
		return "build"
	case COLON:
		return ":"
	case DEFAULT:
		return "default"
	case EQUALS:
		return "="
	case IDENT:
		return "identifier"
	case INCLUDE:
		return "include"
	case INDENT:
		return "indent"
	case NEWLINE:
		return "newline"
	case PIPE:
		return "|"
	case PIPE2:
		return "||"
	case PIPEAT:
		return "|@"
	case POOL:
		return "pool"
	case RULE:
		return "rule"
	case SUBNINJA:
		return "subninja"
	case EOF:
		return "eof"
	default:
		return fmt.Sprintf("unknown(%d)", t)
	}
}

// TokenErrorHint returns a hint for expected tokens
func TokenErrorHint(expected Token) string {
	switch expected {
	case COLON:
		return " ($ also escapes ':')"
	default:
		return ""
	}
}

// Lexer tokenizes ninja build files
type Lexer struct {
	filename  string
	input     []byte
	pos       int    // current position in input
	lastPos   int    // position before last token
	lastToken Token  // last token read
	lastError string // last error message
}

// New creates a new Lexer
func New() *Lexer {
	return &Lexer{}
}

// NewWithInput creates a new Lexer with input
func NewWithInput(input string) *Lexer {
	l := &Lexer{}
	l.Start("input", input)
	return l
}

// Start initializes the lexer with input
func (l *Lexer) Start(filename, input string) {
	l.filename = filename
	l.input = []byte(input)
	l.pos = 0
	l.lastPos = 0
	l.lastToken = ERROR
	l.lastError = ""
}

// DescribeLastError returns the last error message
func (l *Lexer) DescribeLastError() string {
	return l.lastError
}

// Error constructs an error message with context
func (l *Lexer) Error(message string) error {
	line, col := l.position()
	l.lastError = fmt.Sprintf("%s:%d:%d: %s", l.filename, line, col, message)
	return fmt.Errorf("%s", l.lastError)
}

// position returns the current line and column
func (l *Lexer) position() (line, col int) {
	line = 1
	col = 1
	for i := 0; i < l.pos && i < len(l.input); i++ {
		if l.input[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return
}

// ReadToken reads the next token
func (l *Lexer) ReadToken() Token {
	l.lastPos = l.pos

	// Check for indent BEFORE eating whitespace
	if l.pos < len(l.input) && l.input[l.pos] == ' ' && l.isStartOfLine() {
		start := l.pos
		for l.pos < len(l.input) && l.input[l.pos] == ' ' {
			l.pos++
		}
		if l.pos > start {
			l.lastToken = INDENT
			return INDENT
		}
	}

	l.eatWhitespace()

	if l.pos >= len(l.input) {
		l.lastToken = EOF
		return EOF
	}

	c := l.input[l.pos]

	// Check for keywords and identifiers
	if isIdentStart(c) {
		start := l.pos
		for l.pos < len(l.input) && isIdentChar(l.input[l.pos]) {
			l.pos++
		}
		word := string(l.input[start:l.pos])

		var token Token
		switch word {
		case "build":
			token = BUILD
		case "rule":
			token = RULE
		case "pool":
			token = POOL
		case "default":
			token = DEFAULT
		case "include":
			token = INCLUDE
		case "subninja":
			token = SUBNINJA
		default:
			// Not a keyword, treat as identifier
			token = IDENT
		}

		l.lastToken = token
		return token
	}

	// Single character tokens
	switch c {
	case '\n':
		l.pos++
		l.lastToken = NEWLINE
		return NEWLINE

	case ':':
		l.pos++
		l.lastToken = COLON
		return COLON

	case '=':
		l.pos++
		l.lastToken = EQUALS
		return EQUALS

	case '|':
		l.pos++
		if l.pos < len(l.input) {
			next := l.input[l.pos]
			if next == '|' {
				l.pos++
				l.lastToken = PIPE2
				return PIPE2
			} else if next == '@' {
				l.pos++
				l.lastToken = PIPEAT
				return PIPEAT
			}
		}
		l.lastToken = PIPE
		return PIPE

	default:
		l.lastError = fmt.Sprintf("unexpected character '%c'", c)
		l.lastToken = ERROR
		return ERROR
	}
}

// UnreadToken rewinds to the last read token
func (l *Lexer) UnreadToken() {
	l.pos = l.lastPos
}

// PeekToken checks if the next token matches
func (l *Lexer) PeekToken(token Token) bool {
	t := l.ReadToken()
	l.UnreadToken()
	return t == token
}

// ReadIdent reads a simple identifier
func (l *Lexer) ReadIdent() (string, bool) {
	l.eatWhitespace()

	if l.pos >= len(l.input) || !isIdentStart(l.input[l.pos]) {
		return "", false
	}

	start := l.pos
	for l.pos < len(l.input) && isIdentChar(l.input[l.pos]) {
		l.pos++
	}
	return string(l.input[start:l.pos]), true
}

// ReadPath reads a path with $escapes
func (l *Lexer) ReadPath() (*graph.EvalString, error) {
	return l.readEvalString(true)
}

// ReadVarValue reads a variable value with $escapes
func (l *Lexer) ReadVarValue() (*graph.EvalString, error) {
	return l.readEvalString(false)
}

// readEvalString reads a $-escaped string
func (l *Lexer) readEvalString(isPath bool) (*graph.EvalString, error) {
	es := &graph.EvalString{}
	var currentText strings.Builder
	l.eatWhitespace()

	for l.pos < len(l.input) {
		c := l.input[l.pos]

		// Check for delimiters
		if isPath {
			if c == ':' || c == '|' || c == ' ' || c == '\n' || c == '\r' {
				break
			}
		} else {
			if c == '\n' || c == '\r' {
				break
			}
		}

		if c == '$' {
			l.pos++
			if l.pos >= len(l.input) {
				return nil, l.Error("unexpected end of file in $-escape")
			}

			next := l.input[l.pos]
			switch next {
			case '$':
				// Escaped dollar sign - add as text
				currentText.WriteByte('$')
				l.pos++

			case ' ':
				// Escaped space - add as text
				currentText.WriteByte(' ')
				l.pos++

			case ':':
				// Escaped colon - add as text
				currentText.WriteByte(':')
				l.pos++

			case '\n', '\r':
				// Line continuation - skip the newline and any following whitespace
				l.pos++
				// Skip \r\n on Windows
				if l.pos < len(l.input) && l.input[l.pos-1] == '\r' && l.input[l.pos] == '\n' {
					l.pos++
				}
				// Skip leading whitespace on the next line
				for l.pos < len(l.input) && (l.input[l.pos] == ' ' || l.input[l.pos] == '\t') {
					l.pos++
				}

			case '{':
				// Variable reference ${name} - can include dots
				// First add any pending text
				if currentText.Len() > 0 {
					es.AddText(currentText.String())
					currentText.Reset()
				}

				l.pos++
				varStart := l.pos
				foundClose := false
				for l.pos < len(l.input) {
					if l.input[l.pos] == '}' {
						varName := string(l.input[varStart:l.pos])
						es.AddSpecial(varName)
						l.pos++
						foundClose = true
						break
					}
					// For ${}, we allow dots in variable names
					if !isIdentChar(l.input[l.pos]) {
						return nil, l.Error(fmt.Sprintf("invalid character in variable name: '%c'", l.input[l.pos]))
					}
					l.pos++
				}

				if !foundClose {
					return nil, l.Error("unclosed ${")
				}

			default:
				if isIdentStart(next) {
					// Variable reference $name - no dots allowed
					// First add any pending text
					if currentText.Len() > 0 {
						es.AddText(currentText.String())
						currentText.Reset()
					}

					varStart := l.pos
					for l.pos < len(l.input) && isSimpleVarChar(l.input[l.pos]) {
						l.pos++
					}
					varName := string(l.input[varStart:l.pos])
					es.AddSpecial(varName)
				} else {
					return nil, l.Error(fmt.Sprintf("invalid $-escape '%c'", next))
				}
			}
		} else {
			currentText.WriteByte(c)
			l.pos++
		}
	}

	// Add any remaining text
	if currentText.Len() > 0 {
		str := currentText.String()
		// Trim trailing whitespace for non-path values
		if !isPath {
			str = strings.TrimRight(str, " \t")
		}
		es.AddText(str)
	}

	return es, nil
}

// eatWhitespace skips whitespace except newlines
func (l *Lexer) eatWhitespace() {
	for l.pos < len(l.input) {
		c := l.input[l.pos]
		if c != ' ' && c != '\t' && c != '\r' {
			break
		}
		l.pos++
	}

	// Skip comments
	if l.pos < len(l.input) && l.input[l.pos] == '#' {
		for l.pos < len(l.input) && l.input[l.pos] != '\n' {
			l.pos++
		}
	}
}

// isStartOfLine checks if we're at the start of a line
func (l *Lexer) isStartOfLine() bool {
	if l.pos == 0 {
		return true
	}

	// Look back for the last newline
	for i := l.pos - 1; i >= 0; i-- {
		if l.input[i] == '\n' {
			// Check if there's only whitespace between newline and current position
			for j := i + 1; j < l.pos; j++ {
				if l.input[j] != ' ' && l.input[j] != '\t' {
					return false
				}
			}
			return true
		} else if l.input[i] != ' ' && l.input[i] != '\t' {
			return false
		}
	}

	// Beginning of file
	return true
}

// isIdentStart checks if a character can start an identifier
func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		c == '_'
}

// isIdentChar checks if a character can be part of an identifier
func isIdentChar(c byte) bool {
	return isIdentStart(c) ||
		(c >= '0' && c <= '9') ||
		c == '-' || c == '.'
}

// isSimpleVarChar checks if a character can be part of a simple variable name (no dots)
func isSimpleVarChar(c byte) bool {
	return isIdentStart(c) ||
		(c >= '0' && c <= '9') ||
		c == '-'
}

// isSpace checks if a character is whitespace
func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r'
}

// Scanner provides a higher-level interface for lexing
type Scanner struct {
	Lexer *Lexer // Exposed for parser access
}

// NewScanner creates a new Scanner
func NewScanner() *Scanner {
	return &Scanner{
		Lexer: New(),
	}
}

// Init initializes the scanner with input
func (s *Scanner) Init(filename, input string) {
	s.Lexer.Start(filename, input)
}

// NextToken reads the next token
func (s *Scanner) NextToken() Token {
	return s.Lexer.ReadToken()
}

// ExpectToken reads a token and checks it matches expected
func (s *Scanner) ExpectToken(expected Token) error {
	got := s.Lexer.ReadToken()
	if got != expected {
		return s.Lexer.Error(fmt.Sprintf("expected %s, got %s%s",
			TokenName(expected), TokenName(got), TokenErrorHint(expected)))
	}
	return nil
}

// ExpectIdent reads an identifier
func (s *Scanner) ExpectIdent() (string, error) {
	ident, ok := s.Lexer.ReadIdent()
	if !ok {
		return "", s.Lexer.Error("expected identifier")
	}
	return ident, nil
}

// ReadPath reads a path
func (s *Scanner) ReadPath() (*graph.EvalString, error) {
	return s.Lexer.ReadPath()
}

// ReadVarValue reads a variable value
func (s *Scanner) ReadVarValue() (*graph.EvalString, error) {
	return s.Lexer.ReadVarValue()
}

// PeekToken checks if the next token matches
func (s *Scanner) PeekToken(token Token) bool {
	return s.Lexer.PeekToken(token)
}

// Error returns an error with context
func (s *Scanner) Error(message string) error {
	return s.Lexer.Error(message)
}

// IsEOF checks if we've reached end of file
func (s *Scanner) IsEOF() bool {
	return s.Lexer.pos >= len(s.Lexer.input)
}
