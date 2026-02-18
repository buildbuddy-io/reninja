package include_scanner_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/buildbuddy-io/reninja/internal/include_scanner"
)

func TestScanEdgeIncludePatterns(t *testing.T) {
	// Exercises various #include syntax patterns through the public API.
	dir := t.TempDir()

	incDir := filepath.Join(dir, "inc")
	os.MkdirAll(filepath.Join(incDir, "sys"), 0755)
	os.MkdirAll(filepath.Join(dir, "bar"), 0755)

	// Headers reachable via quoted include (relative to source dir).
	os.WriteFile(filepath.Join(dir, "foo.h"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "bar", "baz.h"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "spaced.h"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "nospace.h"), []byte(""), 0644)

	// Header reachable via angle-bracket include (search path).
	os.WriteFile(filepath.Join(incDir, "sys", "types.h"), []byte(""), 0644)

	src := filepath.Join(dir, "test.c")
	content := `
#include "foo.h"
#include <sys/types.h>
#include "bar/baz.h"
  #  include   "spaced.h"
// #include "commented.h"
int x = 0; // not an include
#include"nospace.h"
`
	os.WriteFile(src, []byte(content), 0644)

	s := include_scanner.New()
	extra, err := s.ScanEdge([]string{src}, "gcc -I"+incDir+" "+src)
	if err != nil {
		t.Fatal(err)
	}

	found := make(map[string]bool)
	for _, f := range extra {
		abs, _ := filepath.Abs(f)
		found[abs] = true
	}

	expected := []string{
		filepath.Join(dir, "foo.h"),
		filepath.Join(incDir, "sys", "types.h"),
		filepath.Join(dir, "bar", "baz.h"),
		filepath.Join(dir, "spaced.h"),
		filepath.Join(dir, "nospace.h"),
	}
	for _, h := range expected {
		abs, _ := filepath.Abs(h)
		if !found[abs] {
			t.Errorf("expected to find %s, got extra=%v", h, extra)
		}
	}
}

