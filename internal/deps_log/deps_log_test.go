package deps_log_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/buildbuddy-io/gin/internal/deps_log"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/state"
	"github.com/buildbuddy-io/gin/internal/test"
	"github.com/buildbuddy-io/gin/internal/timestamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testFilename = "DepsLogTest-tempfile"
)

func TestWriteRead(t *testing.T) {
	os.Remove(testFilename)
	t.Cleanup(func() {
		os.Remove(testFilename)
	})

	log1 := deps_log.NewDepsLog()
	state1 := state.New()
	require.NoError(t, log1.OpenForWrite(testFilename))

	{
		deps := make([]*graph.Node, 0)
		deps = append(deps, state1.GetNode("foo.h"))
		deps = append(deps, state1.GetNode("bar.h"))
		log1.RecordDeps(state1.GetNode("out.o"), 1, deps)

		deps = deps[:0]
		deps = append(deps, state1.GetNode("foo.h"))
		deps = append(deps, state1.GetNode("bar2.h"))
		log1.RecordDeps(state1.GetNode("out2.o"), 2, deps)

		logDeps := log1.GetDeps(state1.GetNode("out.o"))
		require.NotNil(t, logDeps)
		assert.Equal(t, timestamp.TimeStamp(1), logDeps.Mtime)
		assert.Equal(t, 2, len(logDeps.Nodes))
		assert.Equal(t, "foo.h", logDeps.Nodes[0].Path())
		assert.Equal(t, "bar.h", logDeps.Nodes[1].Path())
	}

	require.NoError(t, log1.Close())

	state2 := state.New()
	log2 := deps_log.NewDepsLog()
	require.NoError(t, log2.Load(testFilename, state2))

	require.Equal(t, len(log1.Nodes()), len(log2.Nodes()))
	require.Equal(t, log1.Nodes(), log2.Nodes())

	logDeps := log2.GetDeps(state2.GetNode("out2.o"))
	require.NotNil(t, logDeps)
	assert.Equal(t, timestamp.TimeStamp(2), logDeps.Mtime)
	assert.Equal(t, 2, len(logDeps.Nodes))
	assert.Equal(t, "foo.h", logDeps.Nodes[0].Path())
	assert.Equal(t, "bar2.h", logDeps.Nodes[1].Path())
}

func TestLotsOfDeps(t *testing.T) {
	const numDeps = 100000

	os.Remove(testFilename)
	t.Cleanup(func() {
		os.Remove(testFilename)
	})

	log1 := deps_log.NewDepsLog()
	state1 := state.New()
	require.NoError(t, log1.OpenForWrite(testFilename))

	{
		deps := make([]*graph.Node, 0, numDeps)
		for i := 0; i < numDeps; i++ {
			buf := fmt.Sprintf("file%d.h", i)
			deps = append(deps, state1.GetNode(buf))
		}
		log1.RecordDeps(state1.GetNode("out.o"), 1, deps)

		logDeps := log1.GetDeps(state1.GetNode("out.o"))
		assert.Equal(t, numDeps, len(logDeps.Nodes))
	}

	require.NoError(t, log1.Close())

	state2 := state.New()
	log2 := deps_log.NewDepsLog()
	require.NoError(t, log2.Load(testFilename, state2))

	logDeps := log2.GetDeps(state2.GetNode("out.o"))
	assert.Equal(t, numDeps, len(logDeps.Nodes))
}

func TestDoubleEntry(t *testing.T) {
	os.Remove(testFilename)
	t.Cleanup(func() {
		os.Remove(testFilename)
	})

	var fileSize int64
	{
		st := state.New()
		log := deps_log.NewDepsLog()
		require.NoError(t, log.OpenForWrite(testFilename))

		deps := make([]*graph.Node, 0)
		deps = append(deps, st.GetNode("foo.h"))
		deps = append(deps, st.GetNode("bar.h"))
		log.RecordDeps(st.GetNode("out.o"), 1, deps)
		require.NoError(t, log.Close())

		stat, err := os.Stat(testFilename)
		require.NoError(t, err)
		fileSize = stat.Size()
		assert.Greater(t, fileSize, int64(0))
	}

	{
		st := state.New()
		log := deps_log.NewDepsLog()
		require.NoError(t, log.Load(testFilename, st))
		require.NoError(t, log.OpenForWrite(testFilename))

		deps := make([]*graph.Node, 0)
		deps = append(deps, st.GetNode("foo.h"))
		deps = append(deps, st.GetNode("bar.h"))
		log.RecordDeps(st.GetNode("out.o"), 1, deps)
		require.NoError(t, log.Close())

		stat, err := os.Stat(testFilename)
		require.NoError(t, err)
		fileSize2 := stat.Size()
		assert.Equal(t, fileSize, fileSize2)
	}
}

