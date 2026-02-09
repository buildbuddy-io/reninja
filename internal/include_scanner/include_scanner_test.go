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