func TestScanEdgeAngleBracketResolution(t *testing.T) {
	// Angle-bracket includes should only check search paths,
	// NOT relative to the including file's directory.
	dir := t.TempDir()
	incDir := filepath.Join(dir, "include")
	os.MkdirAll(incDir, 0755)

	// lib.h exists ONLY next to the source file (not in search path).
	os.WriteFile(filepath.Join(dir, "lib.h"), []byte(""), 0644)

	src := filepath.Join(dir, "test.c")
	os.WriteFile(src, []byte("#include <lib.h>\n"), 0644)

	s := include_scanner.New()

	// Without search path containing lib.h, angle bracket should not resolve.
	extra, err := s.ScanEdge([]string{src}, "gcc "+src)
	if err != nil {
		t.Fatal(err)
	}
	absLib, _ := filepath.Abs(filepath.Join(dir, "lib.h"))
	for _, f := range extra {
		abs, _ := filepath.Abs(f)
		if abs == absLib {
			t.Errorf("angle bracket include should NOT resolve relative to source dir, but found %s", f)
		}
	}

	// With search path, it should resolve.
	os.WriteFile(filepath.Join(incDir, "lib.h"), []byte(""), 0644)
	s2 := include_scanner.New()
	extra, err = s2.ScanEdge([]string{src}, "gcc -I"+incDir+" "+src)
	if err != nil {
		t.Fatal(err)
	}
	absInc, _ := filepath.Abs(filepath.Join(incDir, "lib.h"))
	found := false
	for _, f := range extra {
		abs, _ := filepath.Abs(f)
		if abs == absInc {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find lib.h via search path, got extra=%v", extra)
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

	s := include_scanner.New()
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

func TestScanEdgeGeneratedIncFile(t *testing.T) {
	// Reproduce the LLVM scenario: source file includes a chain of headers
	// from the source include dir, and the last header includes a generated
	// .inc file from the build include dir.
	//
	// Directory structure (mimics LLVM out-of-tree build):
	//   project/llvm/examples/IRTransforms/SimplifyCFG.cpp
	//   project/llvm/include/llvm/Passes/PassBuilder.h
	//   project/llvm/include/llvm/Analysis/CGSCCPassManager.h
	//   project/llvm/include/llvm/Analysis/LazyCallGraph.h
	//   project/llvm/include/llvm/Analysis/TargetLibraryInfo.h
	//   project/build-rbe/include/llvm/Analysis/TargetLibraryInfo.inc  (generated)
	//
	// CWD is set to project/build-rbe (the build directory).
	// Source file is passed as absolute path.
	// -I flags use absolute paths.
	dir := t.TempDir()

	// Source tree
	srcExampleDir := filepath.Join(dir, "llvm", "examples", "IRTransforms")
	srcIncDir := filepath.Join(dir, "llvm", "include")
	os.MkdirAll(srcExampleDir, 0755)
	os.MkdirAll(filepath.Join(srcIncDir, "llvm", "Passes"), 0755)
	os.MkdirAll(filepath.Join(srcIncDir, "llvm", "Analysis"), 0755)

	// Build tree
	buildDir := filepath.Join(dir, "build-rbe")
	buildIncDir := filepath.Join(buildDir, "include")
	os.MkdirAll(filepath.Join(buildIncDir, "llvm", "Analysis"), 0755)

	simplify := filepath.Join(srcExampleDir, "SimplifyCFG.cpp")
	passBuilder := filepath.Join(srcIncDir, "llvm", "Passes", "PassBuilder.h")
	cgscc := filepath.Join(srcIncDir, "llvm", "Analysis", "CGSCCPassManager.h")
	lazyCallGraph := filepath.Join(srcIncDir, "llvm", "Analysis", "LazyCallGraph.h")
	targetLibInfo := filepath.Join(srcIncDir, "llvm", "Analysis", "TargetLibraryInfo.h")
	targetLibInc := filepath.Join(buildIncDir, "llvm", "Analysis", "TargetLibraryInfo.inc")

	os.WriteFile(simplify, []byte(`#include "llvm/Passes/PassBuilder.h"`), 0644)
	os.WriteFile(passBuilder, []byte(`#include "llvm/Analysis/CGSCCPassManager.h"`), 0644)
	os.WriteFile(cgscc, []byte(`#include "llvm/Analysis/LazyCallGraph.h"`), 0644)
	os.WriteFile(lazyCallGraph, []byte(`#include "llvm/Analysis/TargetLibraryInfo.h"`), 0644)
	os.WriteFile(targetLibInfo, []byte(`#include "llvm/Analysis/TargetLibraryInfo.inc"`), 0644)
	os.WriteFile(targetLibInc, []byte("// generated tablegen output\n"), 0644)

	// Change to build directory (like a real LLVM build)
	origDir, _ := os.Getwd()
	os.Chdir(buildDir)
	defer os.Chdir(origDir)

	s := include_scanner.New()
	// Command uses absolute paths (like CMake out-of-tree builds)
	command := fmt.Sprintf("/usr/bin/c++ -I%s -I%s -c %s",
		filepath.Join(buildDir, "include"), srcIncDir, simplify)
	extra, err := s.ScanEdge([]string{simplify}, command)
	if err != nil {
		t.Fatal(err)
	}

	found := make(map[string]bool)
	for _, f := range extra {
		abs, _ := filepath.Abs(f)
		found[abs] = true
	}

	for _, want := range []struct {
		path string
		desc string
	}{
		{passBuilder, "PassBuilder.h"},
		{cgscc, "CGSCCPassManager.h"},
		{lazyCallGraph, "LazyCallGraph.h"},
		{targetLibInfo, "TargetLibraryInfo.h"},
		{targetLibInc, "TargetLibraryInfo.inc (generated)"},
	} {
		abs, _ := filepath.Abs(want.path)
		if !found[abs] {
			t.Errorf("missing %s (%s), got extra=%v", want.desc, want.path, extra)
		}
	}
}

func TestScanEdgeNoExtras(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "test.c")
	os.WriteFile(src, []byte(`#include <stdio.h>`), 0644)

	s := include_scanner.New()
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

	s := include_scanner.New()
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

	s := include_scanner.New()
	extra, err := s.ScanEdge([]string{src, hdr}, "gcc "+src)
	if err != nil {
		t.Fatal(err)
	}
	if len(extra) != 0 {
		t.Errorf("expected no extra files (header already in inputs), got %v", extra)
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

	s := include_scanner.New()
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

func TestScanEdgeBackslashContinuation(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "foo.h"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "bar.h"), []byte(""), 0644)

	src := filepath.Join(dir, "test.c")
	content := "#include \\\n\"foo.h\"\n#include \\\n  \"bar.h\"\nint x;\n"
	os.WriteFile(src, []byte(content), 0644)

	s := include_scanner.New()
	extra, err := s.ScanEdge([]string{src}, "gcc "+src)
	if err != nil {
		t.Fatal(err)
	}

	found := make(map[string]bool)
	for _, f := range extra {
		abs, _ := filepath.Abs(f)
		found[abs] = true
	}
	for _, name := range []string{"foo.h", "bar.h"} {
		abs, _ := filepath.Abs(filepath.Join(dir, name))
		if !found[abs] {
			t.Errorf("expected to find %s (backslash continuation), got extra=%v", name, extra)
		}
	}
}

func TestScanEdgeComputedInclude(t *testing.T) {
	// Computed includes like #include MACRO should be silently skipped.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "real.h"), []byte(""), 0644)

	src := filepath.Join(dir, "test.c")
	content := `
#include SOME_MACRO
#include MACRO(arg)
#include "real.h"
#include CONCAT(A, B)
`
	os.WriteFile(src, []byte(content), 0644)

	s := include_scanner.New()
	extra, err := s.ScanEdge([]string{src}, "gcc "+src)
	if err != nil {
		t.Fatal(err)
	}

	// Only real.h should be found; computed includes should be skipped.
	if len(extra) != 1 {
		t.Fatalf("expected 1 extra file (real.h only), got %d: %v", len(extra), extra)
	}
	abs, _ := filepath.Abs(extra[0])
	absReal, _ := filepath.Abs(filepath.Join(dir, "real.h"))
	if abs != absReal {
		t.Errorf("expected real.h, got %s", extra[0])
	}
}

func TestScanEdgeImportDirective(t *testing.T) {
	// Objective-C #import should be treated like #include.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ObjCHeader.h"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "regular.h"), []byte(""), 0644)

	// Use .c extension since .m is not in scannableExtensions.
	src := filepath.Join(dir, "test.c")
	content := `
#import "ObjCHeader.h"
#include "regular.h"
`
	os.WriteFile(src, []byte(content), 0644)

	s := include_scanner.New()
	extra, err := s.ScanEdge([]string{src}, "gcc "+src)
	if err != nil {
		t.Fatal(err)
	}

	found := make(map[string]bool)
	for _, f := range extra {
		abs, _ := filepath.Abs(f)
		found[abs] = true
	}
	absObj, _ := filepath.Abs(filepath.Join(dir, "ObjCHeader.h"))
	absReg, _ := filepath.Abs(filepath.Join(dir, "regular.h"))
	if !found[absObj] {
		t.Errorf("expected ObjCHeader.h (via #import), got extra=%v", extra)
	}
	if !found[absReg] {
		t.Errorf("expected regular.h, got extra=%v", extra)
	}
}

func TestScanEdgeIncludeNext(t *testing.T) {
	// GNU extension #include_next is used by glibc and libstdc++.
	// Our scanner treats it like a regular #include for discovery purposes.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "stdlib.h"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "regular.h"), []byte(""), 0644)

	src := filepath.Join(dir, "test.h")
	content := `
#include_next "stdlib.h"
#include "regular.h"
`
	os.WriteFile(src, []byte(content), 0644)

	s := include_scanner.New()
	extra, err := s.ScanEdge([]string{src}, "gcc "+src)
	if err != nil {
		t.Fatal(err)
	}

	found := make(map[string]bool)
	for _, f := range extra {
		abs, _ := filepath.Abs(f)
		found[abs] = true
	}
	absStdlib, _ := filepath.Abs(filepath.Join(dir, "stdlib.h"))
	absRegular, _ := filepath.Abs(filepath.Join(dir, "regular.h"))
	if !found[absStdlib] {
		t.Errorf("expected stdlib.h via #include_next, got extra=%v", extra)
	}
	if !found[absRegular] {
		t.Errorf("expected regular.h, got extra=%v", extra)
	}
}

func TestScanEdgeMalformedDirectives(t *testing.T) {
	// Malformed directives should not crash or produce garbage.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "valid.h"), []byte(""), 0644)

	src := filepath.Join(dir, "test.c")
	content := `
#include
#include <>
#include ""
#include <
#include "
# include
#include "valid.h"
`
	os.WriteFile(src, []byte(content), 0644)

	s := include_scanner.New()
	extra, err := s.ScanEdge([]string{src}, "gcc "+src)
	if err != nil {
		t.Fatal(err)
	}

	absValid, _ := filepath.Abs(filepath.Join(dir, "valid.h"))
	validFound := false
	for _, f := range extra {
		abs, _ := filepath.Abs(f)
		if abs == absValid {
			validFound = true
		}
	}
	if !validFound {
		t.Errorf("expected to find valid.h, got extra=%v", extra)
	}
}

func TestScanEdgeTrailingComment(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "foo.h"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "bar.h"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "baz.h"), []byte(""), 0644)

	src := filepath.Join(dir, "test.c")
	content := `
#include "foo.h" // this is a comment
#include "bar.h" /* another comment */
#include "baz.h"   // trailing
`
	os.WriteFile(src, []byte(content), 0644)

	s := include_scanner.New()
	extra, err := s.ScanEdge([]string{src}, "gcc "+src)
	if err != nil {
		t.Fatal(err)
	}

	found := make(map[string]bool)
	for _, f := range extra {
		abs, _ := filepath.Abs(f)
		found[abs] = true
	}
	for _, name := range []string{"foo.h", "bar.h", "baz.h"} {
		abs, _ := filepath.Abs(filepath.Join(dir, name))
		if !found[abs] {
			t.Errorf("expected to find %s (trailing comment), got extra=%v", name, extra)
		}
	}
}

