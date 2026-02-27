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
	"strconv"
	"strings"
	"testing"
	"time"

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

type statsEntry struct {
	count   int
	totalMS float64
}

type buildResult struct {
	hashes  map[string]string
	stats   map[string]statsEntry
	elapsed time.Duration
}

func parseStats(output string) map[string]statsEntry {
	stats := make(map[string]statsEntry)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "metric") {
			continue
		}
		if strings.Contains(line, "hash load") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		name := strings.TrimSpace(fields[0])
		count, err := strconv.Atoi(strings.TrimSpace(fields[1]))
		if err != nil {
			continue
		}
		ms, err := strconv.ParseFloat(strings.TrimSpace(fields[2]), 64)
		if err != nil {
			continue
		}
		stats[name] = statsEntry{count: count, totalMS: ms}
	}
	return stats
}

func runParityTest(t *testing.T, proj parityProject) {
	sourceDir := expandSource(t, proj.Archive, proj.SourceDir)

	buildAndHash := func(label string, ninjaBin string) buildResult {
		buildDir := t.TempDir()
		configCmd := strings.ReplaceAll(proj.Configure, "{source}", sourceDir)
		run(t, buildDir, "bash", "-c", configCmd)

		t.Logf("Building with %s...", label)
		cmd := exec.Command(ninjaBin, "-d", "stats", "-j1")
		cmd.Dir = buildDir
		start := time.Now()
		out, err := cmd.CombinedOutput()
		elapsed := time.Since(start)
		require.NoError(t, err, "%s build failed:\n%s", label, string(out))

		hashes := hashOutputs(t, buildDir, proj.Outputs)
		for f, h := range hashes {
			t.Logf("  %s: %s = %s", label, f, h)
		}

		stats := parseStats(string(out))
		t.Logf("  %s: elapsed=%s", label, elapsed)
		for name, s := range stats {
			t.Logf("  %s: stat %s count=%d total=%.3fms", label, name, s.count, s.totalMS)
		}

		return buildResult{hashes: hashes, stats: stats, elapsed: elapsed}
	}

	cppResult := buildAndHash("C++ ninja", cppNinjaBin)
	goResult := buildAndHash("reninja", reninjaBin)

	// Compare output hashes.
	for _, output := range proj.Outputs {
		require.Equal(t, cppResult.hashes[output], goResult.hashes[output], "output mismatch for %s", output)
	}

	// Compare stats counts (exact match) for key metrics.
	compareStats := []string{"StartEdge", "FinishCommand", ".ninja_log load", ".ninja_deps load"}
	for _, name := range compareStats {
		cppStat, cppOK := cppResult.stats[name]
		goStat, goOK := goResult.stats[name]
		if cppOK && goOK {
			require.Equal(t, cppStat.count, goStat.count, "stats count mismatch for %q", name)
		}
	}

	// Compare wall-clock time: reninja should be no more than 2x C++ ninja.
	t.Logf("Wall-clock: C++ ninja=%s, reninja=%s", cppResult.elapsed, goResult.elapsed)
	require.LessOrEqual(t, goResult.elapsed, 2*cppResult.elapsed,
		"reninja took %s, more than 2x C++ ninja's %s", goResult.elapsed, cppResult.elapsed)
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
