//go:build parity

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
	Configure string   // shell command to generate build.ninja (use {source} for source dir)
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
			Configure: "cmake -G Ninja -DCMAKE_POLICY_VERSION_MINIMUM=3.5 {source}",
			Outputs:   []string{"ninja"},
			SourceDir: "ninja-1.12.1",
		},
		{
			Name:      "fmt",
			Archive:   "fmt-src.tar.gz",
			Configure: "cmake -G Ninja -DCMAKE_BUILD_TYPE=Release -DFMT_TEST=OFF -DFMT_DOC=OFF {source}",
			Outputs:   []string{"libfmt.a"},
			SourceDir: "fmt-11.0.2",
		},
		{
			Name:      "zlib",
			Archive:   "zlib-src.tar.gz",
			Configure: "cmake -G Ninja -DCMAKE_BUILD_TYPE=Release {source}",
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

// expandSource extracts the archive to a hash-named directory under testdata/
// so repeated runs skip extraction. Returns the path to the source directory.
func expandSource(t *testing.T, archive, sourceDir string) string {
	t.Helper()
	archivePath, err := filepath.Abs(filepath.Join("testdata", archive))
	require.NoError(t, err)

	// Hash the archive to get a content-addressed directory name.
	h := hashFile(t, archivePath)
	expandedDir, err := filepath.Abs(filepath.Join("testdata", sourceDir+"-"+h))
	require.NoError(t, err)

	srcDir := filepath.Join(expandedDir, sourceDir)
	if _, err := os.Stat(srcDir); err == nil {
		t.Logf("Using cached source: %s", expandedDir)
		return srcDir
	}

	t.Logf("Extracting %s to %s", archive, expandedDir)
	os.MkdirAll(expandedDir, 0o755)
	run(t, expandedDir, "tar", "xzf", archivePath)
	return srcDir
}

func runParityTest(t *testing.T, proj parityProject) {
	sourceDir := expandSource(t, proj.Archive, proj.SourceDir)

	// Each build gets a fresh temp dir (out-of-source cmake build).
	buildAndHash := func(label string, ninjaBin string) map[string]string {
		buildDir := t.TempDir()
		configCmd := strings.ReplaceAll(proj.Configure, "{source}", sourceDir)
		run(t, buildDir, "bash", "-c", configCmd)

		t.Logf("Building with %s...", label)
		run(t, buildDir, ninjaBin, "-j1")

		hashes := hashOutputs(t, buildDir, proj.Outputs)
		for f, h := range hashes {
			t.Logf("  %s: %s = %s", label, f, h)
		}
		return hashes
	}

	cppHashes := buildAndHash("C++ ninja", cppNinjaBin)
	goHashes := buildAndHash("reninja", reninjaBin)

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

func hashFile(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	hasher := sha256.New()
	_, err = io.Copy(hasher, f)
	require.NoError(t, err)
	return hex.EncodeToString(hasher.Sum(nil))
}

func hashOutputs(t *testing.T, dir string, outputs []string) map[string]string {
	t.Helper()
	hashes := make(map[string]string, len(outputs))
	for _, output := range outputs {
		hashes[output] = hashFile(t, filepath.Join(dir, output))
	}
	return hashes
}