func TestScanEdgeDirectoryNamedAsHeader(t *testing.T) {
	// If an include path resolves to a directory rather than a file,
	// it should not be treated as a valid include.
	dir := t.TempDir()

	// Create a directory named "foo.h" — this is NOT a file.
	os.MkdirAll(filepath.Join(dir, "foo.h"), 0755)
	os.WriteFile(filepath.Join(dir, "real.h"), []byte(""), 0644)

	src := filepath.Join(dir, "test.c")
	os.WriteFile(src, []byte("#include \"foo.h\"\n#include \"real.h\"\n"), 0644)

	s := include_scanner.New()
	extra, err := s.ScanEdge([]string{src}, "gcc "+src)
	if err != nil {
		t.Fatal(err)
	}

	found := make(map[string]bool)
	for _, f := range extra {
		abs, _ := filepath.Abs(f)
		found[abs] = true
	}
	absReal, _ := filepath.Abs(filepath.Join(dir, "real.h"))
	if !found[absReal] {
		t.Errorf("expected to find real.h, got extra=%v", extra)
	}
	absFoo, _ := filepath.Abs(filepath.Join(dir, "foo.h"))
	if found[absFoo] {
		t.Errorf("directory-named-as-header should not be resolved, got extra=%v", extra)
	}
}

