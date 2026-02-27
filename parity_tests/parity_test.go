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
	"testing"

	"github.com/stretchr/testify/require"
)

var (
	reninjaBin  string
	cppNinjaBin string
)

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
	Name      string
	Archive   string   // filename in testdata/
	Configure string   // shell command to generate build.ninja
	Outputs   []string // output files to hash-compare (relative to build dir)
	SourceDir string   // subdirectory name inside the archive
}

func TestParity(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("parity tests only supported on linux/amd64 (checked-in C++ ninja binary)")
	}

	testProjects := []parityProject{
		{
			Name:      "ninja",
			Archive:   "ninja-src.tar.gz",
			Configure: "cmake -G Ninja .",
			Outputs:   []string{"ninja"},
			SourceDir: "ninja-1.12.1",
		},
		{
			Name:      "fmt",
			Archive:   "fmt-src.tar.gz",
			Configure: "cmake -G Ninja -DCMAKE_BUILD_TYPE=Release -DFMT_TEST=OFF -DFMT_DOC=OFF .",
			Outputs:   []string{"libfmt.a"},
			SourceDir: "fmt-11.0.2",
		},
		{
			Name:      "zlib",
			Archive:   "zlib-src.tar.gz",
			Configure: "cmake -G Ninja -DCMAKE_BUILD_TYPE=Release .",
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

func runParityTest(t *testing.T, proj parityProject) {
	archivePath, err := filepath.Abs(filepath.Join("testdata", proj.Archive))
	require.NoError(t, err)

	buildDir := t.TempDir()

	// Extract archive.
	run(t, buildDir, "tar", "xzf", archivePath)
	projectDir := filepath.Join(buildDir, proj.SourceDir)

	// Generate build.ninja.
	run(t, projectDir, "bash", "-c", proj.Configure)

	// Build with C++ ninja, hash outputs.
	t.Log("Building with C++ ninja...")
	run(t, projectDir, cppNinjaBin, "-j1")
	cppHashes := hashOutputs(t, projectDir, proj.Outputs)
	for f, h := range cppHashes {
		t.Logf("  C++ ninja: %s = %s", f, h)
	}

	// Clean build artifacts.
	run(t, projectDir, cppNinjaBin, "-t", "clean")
	for _, f := range []string{".ninja_log", ".ninja_deps", ".ninja_compact_execution_log.binpb.zst"} {
		os.Remove(filepath.Join(projectDir, f))
	}

	// Build with reninja, hash outputs.
	t.Log("Building with reninja...")
	run(t, projectDir, reninjaBin, "-j1")
	goHashes := hashOutputs(t, projectDir, proj.Outputs)
	for f, h := range goHashes {
		t.Logf("  reninja:   %s = %s", f, h)
	}

	// Compare hashes.
	for _, output := range proj.Outputs {
		require.Equal(t, cppHashes[output], goHashes[output], "output mismatch for %s", output)
	}
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s failed:\n%s", name, string(out))
}

func hashOutputs(t *testing.T, projectDir string, outputs []string) map[string]string {
	t.Helper()
	hashes := make(map[string]string, len(outputs))
	for _, output := range outputs {
		path := filepath.Join(projectDir, output)
		f, err := os.Open(path)
		require.NoError(t, err)
		defer f.Close()
		hasher := sha256.New()
		_, err = io.Copy(hasher, f)
		require.NoError(t, err)
		hashes[output] = hex.EncodeToString(hasher.Sum(nil))
	}
	return hashes
}
