package build_log_test

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/buildbuddy-io/reninja/internal/build_log"
	"github.com/buildbuddy-io/reninja/internal/state"
	"github.com/buildbuddy-io/reninja/internal/test"
	"github.com/buildbuddy-io/reninja/internal/timestamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testFilename = "BuildLogTest-tempfile"
)

type pathChecker func(s string) bool

func (p pathChecker) IsPathDead(s string) bool {
	return p(s)
}

var neverDead = pathChecker(func(s string) bool { return false })

func TestWriteRead(t *testing.T) {
	os.Remove(testFilename)
	t.Cleanup(func() {
		os.Remove(testFilename)
	})

	s := state.New()
	test.AssertParse(t, `rule cat
  command = cat $in > $out
build out: cat mid
build mid: cat in
`, s)
	log1 := build_log.NewBuildLog()
	require.NoError(t, log1.OpenForWrite(testFilename, neverDead))
	log1.RecordCommand(s.Edges()[0], 15, 18, 0)
	log1.RecordCommand(s.Edges()[1], 20, 25, 0)
	require.NoError(t, log1.Close())

	log2 := build_log.NewBuildLog()
	require.NoError(t, log2.Load(testFilename))

	assert.Equal(t, 2, len(log1.Entries()))
	assert.Equal(t, 2, len(log2.Entries()))

	e1 := log1.LookupByOutput("out")
	require.NotNil(t, e1)
	e2 := log2.LookupByOutput("out")
	require.NotNil(t, e2)

	assert.Equal(t, e1, e2)
	assert.Equal(t, int64(15), e1.StartTime)
	assert.Equal(t, "out", e1.Output)
}

func TestFirstWriteAddsSignature(t *testing.T) {
	os.Remove(testFilename)
	t.Cleanup(func() {
		os.Remove(testFilename)
	})

	expectedVersion := "# ninja log vX\n"
	versionPos := len(expectedVersion) - 2 // Points at 'X'

	log := build_log.NewBuildLog()
	require.NoError(t, log.OpenForWrite(testFilename, neverDead))
	require.NoError(t, log.Close())

	contents, err := os.ReadFile(testFilename)
	require.NoError(t, err)
	if len(contents) >= versionPos {
		contents[versionPos] = 'X'
	}
	assert.Equal(t, expectedVersion, string(contents))

	// Opening the file anew shouldn't add a second version string.
	require.NoError(t, log.OpenForWrite(testFilename, neverDead))
	require.NoError(t, log.Close())

	contents, err = os.ReadFile(testFilename)
	require.NoError(t, err)
	if len(contents) >= versionPos {
		contents[versionPos] = 'X'
	}
	assert.Equal(t, expectedVersion, string(contents))
}

func TestDoubleEntry(t *testing.T) {
	os.Remove(testFilename)
	t.Cleanup(func() {
		os.Remove(testFilename)
	})

	f, err := os.Create(testFilename)
	require.NoError(t, err)
	fmt.Fprintf(f, "# ninja log v7\n")
	fmt.Fprintf(f, "0\t1\t2\tout\t%x\n", build_log.HashCommand("command abc"))
	fmt.Fprintf(f, "0\t1\t2\tout\t%x\n", build_log.HashCommand("command def"))
	f.Close()

	log := build_log.NewBuildLog()
	require.NoError(t, log.Load(testFilename))

	e := log.LookupByOutput("out")
	require.NotNil(t, e)
	assert.Equal(t, build_log.HashCommand("command def"), e.CommandHash)
}

func TestTruncate(t *testing.T) {
	os.Remove(testFilename)
	t.Cleanup(func() {
		os.Remove(testFilename)
	})

	s := state.New()
	test.AssertParse(t, `rule cat
  command = cat $in > $out
build out: cat mid
build mid: cat in
`, s)

	// Create initial log
	{
		log1 := build_log.NewBuildLog()
		require.NoError(t, log1.OpenForWrite(testFilename, neverDead))
		log1.RecordCommand(s.Edges()[0], 15, 18, 0)
		log1.RecordCommand(s.Edges()[1], 20, 25, 0)
		require.NoError(t, log1.Close())
	}

	stat, err := os.Stat(testFilename)
	require.NoError(t, err)
	assert.Greater(t, stat.Size(), int64(0))

	// For all possible truncations of the input file, assert that we don't
	// crash when parsing.
	for size := stat.Size(); size > 0; size-- {
		log2 := build_log.NewBuildLog()
		require.NoError(t, log2.OpenForWrite(testFilename, neverDead))
		log2.RecordCommand(s.Edges()[0], 15, 18, 0)
		log2.RecordCommand(s.Edges()[1], 20, 25, 0)
		require.NoError(t, log2.Close())

		require.NoError(t, os.Truncate(testFilename, size))

		log3 := build_log.NewBuildLog()
		// Should either load successfully or return an error, but not crash
		_ = log3.Load(testFilename)
	}
}