func TestScanEdgeSymlinkCycle(t *testing.T) {
	// Create a symlink cycle and verify the scanner terminates.
	// Inspired by distcc's symlink farm tests.
	//
	// Structure:
	//   dir/a/link -> ../b
	//   dir/b/link -> ../a
	//   dir/a/foo.h (includes "link/foo.h")
	//   dir/b/foo.h (includes "link/foo.h")
	//   dir/main.c  (includes "a/foo.h")
	dir := t.TempDir()

	aDir := filepath.Join(dir, "a")
	bDir := filepath.Join(dir, "b")
	os.MkdirAll(aDir, 0755)
	os.MkdirAll(bDir, 0755)

	// Create symlinks: a/link -> ../b, b/link -> ../a
	os.Symlink("../b", filepath.Join(aDir, "link"))
	os.Symlink("../a", filepath.Join(bDir, "link"))

	// Create headers that reference each other through symlinks.
	os.WriteFile(filepath.Join(aDir, "foo.h"), []byte(`#include "link/foo.h"`+"\n"), 0644)
	os.WriteFile(filepath.Join(bDir, "foo.h"), []byte(`#include "link/foo.h"`+"\n"), 0644)

	// Main file that starts the chain.
	mainC := filepath.Join(dir, "main.c")
	os.WriteFile(mainC, []byte(`#include "a/foo.h"`+"\n"), 0644)

	s := include_scanner.New()
	// This should terminate (not hang) even with symlink cycles.
	extra, err := s.ScanEdge([]string{mainC}, "gcc -I"+dir+" "+mainC)
	if err != nil {
		t.Fatal(err)
	}

	// We should find at least a/foo.h and b/foo.h.
	found := make(map[string]bool)
	for _, f := range extra {
		abs, _ := filepath.Abs(f)
		found[abs] = true
	}
	aFoo, _ := filepath.Abs(filepath.Join(aDir, "foo.h"))
	bFoo, _ := filepath.Abs(filepath.Join(bDir, "foo.h"))
	if !found[aFoo] {
		t.Errorf("expected to find a/foo.h, got extra=%v", extra)
	}
	if !found[bFoo] {
		t.Errorf("expected to find b/foo.h, got extra=%v", extra)
	}
}

