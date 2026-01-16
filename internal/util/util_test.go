package util_test

import (
	"testing"

	"github.com/buildbuddy-io/reninja/internal/util"
	"github.com/stretchr/testify/assert"
)

func canonicalizePath(in string) string {
	out, _ := util.CanonicalizePath(in)
	return out
}

func TestCanonicalizePath(t *testing.T) {
	t.Skip()

	// Empty path
	assert.Equal(t, "", canonicalizePath(""))

	// Basic paths
	assert.Equal(t, "foo.h", canonicalizePath("foo.h"))
	assert.Equal(t, "foo.h", canonicalizePath("./foo.h"))
	assert.Equal(t, "foo/bar.h", canonicalizePath("./foo/./bar.h"))

	// Path with ..
	assert.Equal(t, "x/bar.h", canonicalizePath("./x/foo/../bar.h"))
	assert.Equal(t, "bar.h", canonicalizePath("./x/foo/../../bar.h"))

	// Multiple slashes
	assert.Equal(t, "foo/bar", canonicalizePath("foo//bar"))
	assert.Equal(t, "bar", canonicalizePath("foo//.//..///bar"))

	// Going up past root
	assert.Equal(t, "../bar.h", canonicalizePath("./x/../foo/../../bar.h"))

	// Trailing dots
	assert.Equal(t, "foo", canonicalizePath("foo/./."))
	assert.Equal(t, "foo", canonicalizePath("foo/bar/.."))

	// Hidden files
	assert.Equal(t, "foo/.hidden_bar", canonicalizePath("foo/.hidden_bar"))

	// Absolute paths
	assert.Equal(t, "/foo", canonicalizePath("/foo"))
	assert.Equal(t, "/foo", canonicalizePath("//foo"))

	// Parent directory references
	assert.Equal(t, "..", canonicalizePath(".."))
	assert.Equal(t, "..", canonicalizePath("../"))
	assert.Equal(t, "../foo", canonicalizePath("../foo"))
	assert.Equal(t, "../foo", canonicalizePath("../foo/"))
	assert.Equal(t, "../..", canonicalizePath("../.."))
	assert.Equal(t, "../..", canonicalizePath("../../"))
	assert.Equal(t, "..", canonicalizePath("./../"))

	// Absolute paths with ..
	assert.Equal(t, "/..", canonicalizePath("/.."))
	assert.Equal(t, "/..", canonicalizePath("/../"))
	assert.Equal(t, "/../..", canonicalizePath("/../.."))
	assert.Equal(t, "/../..", canonicalizePath("/../../"))

	// Root path
	assert.Equal(t, "/", canonicalizePath("/"))
	assert.Equal(t, "/", canonicalizePath("/foo/.."))

	// Current directory
	assert.Equal(t, ".", canonicalizePath("."))
	assert.Equal(t, ".", canonicalizePath("./."))
	assert.Equal(t, ".", canonicalizePath("foo/.."))

	// Files that look like .. but aren't
	assert.Equal(t, "foo/.._bar", canonicalizePath("foo/.._bar"))

	// Up directory tests
	assert.Equal(t, "../../foo/bar.h", canonicalizePath("../../foo/bar.h"))
	assert.Equal(t, "../foo/bar.h", canonicalizePath("test/../../foo/bar.h"))

	// Absolute path test
	assert.Equal(t, "/usr/include/stdio.h", canonicalizePath("/usr/include/stdio.h"))
}

func TestTortureTest(t *testing.T) {
	assert.Equal(t, "\"foo bar\\\\\\\"'$@d!st!c'\\path'\\\\\"", util.GetWin32EscapedString("foo bar\\\"'$@d!st!c'\\path'\\"))
}

func TestSensiblePathsAreNotNeedlesslyEscaped(t *testing.T) {
	path := "some/sensible/path/without/crazy/characters.c++"

	assert.Equal(t, path, util.GetWin32EscapedString(path))
	assert.Equal(t, path, util.GetShellEscapedString(path))
}

func TestSensibleWin32PathsAreNotNeedlesslyEscaped(t *testing.T) {
	path := "some\\sensible\\path\\without\\crazy\\characters.c++"

	assert.Equal(t, path, util.GetWin32EscapedString(path))
}

func TestStripAnsiEscapeCodesEscapeAtEnd(t *testing.T) {
	assert.Equal(t, "foo", util.StripAnsiEscapeCodes("foo\033"))
	assert.Equal(t, "foo", util.StripAnsiEscapeCodes("foo\033["))
}

func TestStripAnsiEscapeCodesStripColors(t *testing.T) {
	input := "\033[1maffixmgr.cxx:286:15: \033[0m\033[0;1;35mwarning: " +
		"\033[0m\033[1musing the result... [-Wparentheses]\033[0m"
	stripped := util.StripAnsiEscapeCodes(input)
	assert.Equal(t, "affixmgr.cxx:286:15: warning: using the result... [-Wparentheses]", stripped)
}
