package parity_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var (
	reninjaBin  string
	cppNinjaBin string
)

func runParityTest(t *testing.T, proj parityProject) {
	// Check setup dependencies.
	switch proj.Setup {
	case "configure.py":
		requireTool(t, "python3", "python3")
		requireCXXCompiler(t)
	case "cmake":
		requireTool(t, "cmake", "cmake")
		requireCXXCompiler(t)
	}

	archivePath, err := filepath.Abs(filepath.Join("testdata", proj.Archive))
	require.NoError(t, err)
	if _, err := os.Stat(archivePath); os.IsNotExist(err) {
		t.Skipf("archive not found: %s (run generate_testdata.sh)", proj.Archive)
	}

	// Use a single build directory: extract, generate build.ninja, build with
	// C++ ninja, clean, build with reninja, compare.
	buildDir := t.TempDir()

	// Extract archive.
	extractArchive(t, archivePath, buildDir)
	projectDir := filepath.Join(buildDir, proj.SourceDir)

	// Generate build.ninja.
	generateBuildNinja(t, proj, projectDir)

	// --- Build with C++ ninja ---
	t.Log("Building with C++ ninja...")
	runNinja(t, cppNinjaBin, projectDir)

	// Hash outputs.
	cppHashes := hashOutputs(t, projectDir, proj.Outputs)
	for f, h := range cppHashes {
		t.Logf("  C++ ninja: %s = %s", f, h)
	}

	// Clean build artifacts.
	cleanBuild(t, cppNinjaBin, projectDir)

	// --- Build with reninja ---
	t.Log("Building with reninja...")
	runNinja(t, reninjaBin, projectDir)

	// Hash outputs.
	goHashes := hashOutputs(t, projectDir, proj.Outputs)
	for f, h := range goHashes {
		t.Logf("  reninja:   %s = %s", f, h)
	}

	// --- Compare ---
	for _, output := range proj.Outputs {
		cppHash, ok := cppHashes[output]
		require.True(t, ok, "missing C++ ninja output: %s", output)
		goHash, ok := goHashes[output]
		require.True(t, ok, "missing reninja output: %s", output)
		require.Equal(t, cppHash, goHash, "output mismatch for %s", output)
	}
}

// --- Helpers ---

func requireTool(t *testing.T, nameOrPath string, description string) {
	t.Helper()
	if filepath.IsAbs(nameOrPath) {
		if _, err := os.Stat(nameOrPath); os.IsNotExist(err) {
			t.Skipf("%s not found at %s", description, nameOrPath)
		}
		return
	}
	if _, err := exec.LookPath(nameOrPath); err != nil {
		t.Skipf("%s not found on PATH", description)
	}
}

func requireCXXCompiler(t *testing.T) {
	t.Helper()
	for _, cc := range []string{"g++", "clang++"} {
		if _, err := exec.LookPath(cc); err == nil {
			return
		}
	}
	t.Skip("no C++ compiler found (need g++ or clang++)")
}

func extractArchive(t *testing.T, archivePath, destDir string) {
	t.Helper()
	cmd := exec.Command("tar", "xzf", archivePath, "-C", destDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "tar extract failed: %s", string(out))
}

func generateBuildNinja(t *testing.T, proj parityProject, projectDir string) {
	t.Helper()

	switch proj.Setup {
	case "configure.py":
		cmd := exec.Command("python3", "configure.py")
		cmd.Dir = projectDir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "configure.py failed: %s", string(out))

	case "cmake":
		args := []string{"-G", "Ninja"}
		args = append(args, proj.CMakeFlags...)
		args = append(args, ".")
		cmd := exec.Command("cmake", args...)
		cmd.Dir = projectDir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "cmake failed: %s", string(out))

	default:
		t.Fatalf("unknown setup type: %s", proj.Setup)
	}

	// Verify build.ninja was created.
	buildNinja := filepath.Join(projectDir, "build.ninja")
	_, err := os.Stat(buildNinja)
	require.NoError(t, err, "build.ninja not generated")
}

func runNinja(t *testing.T, ninjaBin string, projectDir string) {
	t.Helper()
	cmd := exec.Command(ninjaBin, "-j1")
	cmd.Dir = projectDir
	cmd.Env = buildEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ninja build failed:\n%s", string(out))
	}
}