func TestScanEdgeDotDotThroughSymlink(t *testing.T) {
	// Test that #include "symlink/../real.h" works correctly when
	// the symlink points to a different directory.
	// Inspired by distcc's test_DotdotInInclude.
	dir := t.TempDir()

	// Create:
	//   dir/real/target/  (symlink target directory)
	//   dir/src/link -> ../real/target
	//   dir/real/real.h
	//   dir/src/main.c -> #include "link/../real.h"
	realDir := filepath.Join(dir, "real", "target")
	srcDir := filepath.Join(dir, "src")
	os.MkdirAll(realDir, 0755)
	os.MkdirAll(srcDir, 0755)

	realH := filepath.Join(dir, "real", "real.h")
	os.WriteFile(realH, []byte("// real header\n"), 0644)

	// Symlink: src/link -> ../real/target
	os.Symlink("../real/target", filepath.Join(srcDir, "link"))

	mainC := filepath.Join(srcDir, "main.c")
	// This include goes through the symlink, then ".." goes up to where
	// the symlink target resides (real/), not where the symlink is (src/).
	os.WriteFile(mainC, []byte(`#include "link/../real.h"`+"\n"), 0644)

	s := include_scanner.New()
	extra, err := s.ScanEdge([]string{mainC}, "gcc "+mainC)
	if err != nil {
		t.Fatal(err)
	}

	// The scanner should find real.h through the symlink/../ path.
	found := false
	for _, f := range extra {
		abs, _ := filepath.Abs(f)
		absReal, _ := filepath.Abs(realH)
		if abs == absReal {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find real.h via symlink/../ path, got extra=%v", extra)
	}
}

func TestScanEdgeForceInclude(t *testing.T) {
	// Integration test: -include flag should cause the file to be scanned
	// and its transitive dependencies discovered.
	dir := t.TempDir()

	mainC := filepath.Join(dir, "main.c")
	precomp := filepath.Join(dir, "precompiled.h")
	dep := filepath.Join(dir, "dep.h")

	os.WriteFile(mainC, []byte("int main() { return 0; }\n"), 0644)
	os.WriteFile(precomp, []byte(`#include "dep.h"`+"\n"), 0644)
	os.WriteFile(dep, []byte("// dependency\n"), 0644)

	s := include_scanner.New()
	command := "gcc -include " + precomp + " -c " + mainC
	extra, err := s.ScanEdge([]string{mainC}, command)
	if err != nil {
		t.Fatal(err)
	}

	found := make(map[string]bool)
	for _, f := range extra {
		abs, _ := filepath.Abs(f)
		found[abs] = true
	}

	absPrecomp, _ := filepath.Abs(precomp)
	absDep, _ := filepath.Abs(dep)
	if !found[absPrecomp] {
		t.Errorf("expected to find precompiled.h from -include flag, got extra=%v", extra)
	}
	if !found[absDep] {
		t.Errorf("expected to find dep.h (transitive from -include), got extra=%v", extra)
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
			result := include_scanner.ExtractIntermediateDirsFromCommand(tt.command)
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
		result := include_scanner.ExtractCommandReferencedPaths("gcc -o test test.c", root)
		if len(result) != 0 {
			t.Errorf("expected no paths, got %v", result)
		}
	})

	t.Run("file path includes siblings", func(t *testing.T) {
		command := "cmake -P " + buildCmake
		result := include_scanner.ExtractCommandReferencedPaths(command, root)
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
		result := include_scanner.ExtractCommandReferencedPaths(command, root)
		if len(result) != 1 || result[0] != subDir {
			t.Errorf("expected [%s], got %v", subDir, result)
		}
	})

	t.Run("path embedded in flag", func(t *testing.T) {
		command := "gcc -DCONFIG_PATH=" + configTxt + " test.c"
		result := include_scanner.ExtractCommandReferencedPaths(command, root)
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
		result := include_scanner.ExtractCommandReferencedPaths(command, root)
		if len(result) != 0 {
			t.Errorf("expected no paths for nonexistent file, got %v", result)
		}
	})

	t.Run("deduplicates paths", func(t *testing.T) {
		command := "cmake -P " + buildCmake + " -P " + buildCmake
		result := include_scanner.ExtractCommandReferencedPaths(command, root)
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
		result := include_scanner.ExtractCommandReferencedPaths(command, root)
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
		result := include_scanner.ExtractSearchDirectoryContents(command, root)
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
		result := include_scanner.ExtractSearchDirectoryContents(command, root)
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
		result := include_scanner.ExtractSearchDirectoryContents(command, root)
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
		result := include_scanner.ExtractSearchDirectoryContents(command, root)
		for _, p := range result {
			if !strings.HasPrefix(p, root) {
				t.Errorf("got path outside root: %s", p)
			}
		}
	})

	t.Run("deduplicates directories", func(t *testing.T) {
		command := "gcc -I" + incDir + " -I" + incDir + " test.c"
		result := include_scanner.ExtractSearchDirectoryContents(command, root)
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
		result := include_scanner.ExtractSearchDirectoryContents(command, root)
		if len(result) != 0 {
			t.Errorf("expected no files, got %v", result)
		}
	})
}