func TestRecompact(t *testing.T) {
	const manifest = `rule cc
  command = cc
  deps = gcc
build out.o: cc
build other_out.o: cc
`

	os.Remove(testFilename)
	t.Cleanup(func() {
		os.Remove(testFilename)
	})

	var fileSize int64
	{
		st := state.New()
		test.AssertParse(t, manifest, st)

		log := deps_log.NewDepsLog()
		require.NoError(t, log.OpenForWrite(testFilename))

		deps := make([]*graph.Node, 0)
		deps = append(deps, st.GetNode("foo.h"))
		deps = append(deps, st.GetNode("bar.h"))
		log.RecordDeps(st.GetNode("out.o"), 1, deps)

		deps = deps[:0]
		deps = append(deps, st.GetNode("foo.h"))
		deps = append(deps, st.GetNode("baz.h"))
		log.RecordDeps(st.GetNode("other_out.o"), 1, deps)

		require.NoError(t, log.Close())

		stat, err := os.Stat(testFilename)
		require.NoError(t, err)
		fileSize = stat.Size()
		assert.Greater(t, fileSize, int64(0))
	}

	var fileSize2 int64
	{
		st := state.New()
		test.AssertParse(t, manifest, st)

		log := deps_log.NewDepsLog()
		require.NoError(t, log.Load(testFilename, st))
		require.NoError(t, log.OpenForWrite(testFilename))

		deps := make([]*graph.Node, 0)
		deps = append(deps, st.GetNode("foo.h"))
		log.RecordDeps(st.GetNode("out.o"), 1, deps)
		require.NoError(t, log.Close())

		stat, err := os.Stat(testFilename)
		require.NoError(t, err)
		fileSize2 = stat.Size()
		assert.Greater(t, fileSize2, fileSize)
	}

	var fileSize3 int64
	{
		st := state.New()
		test.AssertParse(t, manifest, st)

		log := deps_log.NewDepsLog()
		require.NoError(t, log.Load(testFilename, st))

		out := st.GetNode("out.o")
		deps := log.GetDeps(out)
		require.NotNil(t, deps)
		assert.Equal(t, timestamp.TimeStamp(1), deps.Mtime)
		assert.Equal(t, 1, len(deps.Nodes))
		assert.Equal(t, "foo.h", deps.Nodes[0].Path())

		otherOut := st.GetNode("other_out.o")
		deps = log.GetDeps(otherOut)
		require.NotNil(t, deps)
		assert.Equal(t, timestamp.TimeStamp(1), deps.Mtime)
		assert.Equal(t, 2, len(deps.Nodes))
		assert.Equal(t, "foo.h", deps.Nodes[0].Path())
		assert.Equal(t, "baz.h", deps.Nodes[1].Path())

		require.NoError(t, log.Recompact(testFilename))

		deps = log.GetDeps(out)
		require.NotNil(t, deps)
		assert.Equal(t, timestamp.TimeStamp(1), deps.Mtime)
		assert.Equal(t, 1, len(deps.Nodes))
		assert.Equal(t, "foo.h", deps.Nodes[0].Path())
		nodes := log.Nodes()
		assert.Equal(t, out, nodes[out.ID()])

		deps = log.GetDeps(otherOut)
		require.NotNil(t, deps)
		assert.Equal(t, timestamp.TimeStamp(1), deps.Mtime)
		assert.Equal(t, 2, len(deps.Nodes))
		assert.Equal(t, "foo.h", deps.Nodes[0].Path())
		assert.Equal(t, "baz.h", deps.Nodes[1].Path())
		assert.Equal(t, otherOut, nodes[otherOut.ID()])

		stat, err := os.Stat(testFilename)
		require.NoError(t, err)
		fileSize3 = stat.Size()
		assert.Less(t, fileSize3, fileSize2)
	}

	{
		st := state.New()
		log := deps_log.NewDepsLog()
		require.NoError(t, log.Load(testFilename, st))

		out := st.GetNode("out.o")
		deps := log.GetDeps(out)
		require.NotNil(t, deps)
		assert.Equal(t, timestamp.TimeStamp(1), deps.Mtime)
		assert.Equal(t, 1, len(deps.Nodes))
		assert.Equal(t, "foo.h", deps.Nodes[0].Path())

		otherOut := st.GetNode("other_out.o")
		deps = log.GetDeps(otherOut)
		require.NotNil(t, deps)
		assert.Equal(t, timestamp.TimeStamp(1), deps.Mtime)
		assert.Equal(t, 2, len(deps.Nodes))
		assert.Equal(t, "foo.h", deps.Nodes[0].Path())
		assert.Equal(t, "baz.h", deps.Nodes[1].Path())

		require.NoError(t, log.Recompact(testFilename))

		deps = log.GetDeps(out)
		assert.Nil(t, deps)

		deps = log.GetDeps(otherOut)
		assert.Nil(t, deps)

		assert.Equal(t, -1, st.LookupNode("foo.h").ID())
		assert.Equal(t, -1, st.LookupNode("baz.h").ID())

		stat, err := os.Stat(testFilename)
		require.NoError(t, err)
		fileSize4 := stat.Size()
		assert.Less(t, fileSize4, fileSize3)
	}
}