func TestObsoleteOldVersion(t *testing.T) {
	os.Remove(testFilename)
	t.Cleanup(func() {
		os.Remove(testFilename)
	})

	f, err := os.Create(testFilename)
	require.NoError(t, err)
	fmt.Fprintf(f, "# ninja log v3\n")
	fmt.Fprintf(f, "123 456 0 out command\n")
	f.Close()

	log := build_log.NewBuildLog()
	err = log.Load(testFilename)
	// Old version returns a sentinel error, not a hard error
	assert.True(t, errors.Is(err, build_log.ErrBuildLogVersionOld))
}

func TestSpacesInOutput(t *testing.T) {
	os.Remove(testFilename)
	t.Cleanup(func() {
		os.Remove(testFilename)
	})

	f, err := os.Create(testFilename)
	require.NoError(t, err)
	fmt.Fprintf(f, "# ninja log v7\n")
	fmt.Fprintf(f, "123\t456\t456\tout with space\t%x\n", build_log.HashCommand("command"))
	f.Close()

	log := build_log.NewBuildLog()
	require.NoError(t, log.Load(testFilename))

	e := log.LookupByOutput("out with space")
	require.NotNil(t, e)
	assert.Equal(t, int64(123), e.StartTime)
	assert.Equal(t, int64(456), e.EndTime)
	assert.Equal(t, timestamp.TimeStamp(456), e.Mtime)
	assert.Equal(t, build_log.HashCommand("command"), e.CommandHash)
}

func TestDuplicateVersionHeader(t *testing.T) {
	os.Remove(testFilename)
	t.Cleanup(func() {
		os.Remove(testFilename)
	})

	// Old versions of ninja accidentally wrote multiple version headers to the
	// build log on Windows. This shouldn't crash, and the second version header
	// should be ignored.
	f, err := os.Create(testFilename)
	require.NoError(t, err)
	fmt.Fprintf(f, "# ninja log v7\n")
	fmt.Fprintf(f, "123\t456\t456\tout\t%x\n", build_log.HashCommand("command"))
	fmt.Fprintf(f, "# ninja log v7\n")
	fmt.Fprintf(f, "456\t789\t789\tout2\t%x\n", build_log.HashCommand("command2"))
	f.Close()

	log := build_log.NewBuildLog()
	require.NoError(t, log.Load(testFilename))

	e := log.LookupByOutput("out")
	require.NotNil(t, e)
	assert.Equal(t, int64(123), e.StartTime)
	assert.Equal(t, int64(456), e.EndTime)
	assert.Equal(t, timestamp.TimeStamp(456), e.Mtime)
	assert.Equal(t, build_log.HashCommand("command"), e.CommandHash)

	e = log.LookupByOutput("out2")
	require.NotNil(t, e)
	assert.Equal(t, int64(456), e.StartTime)
	assert.Equal(t, int64(789), e.EndTime)
	assert.Equal(t, timestamp.TimeStamp(789), e.Mtime)
	assert.Equal(t, build_log.HashCommand("command2"), e.CommandHash)
}

type testDiskInterface struct{}

func (t *testDiskInterface) Stat(path string) (timestamp.TimeStamp, error) {
	return 4, nil
}

func (t *testDiskInterface) WriteFile(path string, contents []byte, clrfOnWindows bool) error {
	panic("should not be called")
}

func (t *testDiskInterface) MakeDir(path string) error {
	panic("should not be called")
}
func (t *testDiskInterface) MakeDirs(path string) error {
	panic("should not be called")
}

func (t *testDiskInterface) ReadFile(path string) ([]byte, error) {
	panic("should not be called")
}

func (t *testDiskInterface) RemoveFile(path string) int {
	panic("should not be called")
}