// buildThinArchive constructs a minimal GNU thin archive in memory.
// memberNames are stored in an extended name table (// entry) and
// referenced from each member header via /offset format.
func buildThinArchive(memberNames []string) []byte {
	var buf []byte
	buf = append(buf, "!<thin>\n"...)

	// Build the extended name table: each name terminated by "/\n".
	var extBuf []byte
	offsets := make([]int, len(memberNames))
	for i, name := range memberNames {
		offsets[i] = len(extBuf)
		extBuf = append(extBuf, name...)
		extBuf = append(extBuf, "/\n"...)
	}

	// Write the "//" header for the extended name table.
	buf = append(buf, arHeader("//", len(extBuf))...)
	buf = append(buf, extBuf...)
	if len(buf)%2 != 0 {
		buf = append(buf, '\n')
	}

	// Write a member header for each file (no data follows in thin archives).
	for i := range memberNames {
		name := fmt.Sprintf("/%d", offsets[i])
		buf = append(buf, arHeader(name, 0)...)
	}

	return buf
}

// arHeader builds a 60-byte AR header. The name field is 16 bytes
// (right-padded with spaces), matching real GNU ar behavior.
func arHeader(name string, size int) []byte {
	// AR header: name/16 | mtime/12 | uid/6 | gid/6 | mode/8 | size/10 | magic/2
	hdr := make([]byte, 60)
	for i := range hdr {
		hdr[i] = ' '
	}
	copy(hdr[0:], name)
	// Pad remaining name field with spaces (already done by fill).
	sizeStr := fmt.Sprintf("%d", size)
	copy(hdr[48:], sizeStr)
	hdr[58] = '`'
	hdr[59] = '\n'
	return hdr
}

func TestExtractThinArchiveMembers(t *testing.T) {
	dir := t.TempDir()

	t.Run("not a thin archive", func(t *testing.T) {
		path := filepath.Join(dir, "regular.a")
		os.WriteFile(path, []byte("!<arch>\nnot thin"), 0644)
		members := include_scanner.ExtractThinArchiveMembers(path)
		if members != nil {
			t.Errorf("expected nil for regular archive, got %v", members)
		}
	})

	t.Run("nonexistent file", func(t *testing.T) {
		members := include_scanner.ExtractThinArchiveMembers(filepath.Join(dir, "nope.a"))
		if members != nil {
			t.Errorf("expected nil for nonexistent file, got %v", members)
		}
	})

	t.Run("parses members with extended names", func(t *testing.T) {
		names := []string{
			"../../deps/icu/source/common/foo.o",
			"../../deps/icu/source/common/bar.o",
			"short.o",
		}
		archivePath := filepath.Join(dir, "libtest.a")
		os.WriteFile(archivePath, buildThinArchive(names), 0644)

		members := include_scanner.ExtractThinArchiveMembers(archivePath)
		if len(members) != len(names) {
			t.Fatalf("expected %d members, got %d: %v", len(names), len(members), members)
		}
		for i, m := range members {
			expected := filepath.Clean(filepath.Join(dir, names[i]))
			if m != expected {
				t.Errorf("member[%d] = %q, want %q", i, m, expected)
			}
		}
	})

	t.Run("handles space-padded offset fields", func(t *testing.T) {
		// Regression test: AR header name field is 16 bytes wide.
		// For extended name references like "/1406", the field becomes
		// "/1406          /" (offset + spaces + trailing slash).
		// The parser must TrimSpace the offset to avoid Atoi failures.
		names := []string{
			strings.Repeat("a", 200) + ".o", // long name forces large offset
			"b.o",
		}
		archivePath := filepath.Join(dir, "libpadded.a")
		os.WriteFile(archivePath, buildThinArchive(names), 0644)

		members := include_scanner.ExtractThinArchiveMembers(archivePath)
		if len(members) != 2 {
			t.Fatalf("expected 2 members, got %d: %v", len(members), members)
		}
	})

	t.Run("absolute member paths", func(t *testing.T) {
		absPath := "/absolute/path/to/foo.o"
		archivePath := filepath.Join(dir, "libabs.a")
		os.WriteFile(archivePath, buildThinArchive([]string{absPath}), 0644)

		members := include_scanner.ExtractThinArchiveMembers(archivePath)
		if len(members) != 1 || members[0] != absPath {
			t.Errorf("expected [%s], got %v", absPath, members)
		}
	})
}

