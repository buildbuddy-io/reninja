package util

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/buildbuddy-io/gin/internal/edit_distance"
)

// StringList implements a flag.Value that accepts an sequence of values as a CSV.
type StringList []string

// Set implements part of the flag.Getter interface and will append new values to the flag.
func (f *StringList) Set(s string) error {
	*f = append(*f, strings.Split(s, ",")...)
	return nil
}

// String implements part of the flag.Getter interface and returns a string-ish value for the flag.
func (f *StringList) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(*f, ",")
}

// Get implements flag.Getter and returns a slice of string values.
func (f *StringList) Get() any {
	if f == nil {
		return []string(nil)
	}
	return *f
}

// CanonicalizePath normalizes a path to use forward slashes and returns slash bits
// TODO(tylerw): review this, it's probably wrong.
func CanonicalizePath(path string) (outp string, outs uint64) {
	if !strings.ContainsRune(path, '\\') {
		return filepath.Clean(path), 0
	}

	var slashBits uint64
	bit := uint64(1)
	result := strings.Builder{}
	result.Grow(len(path))

	for _, ch := range path {
		if ch == '\\' {
			result.WriteByte('/')
			slashBits |= bit
			bit <<= 1
		} else {
			result.WriteRune(ch)
			if ch == '/' {
				bit <<= 1
			}
		}
	}

	return filepath.Clean(result.String()), slashBits
}

func IsKnownShellSafeCharacter(ch rune) bool {
	if 'A' <= ch && ch <= 'Z' {
		return true
	}
	if 'a' <= ch && ch <= 'z' {
		return true
	}
	if '0' <= ch && ch <= '9' {
		return true
	}

	switch ch {
	case '_', '+', '-', '.', '/':
		return true
	default:
		return false
	}
}

func IsKnownWin32SafeCharacter(ch rune) bool {
	switch ch {
	case ' ', '"':
		return false
	default:
		return true
	}
}

func StringNeedsShellEscaping(input string) bool {
	for _, r := range input {
		if !IsKnownShellSafeCharacter(r) {
			return true
		}
	}
	return false
}

func StringNeedsWin32Escaping(input string) bool {
	for _, r := range input {
		if !IsKnownWin32SafeCharacter(r) {
			return true
		}
	}
	return false
}

func GetShellEscapedString(input string) string {
	if !StringNeedsShellEscaping(input) {
		return input
	}

	quote := '\''
	escapeSequence := "'\\'"

	var result strings.Builder
	result.WriteRune(quote)

	spanBegin := 0
	for i, ch := range input {
		if ch == quote {
			result.WriteString(input[spanBegin:i])
			result.WriteString(escapeSequence)
			spanBegin = i
		}
	}
	result.WriteString(input[spanBegin:])
	result.WriteRune(quote)

	return result.String()
}

func GetWin32EscapedString(input string) string {
	if !StringNeedsWin32Escaping(input) {
		return input
	}

	quote := '"'
	backslash := '\\'

	var result strings.Builder
	result.WriteRune(quote)

	consecutiveBackslashCount := 0
	spanBegin := 0

	for i, ch := range input {
		switch ch {
		case backslash:
			consecutiveBackslashCount++
		case quote:
			result.WriteString(input[spanBegin:i])
			result.WriteString(strings.Repeat(string(backslash), consecutiveBackslashCount+1))
			spanBegin = i
			consecutiveBackslashCount = 0
		default:
			consecutiveBackslashCount = 0
		}
	}

	result.WriteString(input[spanBegin:])
	result.WriteString(strings.Repeat(string(backslash), consecutiveBackslashCount))
	result.WriteRune(quote)

	return result.String()
}

func isLatinAlpha(c byte) bool {
	// isalpha() is locale-dependent.
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func StripAnsiEscapeCodes(in string) string {
	var stripped strings.Builder
	stripped.Grow(len(in))

	for i := 0; i < len(in); i++ {
		if in[i] != '\033' {
			// Not an escape code.
			stripped.WriteByte(in[i])
			continue
		}

		// Only strip CSIs for now.
		if i+1 >= len(in) {
			break
		}
		if in[i+1] != '[' {
			continue // Not a CSI.
		}
		i += 2

		// Skip everything up to and including the next [a-zA-Z].
		for i < len(in) && !isLatinAlpha(in[i]) {
			i++
		}
	}
	return stripped.String()
}

func SpellcheckString(text string, words ...string) string {
	allowReplacements := true
	maxValidEditDistance := 3

	minDistance := maxValidEditDistance + 1
	var result string
	for _, word := range words {
		w := word
		distance := edit_distance.EditDistance(w, text, allowReplacements, maxValidEditDistance)
		if distance < minDistance {
			minDistance = distance
			result = w
		}
	}
	return result
}

func Warning(msg string) {
	fmt.Fprintf(os.Stderr, "ninja: warning: %s\n", msg)
}

func Warningf(format string, args ...interface{}) {
	Warning(fmt.Sprintf(format, args...))
}

func Error(msg string) {
	fmt.Fprintf(os.Stderr, "ninja: err: %s\n", msg)
}

func Errorf(format string, args ...interface{}) {
	Error(fmt.Sprintf(format, args...))
}

func Info(msg string) {
	fmt.Fprintf(os.Stdout, "ninja: %s\n", msg)
}

func Infof(format string, args ...interface{}) {
	Info(fmt.Sprintf(format, args...))
}
