package include_scanner

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

func TestParseIncludes(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "test.c")
	content := `
#include "foo.h"
#include <stdio.h>
#include "bar/baz.h"
  #  include   "spaced.h"
#include <sys/types.h>
// #include "commented.h"
int x = 0; // not an include
#include"nospace.h"
`
	if err := os.WriteFile(src, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	s := New()
	inclusions, err := s.parseIncludes(src)
	if err != nil {
		t.Fatal(err)
	}

	expected := []Inclusion{
		{Path: "foo.h", Quoted: true},
		{Path: "stdio.h", Quoted: false},
		{Path: "bar/baz.h", Quoted: true},
		{Path: "spaced.h", Quoted: true},
		{Path: "sys/types.h", Quoted: false},
		{Path: "nospace.h", Quoted: true},
	}

	if len(inclusions) != len(expected) {
		t.Fatalf("expected %d inclusions, got %d: %+v", len(expected), len(inclusions), inclusions)
	}
	for i, inc := range inclusions {
		if inc.Path != expected[i].Path || inc.Quoted != expected[i].Quoted {
			t.Errorf("inclusion[%d] = %+v, want %+v", i, inc, expected[i])
		}
	}
}

func TestParseIncludesCache(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "test.c")
	if err := os.WriteFile(src, []byte(`#include "a.h"`), 0644); err != nil {
		t.Fatal(err)
	}

	s := New()
	inc1, err := s.parseIncludes(src)
	if err != nil {
		t.Fatal(err)
	}
	inc2, err := s.parseIncludes(src)
	if err != nil {
		t.Fatal(err)
	}

	if len(inc1) != 1 || len(inc2) != 1 {
		t.Fatalf("expected 1 inclusion each, got %d and %d", len(inc1), len(inc2))
	}
	if inc1[0] != inc2[0] {
		t.Error("cached result differs from first parse")
	}
}