func TestExtractRelativeDotDotContents(t *testing.T) {
	// Create a project root with some nested directories to simulate
	// a command like "cd ../../tools/icu" from a build output directory.
	root := t.TempDir()

	toolsDir := filepath.Join(root, "tools", "icu")
	os.MkdirAll(toolsDir, 0755)
	icupkg := filepath.Join(toolsDir, "icupkg.py")
	icudata := filepath.Join(toolsDir, "icudata.txt")
	os.WriteFile(icupkg, []byte("#!/usr/bin/env python"), 0644)
	os.WriteFile(icudata, []byte("data"), 0644)

	// Also create a subdirectory inside icu.
	subDir := filepath.Join(toolsDir, "sub")
	os.MkdirAll(subDir, 0755)
	subFile := filepath.Join(subDir, "nested.txt")
	os.WriteFile(subFile, []byte("nested"), 0644)

	// Simulate running from root/src/out/Debug (3 levels deep).
	buildDir := filepath.Join(root, "src", "out", "Debug")
	os.MkdirAll(buildDir, 0755)
	origDir, _ := os.Getwd()
	os.Chdir(buildDir)
	defer os.Chdir(origDir)

	t.Run("cd with relative dotdot resolves directory contents", func(t *testing.T) {
		command := "cd ../../../tools/icu && python icupkg.py"
		result := include_scanner.ExtractRelativeDotDotContents(command, root)
		resultSet := make(map[string]bool)
		for _, p := range result {
			resultSet[p] = true
		}
		if !resultSet[icupkg] {
			t.Errorf("expected %s in result, got %v", icupkg, result)
		}
		if !resultSet[icudata] {
			t.Errorf("expected %s in result, got %v", icudata, result)
		}
		if !resultSet[subFile] {
			t.Errorf("expected nested %s in result, got %v", subFile, result)
		}
	})

	t.Run("relative dotdot file path includes siblings", func(t *testing.T) {
		command := "python ../../../tools/icu/icupkg.py"
		result := include_scanner.ExtractRelativeDotDotContents(command, root)
		resultSet := make(map[string]bool)
		for _, p := range result {
			resultSet[p] = true
		}
		if !resultSet[icupkg] {
			t.Errorf("expected %s in result, got %v", icupkg, result)
		}
		// Sibling file in the same directory should also be included.
		if !resultSet[icudata] {
			t.Errorf("expected sibling %s in result, got %v", icudata, result)
		}
	})

	t.Run("file path sibling inclusion for scripts", func(t *testing.T) {
		// Simulate a Python script that needs sibling modules, like
		// check_protocol_compatibility.py needing pdl.py.
		scriptsDir := filepath.Join(root, "deps", "v8", "third_party", "inspector_protocol")
		os.MkdirAll(scriptsDir, 0755)
		mainScript := filepath.Join(scriptsDir, "check_protocol_compatibility.py")
		siblingModule := filepath.Join(scriptsDir, "pdl.py")
		os.WriteFile(mainScript, []byte("import pdl"), 0644)
		os.WriteFile(siblingModule, []byte("# pdl module"), 0644)

		command := "cd ../../../tools/v8_gypfiles; python ../../../deps/v8/third_party/inspector_protocol/check_protocol_compatibility.py"
		result := include_scanner.ExtractRelativeDotDotContents(command, root)
		resultSet := make(map[string]bool)
		for _, p := range result {
			resultSet[p] = true
		}
		if !resultSet[mainScript] {
			t.Errorf("expected %s in result, got %v", mainScript, result)
		}
		if !resultSet[siblingModule] {
			t.Errorf("expected sibling %s in result, got %v", siblingModule, result)
		}
	})

	t.Run("no dotdot paths returns empty", func(t *testing.T) {
		command := "gcc -o test test.c"
		result := include_scanner.ExtractRelativeDotDotContents(command, root)
		if len(result) != 0 {
			t.Errorf("expected empty, got %v", result)
		}
	})

	t.Run("absolute dotdot paths ignored", func(t *testing.T) {
		command := "gcc -I/some/../path test.c"
		result := include_scanner.ExtractRelativeDotDotContents(command, root)
		if len(result) != 0 {
			t.Errorf("expected empty for absolute paths, got %v", result)
		}
	})

	t.Run("dotdot resolving outside root ignored", func(t *testing.T) {
		command := "cd ../../../../../../../../tmp && ls"
		result := include_scanner.ExtractRelativeDotDotContents(command, root)
		if len(result) != 0 {
			t.Errorf("expected empty for path outside root, got %v", result)
		}
	})

	t.Run("strips trailing shell operators", func(t *testing.T) {
		command := "cd ../../../tools/icu;&& python icupkg.py"
		result := include_scanner.ExtractRelativeDotDotContents(command, root)
		resultSet := make(map[string]bool)
		for _, p := range result {
			resultSet[p] = true
		}
		if !resultSet[icupkg] {
			t.Errorf("expected %s after stripping ';', got %v", icupkg, result)
		}
	})

	t.Run("flag prefix with dotdot", func(t *testing.T) {
		command := "gcc -I../../../tools/icu test.c"
		result := include_scanner.ExtractRelativeDotDotContents(command, root)
		resultSet := make(map[string]bool)
		for _, p := range result {
			resultSet[p] = true
		}
		if !resultSet[icupkg] {
			t.Errorf("expected files from -I dir, got %v", result)
		}
	})

	t.Run("deduplicates paths", func(t *testing.T) {
		command := "cd ../../../tools/icu && ls ../../../tools/icu"
		result := include_scanner.ExtractRelativeDotDotContents(command, root)
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
}

func TestExtractCdRelativePaths(t *testing.T) {
	root := t.TempDir()

	// Create directory structure:
	//   root/scripts/create_repo.py
	//   root/scripts/helper.py
	//   root/data/  (directory with files)
	scriptsDir := filepath.Join(root, "scripts")
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(scriptsDir, 0755)
	os.MkdirAll(dataDir, 0755)

	createRepo := filepath.Join(scriptsDir, "create_repo.py")
	helperPy := filepath.Join(scriptsDir, "helper.py")
	dataFile := filepath.Join(dataDir, "config.json")
	os.WriteFile(createRepo, []byte("#!/usr/bin/env python3"), 0644)
	os.WriteFile(helperPy, []byte("# helper"), 0644)
	os.WriteFile(dataFile, []byte("{}"), 0644)

	t.Run("resolves relative script path against cd target", func(t *testing.T) {
		command := "cd " + root + " && /usr/bin/python3 scripts/create_repo.py arg1 arg2"
		result := include_scanner.ExtractCdRelativePaths(command, root)
		resultSet := make(map[string]bool)
		for _, p := range result {
			resultSet[p] = true
		}
		if !resultSet[createRepo] {
			t.Errorf("expected %s in result, got %v", createRepo, result)
		}
		// Sibling should be included.
		if !resultSet[helperPy] {
			t.Errorf("expected sibling %s in result, got %v", helperPy, result)
		}
	})

	t.Run("resolves directory path against cd target", func(t *testing.T) {
		command := "cd " + root + " && ls data"
		result := include_scanner.ExtractCdRelativePaths(command, root)
		resultSet := make(map[string]bool)
		for _, p := range result {
			resultSet[p] = true
		}
		if !resultSet[dataFile] {
			t.Errorf("expected %s in result, got %v", dataFile, result)
		}
	})

	t.Run("no cd returns empty", func(t *testing.T) {
		command := "/usr/bin/python3 scripts/create_repo.py"
		result := include_scanner.ExtractCdRelativePaths(command, root)
		if len(result) != 0 {
			t.Errorf("expected empty without cd, got %v", result)
		}
	})

	t.Run("cd to non-root directory returns empty", func(t *testing.T) {
		command := "cd /tmp && python3 scripts/foo.py"
		result := include_scanner.ExtractCdRelativePaths(command, root)
		if len(result) != 0 {
			t.Errorf("expected empty for cd outside root, got %v", result)
		}
	})

	t.Run("skips flags and shell operators", func(t *testing.T) {
		command := "cd " + root + " && /usr/bin/python3 -u scripts/create_repo.py --flag arg1"
		result := include_scanner.ExtractCdRelativePaths(command, root)
		resultSet := make(map[string]bool)
		for _, p := range result {
			resultSet[p] = true
		}
		if !resultSet[createRepo] {
			t.Errorf("expected %s in result, got %v", createRepo, result)
		}
	})

	t.Run("nonexistent relative path ignored", func(t *testing.T) {
		command := "cd " + root + " && python3 nonexistent/script.py"
		result := include_scanner.ExtractCdRelativePaths(command, root)
		if len(result) != 0 {
			t.Errorf("expected empty for nonexistent path, got %v", result)
		}
	})

	t.Run("strips trailing semicolon from cd target", func(t *testing.T) {
		command := "cd " + root + "; /usr/bin/python3 scripts/create_repo.py"
		result := include_scanner.ExtractCdRelativePaths(command, root)
		resultSet := make(map[string]bool)
		for _, p := range result {
			resultSet[p] = true
		}
		if !resultSet[createRepo] {
			t.Errorf("expected %s in result, got %v", createRepo, result)
		}
	})
}