func TestInvalidHeader(t *testing.T) {
	invalidHeaders := []string{
		"",
		"# ninjad",
		"# ninjadeps\n",
		"# ninjadeps\n\001\002",
		"# ninjadeps\n\001\002\003\004",
	}

	for i, header := range invalidHeaders {
		t.Run(fmt.Sprintf("header_%d", i), func(t *testing.T) {
			os.Remove(testFilename)
			t.Cleanup(func() {
				os.Remove(testFilename)
			})

			require.NoError(t, os.WriteFile(testFilename, []byte(header), 0644))

			log := deps_log.NewDepsLog()
			st := state.New()
			err := log.Load(testFilename, st)
			require.Error(t, err)
			require.Contains(t, "bad deps log signature or version; starting over", err.Error())
		})
	}
}

func TestTruncated(t *testing.T) {
	os.Remove(testFilename)
	t.Cleanup(func() {
		os.Remove(testFilename)
	})

	{
		st := state.New()
		log := deps_log.NewDepsLog()
		require.NoError(t, log.OpenForWrite(testFilename))

		deps := make([]*graph.Node, 0)
		deps = append(deps, st.GetNode("foo.h"))
		deps = append(deps, st.GetNode("bar.h"))
		log.RecordDeps(st.GetNode("out.o"), 1, deps)

		deps = deps[:0]
		deps = append(deps, st.GetNode("foo.h"))
		deps = append(deps, st.GetNode("bar2.h"))
		log.RecordDeps(st.GetNode("out2.o"), 2, deps)

		require.NoError(t, log.Close())
	}

	stat, err := os.Stat(testFilename)
	require.NoError(t, err)
	originalSize := stat.Size()

	nodeCount := 5
	depsCount := 2
	for size := originalSize; size > 0; size-- {
		err := os.Truncate(testFilename, size)
		require.NoError(t, err)

		st := state.New()
		log := deps_log.NewDepsLog()
		err = log.Load(testFilename, st)
		if err != nil {
			break
		}

		assert.GreaterOrEqual(t, nodeCount, len(log.Nodes()))
		nodeCount = len(log.Nodes())

		newDepsCount := 0
		for _, deps := range log.TestingGetDeps() {
			if deps != nil {
				newDepsCount++
			}
		}
		assert.GreaterOrEqual(t, depsCount, newDepsCount)
		depsCount = newDepsCount
	}
}

func TestTruncatedRecovery(t *testing.T) {
	os.Remove(testFilename)
	t.Cleanup(func() {
		os.Remove(testFilename)
	})

	{
		st := state.New()
		log := deps_log.NewDepsLog()
		require.NoError(t, log.OpenForWrite(testFilename))

		deps := make([]*graph.Node, 0)
		deps = append(deps, st.GetNode("foo.h"))
		deps = append(deps, st.GetNode("bar.h"))
		log.RecordDeps(st.GetNode("out.o"), 1, deps)

		deps = deps[:0]
		deps = append(deps, st.GetNode("foo.h"))
		deps = append(deps, st.GetNode("bar2.h"))
		log.RecordDeps(st.GetNode("out2.o"), 2, deps)

		require.NoError(t, log.Close())
	}

	{
		stat, err := os.Stat(testFilename)
		require.NoError(t, err)
		err = os.Truncate(testFilename, stat.Size()-2)
		require.NoError(t, err)
	}

	{
		st := state.New()
		log := deps_log.NewDepsLog()
		err := log.Load(testFilename, st)
		require.NoError(t, err)

		assert.Nil(t, log.GetDeps(st.GetNode("out2.o")))

		require.NoError(t, log.OpenForWrite(testFilename))

		deps := make([]*graph.Node, 0)
		deps = append(deps, st.GetNode("foo.h"))
		deps = append(deps, st.GetNode("bar2.h"))
		log.RecordDeps(st.GetNode("out2.o"), 3, deps)

		require.NoError(t, log.Close())
	}

	{
		st := state.New()
		log := deps_log.NewDepsLog()
		err := log.Load(testFilename, st)
		require.NoError(t, err)

		deps := log.GetDeps(st.GetNode("out2.o"))
		require.NotNil(t, deps)
	}
}

