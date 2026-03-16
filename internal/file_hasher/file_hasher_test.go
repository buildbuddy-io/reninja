package file_hasher_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/buildbuddy-io/reninja/internal/digest"
	"github.com/buildbuddy-io/reninja/internal/file_hasher"
	"github.com/stretchr/testify/require"

	repb "github.com/buildbuddy-io/reninja/genproto/remote_execution"
)

// sharedDir is created once for the entire test binary run and never cleaned
// up mid-run. This prevents inode reuse within a single run, which would
// otherwise cause false cache hits in the global fileHashes map.
var sharedDir string

func TestMain(m *testing.M) {
	var err error
	sharedDir, err = os.MkdirTemp("", "file_hasher_test")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(sharedDir)
	os.Exit(m.Run())
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(sharedDir, name)
	err := os.WriteFile(path, []byte(content), 0644)
	require.NoError(t, err)
	return path
}

func TestHashFile_MatchesDigestCompute(t *testing.T) {
	path := writeFile(t, "basic.txt", "hello world")

	got, err := file_hasher.HashFile(path, repb.DigestFunction_SHA256)
	require.NoError(t, err)

	want, err := digest.ComputeForFile(path, repb.DigestFunction_SHA256)
	require.NoError(t, err)

	require.Equal(t, want, got)
}

func TestHashFile_RepeatedCallReturnsSameDigest(t *testing.T) {
	path := writeFile(t, "repeated.txt", "repeated content")

	d1, err := file_hasher.HashFile(path, repb.DigestFunction_SHA256)
	require.NoError(t, err)

	d2, err := file_hasher.HashFile(path, repb.DigestFunction_SHA256)
	require.NoError(t, err)

	require.Equal(t, d1, d2)
}

func TestHashFile_MissingFile(t *testing.T) {
	_, err := file_hasher.HashFile("/nonexistent/path/file.txt", repb.DigestFunction_SHA256)
	require.Error(t, err)
}

func TestHashFile_DifferentContentDifferentHash(t *testing.T) {
	path1 := writeFile(t, "content_a.txt", "content A")
	path2 := writeFile(t, "content_b.txt", "content B")

	d1, err := file_hasher.HashFile(path1, repb.DigestFunction_SHA256)
	require.NoError(t, err)

	d2, err := file_hasher.HashFile(path2, repb.DigestFunction_SHA256)
	require.NoError(t, err)

	require.NotEqual(t, d1, d2)
}

func TestHashFile_EmptyFile(t *testing.T) {
	path := writeFile(t, "empty.txt", "")

	got, err := file_hasher.HashFile(path, repb.DigestFunction_SHA256)
	require.NoError(t, err)

	want, err := digest.ComputeForFile(path, repb.DigestFunction_SHA256)
	require.NoError(t, err)

	require.Equal(t, want, got)
}

func TestHashFile_SameContentDifferentDigestFunction(t *testing.T) {
	path := writeFile(t, "digest_fn.txt", "same content")

	dSHA256, err := file_hasher.HashFile(path, repb.DigestFunction_SHA256)
	require.NoError(t, err)

	dSHA1, err := file_hasher.HashFile(path, repb.DigestFunction_SHA1)
	require.NoError(t, err)

	require.NotEqual(t, dSHA256, dSHA1)
}

func TestHashFile_ConcurrentAccess(t *testing.T) {
	path := writeFile(t, "concurrent.txt", "concurrent content")

	want, err := digest.ComputeForFile(path, repb.DigestFunction_SHA256)
	require.NoError(t, err)

	const goroutines = 20
	results := make([]*repb.Digest, goroutines)
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = file_hasher.HashFile(path, repb.DigestFunction_SHA256)
		}(i)
	}
	wg.Wait()

	for i := range goroutines {
		require.NoError(t, errs[i])
		require.Equal(t, want, results[i])
	}
}