func TestResolveIncludeQuotedRelative(t *testing.T) {
	dir := t.TempDir()
	hdr := filepath.Join(dir, "foo.h")
	if err := os.WriteFile(hdr, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	inc := Inclusion{Path: "foo.h", Quoted: true}
	resolved := resolveInclude(inc, dir, nil)
	if resolved == "" {
		t.Fatal("expected to resolve foo.h relative to including dir")
	}
	absHdr, _ := filepath.Abs(hdr)
	if resolved != absHdr {
		t.Errorf("resolved = %q, want %q", resolved, absHdr)
	}
}

func TestResolveIncludeQuotedSearchPath(t *testing.T) {
	dir := t.TempDir()
	incDir := filepath.Join(dir, "include")
	os.MkdirAll(incDir, 0755)
	hdr := filepath.Join(incDir, "bar.h")
	if err := os.WriteFile(hdr, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Not found relative to "otherdir", should be found via search path.
	otherDir := filepath.Join(dir, "otherdir")
	os.MkdirAll(otherDir, 0755)

	inc := Inclusion{Path: "bar.h", Quoted: true}
	resolved := resolveInclude(inc, otherDir, []string{incDir})
	absHdr, _ := filepath.Abs(hdr)
	if resolved != absHdr {
		t.Errorf("resolved = %q, want %q", resolved, absHdr)
	}
}

func TestResolveIncludeAngleBracket(t *testing.T) {
	dir := t.TempDir()
	incDir := filepath.Join(dir, "include")
	os.MkdirAll(incDir, 0755)
	hdr := filepath.Join(incDir, "lib.h")
	if err := os.WriteFile(hdr, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Angle bracket should NOT check relative to including file dir.
	inc := Inclusion{Path: "lib.h", Quoted: false}
	resolved := resolveInclude(inc, dir, []string{incDir})
	absHdr, _ := filepath.Abs(hdr)
	if resolved != absHdr {
		t.Errorf("resolved = %q, want %q", resolved, absHdr)
	}

	// Without search path, angle bracket should not resolve.
	resolved = resolveInclude(inc, dir, nil)
	if resolved != "" {
		t.Errorf("expected empty, got %q", resolved)
	}
}

func TestResolveIncludeNotFound(t *testing.T) {
	inc := Inclusion{Path: "nonexistent.h", Quoted: true}
	resolved := resolveInclude(inc, "/tmp", nil)
	if resolved != "" {
		t.Errorf("expected empty, got %q", resolved)
	}
}

func TestExtractSearchPaths(t *testing.T) {
	tests := []struct {
		command  string
		expected []string
	}{
		{
			command:  "gcc -I/usr/include -Ilocal/include -o test test.c",
			expected: []string{"/usr/include", "local/include"},
		},
		{
			command:  "g++ -isystem /usr/lib/include -iquote mydir -I inc test.cc",
			expected: []string{"/usr/lib/include", "mydir", "inc"},
		},
		{
			command:  "gcc test.c",
			expected: nil,
		},
	}

	for _, tt := range tests {
		result := extractSearchPaths(tt.command)
		if !slices.Equal(result, tt.expected) {
			t.Errorf("extractSearchPaths(%q) = %v, want %v", tt.command, result, tt.expected)
		}
	}
}

func TestScanEdgeTransitive(t *testing.T) {
	// Create a temp directory tree:
	//   src/main.c       -> #include "a.h"
	//   src/a.h          -> #include "sub/b.h"
	//   src/sub/b.h      -> #include "c.h"
	//   include/c.h      -> (no includes)
	dir := t.TempDir()

	srcDir := filepath.Join(dir, "src")
	subDir := filepath.Join(dir, "src", "sub")
	incDir := filepath.Join(dir, "include")
	os.MkdirAll(srcDir, 0755)
	os.MkdirAll(subDir, 0755)
	os.MkdirAll(incDir, 0755)

	mainC := filepath.Join(srcDir, "main.c")
	aH := filepath.Join(srcDir, "a.h")
	bH := filepath.Join(subDir, "b.h")
	cH := filepath.Join(incDir, "c.h")

	os.WriteFile(mainC, []byte(`#include "a.h"`), 0644)
	os.WriteFile(aH, []byte(`#include "sub/b.h"`), 0644)
	os.WriteFile(bH, []byte(`#include "c.h"`), 0644)
	os.WriteFile(cH, []byte("// no includes\n"), 0644)

	s := New()
	command := "gcc -I" + incDir + " -o main " + mainC
	extra, err := s.ScanEdge([]string{mainC}, command)
	if err != nil {
		t.Fatal(err)
	}

	// main.c is the only input. We should discover a.h, sub/b.h, and c.h.
	sort.Strings(extra)
	for _, f := range extra {
		abs, _ := filepath.Abs(f)
		if abs != aH && abs != bH && abs != cH {
			t.Errorf("unexpected extra file: %s", f)
		}
	}
	if len(extra) != 3 {
		t.Errorf("expected 3 extra files, got %d: %v", len(extra), extra)
	}
}

func TestScanEdgeNoExtras(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "test.c")
	os.WriteFile(src, []byte(`#include <stdio.h>`), 0644)

	s := New()
	extra, err := s.ScanEdge([]string{src}, "gcc "+src)
	if err != nil {
		t.Fatal(err)
	}
	if len(extra) != 0 {
		t.Errorf("expected no extra files, got %v", extra)
	}
}

func TestScanEdgeSkipsNonScannableFiles(t *testing.T) {
	dir := t.TempDir()
	obj := filepath.Join(dir, "test.o")
	os.WriteFile(obj, []byte("not a c file"), 0644)

	s := New()
	extra, err := s.ScanEdge([]string{obj}, "ld "+obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(extra) != 0 {
		t.Errorf("expected no extra files for .o, got %v", extra)
	}
}

func TestScanEdgeDeduplicate(t *testing.T) {
	// If a header is already in inputFiles, it should not appear in extra.
	dir := t.TempDir()
	src := filepath.Join(dir, "main.c")
	hdr := filepath.Join(dir, "already.h")
	os.WriteFile(src, []byte(`#include "already.h"`), 0644)
	os.WriteFile(hdr, []byte(""), 0644)

	s := New()
	extra, err := s.ScanEdge([]string{src, hdr}, "gcc "+src)
	if err != nil {
		t.Fatal(err)
	}
	if len(extra) != 0 {
		t.Errorf("expected no extra files (header already in inputs), got %v", extra)
	}
}

func TestResolveIncludeAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	hdr := filepath.Join(dir, "abs.h")
	if err := os.WriteFile(hdr, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Angle-bracket include with an absolute path (e.g. from a CMake unity build).
	inc := Inclusion{Path: hdr, Quoted: false}
	resolved := resolveInclude(inc, "/some/other/dir", []string{"/another/dir"})
	if resolved != hdr {
		t.Errorf("resolveInclude with absolute path = %q, want %q", resolved, hdr)
	}
}

func TestScanEdgeUnityBuildAbsoluteIncludes(t *testing.T) {
	// Simulate a CMake unity build file that #includes .cpp files via absolute paths.
	dir := t.TempDir()

	srcDir := filepath.Join(dir, "src")
	os.MkdirAll(srcDir, 0755)

	ub := filepath.Join(dir, "ub_unity.cpp")
	cppA := filepath.Join(srcDir, "a.cpp")
	cppB := filepath.Join(srcDir, "b.cpp")

	os.WriteFile(cppA, []byte("// a.cpp\n"), 0644)
	os.WriteFile(cppB, []byte("// b.cpp\n"), 0644)
	// Unity build file includes .cpp files via absolute paths with angle brackets.
	os.WriteFile(ub, []byte(
		"#include <"+cppA+">\n"+
			"#include <"+cppB+">\n",
	), 0644)

	s := New()
	extra, err := s.ScanEdge([]string{ub}, "g++ -o out "+ub)
	if err != nil {
		t.Fatal(err)
	}

	sort.Strings(extra)
	absA, _ := filepath.Abs(cppA)
	absB, _ := filepath.Abs(cppB)
	found := make(map[string]bool)
	for _, f := range extra {
		abs, _ := filepath.Abs(f)
		found[abs] = true
	}
	if !found[absA] {
		t.Errorf("expected to discover %s, got extra=%v", cppA, extra)
	}
	if !found[absB] {
		t.Errorf("expected to discover %s, got extra=%v", cppB, extra)
	}
}

func TestIsScannable(t *testing.T) {
	scannable := []string{"foo.c", "bar.cc", "baz.cpp", "x.cxx", "a.h", "b.hh", "c.hpp", "d.hxx", "e.inc"}
	for _, f := range scannable {
		if !isScannable(f) {
			t.Errorf("expected %q to be scannable", f)
		}
	}
	notScannable := []string{"foo.o", "bar.a", "baz.so", "x.py", "y.go", "z.txt", "noext"}
	for _, f := range notScannable {
		if isScannable(f) {
			t.Errorf("expected %q to not be scannable", f)
		}
	}
}

func TestExtractIntermediateDirsFromCommand(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		expected []string
	}{
		{
			name:     "no dotdot paths",
			command:  "gcc -I/usr/include -o test test.c",
			expected: nil,
		},
		{
			name:     "relative path with dotdot ignored",
			command:  "gcc -I../include test.c",
			expected: nil,
		},
		{
			name:    "absolute path with dotdot",
			command: "gcc -I/home/user/project/build/../include test.c",
			expected: []string{
				"/home/user/project/build",
			},
		},
		{
			name:    "flag prefix stripped",
			command: "gcc -I/a/b/../c -L/d/e/../f test.c",
			expected: []string{
				"/a/b",
				"/d/e",
			},
		},
		{
			name:    "multiple dotdots in one path",
			command: "gcc /a/b/c/../../d test.c",
			expected: []string{
				"/a/b/c",
				"/a/b",
			},
		},
		{
			name:    "deduplicates directories",
			command: "gcc -I/a/b/../c -I/a/b/../d",
			expected: []string{
				"/a/b",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractIntermediateDirsFromCommand(tt.command)
			if !slices.Equal(result, tt.expected) {
				t.Errorf("ExtractIntermediateDirsFromCommand(%q) = %v, want %v", tt.command, result, tt.expected)
			}
		})
	}
}

func TestExtractCommandReferencedPaths(t *testing.T) {
	root := t.TempDir()

	// Create directory structure:
	//   root/scripts/build.cmake
	//   root/scripts/helper.cmake
	//   root/data/config.txt
	//   root/subdir/  (empty directory)
	scriptsDir := filepath.Join(root, "scripts")
	dataDir := filepath.Join(root, "data")
	subDir := filepath.Join(root, "subdir")
	os.MkdirAll(scriptsDir, 0755)
	os.MkdirAll(dataDir, 0755)
	os.MkdirAll(subDir, 0755)

	buildCmake := filepath.Join(scriptsDir, "build.cmake")
	helperCmake := filepath.Join(scriptsDir, "helper.cmake")
	configTxt := filepath.Join(dataDir, "config.txt")

	os.WriteFile(buildCmake, []byte("cmake script"), 0644)
	os.WriteFile(helperCmake, []byte("helper script"), 0644)
	os.WriteFile(configTxt, []byte("config"), 0644)

	t.Run("no matching paths", func(t *testing.T) {
		result := ExtractCommandReferencedPaths("gcc -o test test.c", root)
		if len(result) != 0 {
			t.Errorf("expected no paths, got %v", result)
		}
	})

	t.Run("file path includes siblings", func(t *testing.T) {
		command := "cmake -P " + buildCmake
		result := ExtractCommandReferencedPaths(command, root)
		// Should include build.cmake and helper.cmake (sibling)
		resultSet := make(map[string]bool)
		for _, p := range result {
			resultSet[p] = true
		}
		if !resultSet[buildCmake] {
			t.Errorf("expected %s in result, got %v", buildCmake, result)
		}
		if !resultSet[helperCmake] {
			t.Errorf("expected sibling %s in result, got %v", helperCmake, result)
		}
	})

	t.Run("directory path no siblings", func(t *testing.T) {
		command := "ls " + subDir
		result := ExtractCommandReferencedPaths(command, root)
		if len(result) != 1 || result[0] != subDir {
			t.Errorf("expected [%s], got %v", subDir, result)
		}
	})

	t.Run("path embedded in flag", func(t *testing.T) {
		command := "gcc -DCONFIG_PATH=" + configTxt + " test.c"
		result := ExtractCommandReferencedPaths(command, root)
		resultSet := make(map[string]bool)
		for _, p := range result {
			resultSet[p] = true
		}
		if !resultSet[configTxt] {
			t.Errorf("expected %s in result, got %v", configTxt, result)
		}
	})

	t.Run("nonexistent path ignored", func(t *testing.T) {
		command := "gcc " + filepath.Join(root, "nonexistent", "file.c")
		result := ExtractCommandReferencedPaths(command, root)
		if len(result) != 0 {
			t.Errorf("expected no paths for nonexistent file, got %v", result)
		}
	})

	t.Run("deduplicates paths", func(t *testing.T) {
		command := "cmake -P " + buildCmake + " -P " + buildCmake
		result := ExtractCommandReferencedPaths(command, root)
		seen := make(map[string]int)
		for _, p := range result {
			seen[p]++
		}
		for p, count := range seen {
			if count > 1 {
				t.Errorf("path %s appeared %d times", p, count)
			}
		}
	})

	t.Run("quoted path strips trailing quote", func(t *testing.T) {
		// Simulates -Wl,--version-script,"/root/path/LTO.exports"
		// where strings.Fields keeps the quotes as part of the token.
		command := `-Wl,--version-script,"` + configTxt + `"`
		result := ExtractCommandReferencedPaths(command, root)
		resultSet := make(map[string]bool)
		for _, p := range result {
			resultSet[p] = true
		}
		if !resultSet[configTxt] {
			t.Errorf("expected %s in result (trailing quote should be stripped), got %v", configTxt, result)
		}
	})
}

func TestExtractSearchDirectoryContents(t *testing.T) {
	root := t.TempDir()

	// Create directory structure:
	//   root/inc/a.h
	//   root/inc/b.h
	//   root/inc/sub/c.h
	//   root/lib/libfoo.a
	//   root/outside_file.txt  (not in any -I/-L dir)
	incDir := filepath.Join(root, "inc")
	incSubDir := filepath.Join(root, "inc", "sub")
	libDir := filepath.Join(root, "lib")
	os.MkdirAll(incSubDir, 0755)
	os.MkdirAll(libDir, 0755)

	aH := filepath.Join(incDir, "a.h")
	bH := filepath.Join(incDir, "b.h")
	cH := filepath.Join(incSubDir, "c.h")
	libFoo := filepath.Join(libDir, "libfoo.a")
	outside := filepath.Join(root, "outside_file.txt")

	os.WriteFile(aH, []byte(""), 0644)
	os.WriteFile(bH, []byte(""), 0644)
	os.WriteFile(cH, []byte(""), 0644)
	os.WriteFile(libFoo, []byte(""), 0644)
	os.WriteFile(outside, []byte(""), 0644)

	t.Run("collects files from -I directory", func(t *testing.T) {
		command := "gcc -I" + incDir + " -o test test.c"
		result := ExtractSearchDirectoryContents(command, root)
		resultSet := make(map[string]bool)
		for _, p := range result {
			resultSet[p] = true
		}
		if !resultSet[aH] || !resultSet[bH] {
			t.Errorf("expected a.h and b.h, got %v", result)
		}
		if !resultSet[cH] {
			t.Errorf("expected recursive sub/c.h, got %v", result)
		}
		if resultSet[outside] {
			t.Errorf("should not include files outside search dirs, got %v", result)
		}
	})

	t.Run("collects files from -L directory", func(t *testing.T) {
		command := "ld -L" + libDir + " -lfoo"
		result := ExtractSearchDirectoryContents(command, root)
		resultSet := make(map[string]bool)
		for _, p := range result {
			resultSet[p] = true
		}
		if !resultSet[libFoo] {
			t.Errorf("expected libfoo.a from -L dir, got %v", result)
		}
	})

	t.Run("space-separated -I flag", func(t *testing.T) {
		command := "gcc -I " + incDir + " test.c"
		result := ExtractSearchDirectoryContents(command, root)
		found := false
		for _, p := range result {
			if p == aH {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected a.h from space-separated -I, got %v", result)
		}
	})

	t.Run("ignores directories outside root", func(t *testing.T) {
		command := "gcc -I/usr/include -I" + incDir + " test.c"
		result := ExtractSearchDirectoryContents(command, root)
		for _, p := range result {
			if !filepath.HasPrefix(p, root) {
				t.Errorf("got path outside root: %s", p)
			}
		}
	})

	t.Run("deduplicates directories", func(t *testing.T) {
		command := "gcc -I" + incDir + " -I" + incDir + " test.c"
		result := ExtractSearchDirectoryContents(command, root)
		seen := make(map[string]int)
		for _, p := range result {
			seen[p]++
		}
		for p, count := range seen {
			if count > 1 {
				t.Errorf("path %s appeared %d times", p, count)
			}
		}
	})

	t.Run("no search directories", func(t *testing.T) {
		command := "gcc -o test test.c"
		result := ExtractSearchDirectoryContents(command, root)
		if len(result) != 0 {
			t.Errorf("expected no files, got %v", result)
		}
	})
}