func TestReverseDepsNodes(t *testing.T) {
	os.Remove(testFilename)
	t.Cleanup(func() {
		os.Remove(testFilename)
	})

	st := state.New()
	log := deps_log.NewDepsLog()
	require.NoError(t, log.OpenForWrite(testFilename))

	deps := make([]*graph.Node, 0)
	deps = append(deps, st.GetNode("foo.h"))
	deps = append(deps, st.GetNode("bar.h"))
	log.RecordDeps(st.GetNode("out.o"), 1, deps)

	deps = deps[:0]
	deps = append(deps, st.GetNode("foo.h"))
	deps = append(deps, st.GetNode("bar2.h"))
	log.RecordDeps(st.GetNode("out2.o"), 2, deps)

	require.NoError(t, log.Close())

	revDeps := log.GetFirstReverseDepsNode(st.GetNode("foo.h"))
	assert.True(t, revDeps == st.GetNode("out.o") || revDeps == st.GetNode("out2.o"))

	revDeps = log.GetFirstReverseDepsNode(st.GetNode("bar.h"))
	assert.Equal(t, revDeps, st.GetNode("out.o"))
}

func TestMalformedDepsLog(t *testing.T) {
	const badLogFile = "DepsLogTest-corrupted.tempfile"

	t.Cleanup(func() {
		os.Remove(testFilename)
		os.Remove(badLogFile)
	})

	var originalContents []byte
	{
		st := state.New()
		log := deps_log.NewDepsLog()
		require.NoError(t, log.OpenForWrite(testFilename))

		deps := make([]*graph.Node, 0)
		deps = append(deps, st.GetNode("foo.hh"))
		deps = append(deps, st.GetNode("bar.hpp"))
		log.RecordDeps(st.GetNode("out.o"), 1, deps)
		require.NoError(t, log.Close())

		var err error
		originalContents, err = os.ReadFile(testFilename)
		require.NoError(t, err)
	}

	const versionOffset = 12
	assert.Equal(t, "# ninjadeps\n", string(originalContents[:versionOffset]))

	writeBadLogFile := func(badContents []byte) error {
		os.Remove(badLogFile)
		return os.WriteFile(badLogFile, badContents, 0644)
	}

	t.Run("corrupt_header", func(t *testing.T) {
		badContents := make([]byte, len(originalContents))
		copy(badContents, originalContents)
		badContents[0] = '@'

		require.NoError(t, writeBadLogFile(badContents))

		st := state.New()
		log := deps_log.NewDepsLog()
		err := log.Load(badLogFile, st)
		require.Error(t, err)
		require.Contains(t, "bad deps log signature or version; starting over", err.Error())
	})

	t.Run("truncate_version", func(t *testing.T) {
		badContents := originalContents[:versionOffset+3]
		require.NoError(t, writeBadLogFile(badContents))

		st := state.New()
		log := deps_log.NewDepsLog()
		err := log.Load(badLogFile, st)
		require.Error(t, err)
		require.Contains(t, "bad deps log signature or version; starting over", err.Error())
	})

	t.Run("truncate_first_record_size", func(t *testing.T) {
		badContents := originalContents[:versionOffset+4+3]
		require.NoError(t, writeBadLogFile(badContents))

		st := state.New()
		log := deps_log.NewDepsLog()
		err := log.Load(badLogFile, st)
		require.NoError(t, err)
	})

	t.Run("corrupt_first_record_size", func(t *testing.T) {
		badContents := make([]byte, len(originalContents))
		copy(badContents, originalContents)
		firstOffset := versionOffset + 4
		badContents[firstOffset+0] = 0x55
		badContents[firstOffset+1] = 0xaa
		badContents[firstOffset+2] = 0xff
		badContents[firstOffset+3] = 0xff

		require.NoError(t, writeBadLogFile(badContents))

		st := state.New()
		log := deps_log.NewDepsLog()
		err := log.Load(badLogFile, st)
		require.NoError(t, err)
	})

	t.Run("first_record_size_less_than_4", func(t *testing.T) {
		badContents := make([]byte, len(originalContents))
		copy(badContents, originalContents)
		firstOffset := versionOffset + 4
		badContents[firstOffset] = 0x01

		require.NoError(t, writeBadLogFile(badContents))

		st := state.New()
		log := deps_log.NewDepsLog()
		err := log.Load(badLogFile, st)
		require.NoError(t, err)
	})
}