func cleanBuild(t *testing.T, ninjaBin string, projectDir string) {
	t.Helper()

	// Use ninja -t clean to remove all build outputs.
	cmd := exec.Command(ninjaBin, "-t", "clean")
	cmd.Dir = projectDir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "ninja -t clean failed: %s", string(out))

	// Remove ninja state files.
	for _, f := range []string{".ninja_log", ".ninja_deps", ".ninja_compact_execution_log.binpb.zst"} {
		os.Remove(filepath.Join(projectDir, f))
	}
}

func hashOutputs(t *testing.T, projectDir string, outputs []string) map[string]string {
	t.Helper()
	hashes := make(map[string]string, len(outputs))
	for _, output := range outputs {
		path := filepath.Join(projectDir, output)
		h := hashFile(t, path)
		hashes[output] = h
	}
	return hashes
}

func hashFile(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err, "failed to open %s", path)
	defer f.Close()

	hasher := sha256.New()
	_, err = io.Copy(hasher, f)
	require.NoError(t, err, "failed to read %s", path)
	return hex.EncodeToString(hasher.Sum(nil))
}

// buildEnv returns a clean environment for ninja builds, removing variables
// that could cause non-deterministic output.
func buildEnv() []string {
	env := os.Environ()
	// Filter out variables that might affect build determinism.
	var filtered []string
	for _, e := range env {
		key := strings.SplitN(e, "=", 2)[0]
		switch key {
		case "SOURCE_DATE_EPOCH":
			// Keep this if set - it helps determinism.
			filtered = append(filtered, e)
		default:
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func TestMain(m *testing.M) {
	// Build reninja binary once for all tests.
	tmpDir, err := os.MkdirTemp("", "parity-reninja-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		os.Exit(1)
	}

	reninjaBin = filepath.Join(tmpDir, "reninja")
	cmd := exec.Command("go", "build", "-o", reninjaBin, "../cmd/ninja")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build reninja: %v\n", err)
		os.Exit(1)
	}

	// Locate C++ ninja binary from testdata.
	cppNinjaBin, _ = filepath.Abs(filepath.Join("testdata", "ninja"))

	exitCode := m.Run()
	os.RemoveAll(tmpDir)
	os.Exit(exitCode)
}

type parityProject struct {
	Name       string
	Archive    string   // filename in testdata/
	Setup      string   // "configure.py" or "cmake"
	CMakeFlags []string // extra cmake flags
	Outputs    []string // output files to hash-compare (relative to build dir)
	SourceDir  string   // subdirectory name inside the archive
}

func TestParity(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("parity tests only supported on linux/amd64 (checked-in C++ ninja binary)")
	}

	requireTool(t, cppNinjaBin, "C++ ninja binary (run generate_testdata.sh)")

	// Verify C++ ninja works.
	out, err := exec.Command(cppNinjaBin, "--version").Output()
	require.NoError(t, err, "C++ ninja binary failed to run")
	t.Logf("C++ ninja version: %s", strings.TrimSpace(string(out)))

	// Verify reninja works.
	out, err = exec.Command(reninjaBin, "--version").Output()
	require.NoError(t, err, "reninja binary failed to run")
	t.Logf("reninja version: %s", strings.TrimSpace(string(out)))

	testProjects := []parityProject{
		{
			Name:      "ninja",
			Archive:   "ninja-src.tar.gz",
			Setup:     "cmake",
			Outputs:   []string{"ninja"},
			SourceDir: "ninja-1.12.1",
		},
		{
			Name:    "fmt",
			Archive: "fmt-src.tar.gz",
			Setup:   "cmake",
			CMakeFlags: []string{
				"-DCMAKE_BUILD_TYPE=Release",
				"-DFMT_TEST=OFF",
				"-DFMT_DOC=OFF",
			},
			Outputs:   []string{"libfmt.a"},
			SourceDir: "fmt-11.0.2",
		},
		{
			Name:    "zlib",
			Archive: "zlib-src.tar.gz",
			Setup:   "cmake",
			CMakeFlags: []string{
				"-DCMAKE_BUILD_TYPE=Release",
			},
			Outputs:   []string{"libz.a"},
			SourceDir: "zlib-1.3.1",
		},
	}

	for _, proj := range testProjects {
		t.Run(proj.Name, func(t *testing.T) {
			runParityTest(t, proj)
		})
	}
}