func TestRestat(t *testing.T) {
	os.Remove(testFilename)
	t.Cleanup(func() {
		os.Remove(testFilename)
	})

	f, err := os.Create(testFilename)
	require.NoError(t, err)
	fmt.Fprintf(f, "# ninja log v7\n")
	fmt.Fprintf(f, "1\t2\t3\tout\tcommand\n")
	f.Close()

	log := build_log.NewBuildLog()
	require.NoError(t, log.Load(testFilename))
	e := log.LookupByOutput("out")
	assert.Equal(t, timestamp.TimeStamp(3), e.Mtime)

	testDisk := &testDiskInterface{}
	filter := []string{"out2"}
	require.NoError(t, log.Restat(testFilename, testDisk, 1, filter))
	e = log.LookupByOutput("out")
	assert.Equal(t, timestamp.TimeStamp(3), e.Mtime) // unchanged, since the filter doesn't match

	require.NoError(t, log.Restat(testFilename, testDisk, 0, nil))
	e = log.LookupByOutput("out")
	assert.Equal(t, timestamp.TimeStamp(4), e.Mtime)
}

func TestVeryLongInputLine(t *testing.T) {
	os.Remove(testFilename)
	t.Cleanup(func() {
		os.Remove(testFilename)
	})

	// Ninja's build log buffer is currently 256kB. Lines longer than that are
	// silently ignored, but don't affect parsing of other lines.
	f, err := os.Create(testFilename)
	require.NoError(t, err)
	fmt.Fprintf(f, "# ninja log v7\n")
	fmt.Fprintf(f, "123\t456\t456\tout\tcommand start")
	for i := 0; i < (512<<10)/len(" more_command"); i++ {
		fmt.Fprint(f, " more_command")
	}
	fmt.Fprintf(f, "\n")
	fmt.Fprintf(f, "456\t789\t789\tout2\t%x\n", build_log.HashCommand("command2"))
	f.Close()

	log := build_log.NewBuildLog()
	require.NoError(t, log.Load(testFilename))

	e := log.LookupByOutput("out")
	assert.Nil(t, e)

	e = log.LookupByOutput("out2")
	require.NotNil(t, e)
	assert.Equal(t, int64(456), e.StartTime)
	assert.Equal(t, int64(789), e.EndTime)
	assert.Equal(t, timestamp.TimeStamp(789), e.Mtime)
	assert.Equal(t, build_log.HashCommand("command2"), e.CommandHash)
}

func TestMultiTargetEdge(t *testing.T) {
	os.Remove(testFilename)
	t.Cleanup(func() {
		os.Remove(testFilename)
	})

	s := state.New()
	test.AssertParse(t, `rule cat
  command = cat $in > $out
build out out.d: cat
`, s)

	log := build_log.NewBuildLog()
	log.RecordCommand(s.Edges()[0], 21, 22, 0)

	assert.Equal(t, 2, len(log.Entries()))
	e1 := log.LookupByOutput("out")
	require.NotNil(t, e1)
	e2 := log.LookupByOutput("out.d")
	require.NotNil(t, e2)
	assert.Equal(t, "out", e1.Output)
	assert.Equal(t, "out.d", e2.Output)
	assert.Equal(t, int64(21), e1.StartTime)
	assert.Equal(t, int64(21), e2.StartTime)
	assert.Equal(t, int64(22), e1.EndTime)
	assert.Equal(t, int64(22), e2.EndTime)
}

func TestRecompact(t *testing.T) {
	os.Remove(testFilename)
	t.Cleanup(func() {
		os.Remove(testFilename)
	})

	s := state.New()
	test.AssertParse(t, `rule cat
  command = cat $in > $out
build out: cat in
build out2: cat in
`, s)

	deadPathChecker := pathChecker(func(s string) bool { return s == "out2" })

	log1 := build_log.NewBuildLog()
	require.NoError(t, log1.OpenForWrite(testFilename, deadPathChecker))
	// Record the same edge several times, to trigger recompaction
	// the next time the log is opened.
	for i := 0; i < 200; i++ {
		log1.RecordCommand(s.Edges()[0], 15, int64(18+i), 0)
	}
	log1.RecordCommand(s.Edges()[1], 21, 22, 0)
	require.NoError(t, log1.Close())

	// Load...
	log2 := build_log.NewBuildLog()
	require.NoError(t, log2.Load(testFilename))
	assert.Equal(t, 2, len(log2.Entries()))
	assert.NotNil(t, log2.LookupByOutput("out"))
	assert.NotNil(t, log2.LookupByOutput("out2"))

	// ...and force a recompaction.
	require.NoError(t, log2.OpenForWrite(testFilename, deadPathChecker))
	require.NoError(t, log2.Close())

	// "out2" is dead, it should've been removed.
	log3 := build_log.NewBuildLog()
	require.NoError(t, log3.Load(testFilename))
	assert.Equal(t, 1, len(log3.Entries()))
	assert.NotNil(t, log3.LookupByOutput("out"))
	assert.Nil(t, log3.LookupByOutput("out2"))
}
