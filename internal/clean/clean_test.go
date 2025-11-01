package clean_test

import (
	"os"
	"testing"

	"github.com/buildbuddy-io/gin/internal/build_config"
	"github.com/buildbuddy-io/gin/internal/build_log"
	"github.com/buildbuddy-io/gin/internal/clean"
	"github.com/buildbuddy-io/gin/internal/disk"
	"github.com/buildbuddy-io/gin/internal/state"
	"github.com/buildbuddy-io/gin/internal/test"
	"github.com/buildbuddy-io/gin/internal/timestamp"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cleanTest struct {
	state  *state.State
	fs     *disk.MockDiskInterface
	config *build_config.Config
}

func (ct *cleanTest) IsPathDead(_ string) bool { return false }

func newStateTestWithBuiltinRules(t *testing.T) *cleanTest {
	t.Helper()

	s := state.New()
	test.AddCatRule(t, s)
	return &cleanTest{
		state: s,
		fs:    disk.NewMockDiskInterface(),
		config: &build_config.Config{
			Verbosity: build_config.Quiet,
		},
	}
}

func TestCleanAll(t *testing.T) {
	ct := newStateTestWithBuiltinRules(t)
	test.AssertParse(t, `
build in1: cat src1
build out1: cat in1
build in2: cat src2
build out2: cat in2
`, ct.state)
	ct.fs.Create("in1", []byte{})
	ct.fs.Create("out1", []byte{})
	ct.fs.Create("in2", []byte{})
	ct.fs.Create("out2", []byte{})

	cleaner := clean.NewCleaner(ct.state, ct.config, ct.fs)

	assert.Equal(t, 0, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 0, cleaner.CleanAll(false /*=generator*/))
	require.Equal(t, 4, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 4, len(ct.fs.FilesRemoved()))

	// Check they are removed
	mtime, _ := ct.fs.Stat("in1")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out1")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("in2")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out2")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
	ct.fs.ClearFilesRemoved()

	require.Equal(t, 0, cleaner.CleanAll(false /*=generator*/))
	require.Equal(t, 0, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 0, len(ct.fs.FilesRemoved()))
}

func TestCleanAllDryRun(t *testing.T) {
	ct := newStateTestWithBuiltinRules(t)
	test.AssertParse(t, `
build in1: cat src1
build out1: cat in1
build in2: cat src2
build out2: cat in2
`, ct.state)
	ct.fs.Create("in1", []byte{})
	ct.fs.Create("out1", []byte{})
	ct.fs.Create("in2", []byte{})
	ct.fs.Create("out2", []byte{})

	ct.config.DryRun = true
	cleaner := clean.NewCleaner(ct.state, ct.config, ct.fs)

	assert.Equal(t, 0, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 0, cleaner.CleanAll(false /*=generator*/))
	require.Equal(t, 4, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 0, len(ct.fs.FilesRemoved()))

	// Check they are not removed
	mtime, _ := ct.fs.Stat("in1")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out1")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("in2")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out2")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	ct.fs.ClearFilesRemoved()

	require.Equal(t, 0, cleaner.CleanAll(false /*=generator*/))
	require.Equal(t, 4, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 0, len(ct.fs.FilesRemoved()))
}

func TestCleanTarget(t *testing.T) {
	ct := newStateTestWithBuiltinRules(t)
	test.AssertParse(t, `
build in1: cat src1
build out1: cat in1
build in2: cat src2
build out2: cat in2
`, ct.state)
	ct.fs.Create("in1", []byte{})
	ct.fs.Create("out1", []byte{})
	ct.fs.Create("in2", []byte{})
	ct.fs.Create("out2", []byte{})

	cleaner := clean.NewCleaner(ct.state, ct.config, ct.fs)

	assert.Equal(t, 0, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 0, cleaner.CleanTargetByName("out1"))
	require.Equal(t, 2, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 2, len(ct.fs.FilesRemoved()))

	// Check they are removed
	mtime, _ := ct.fs.Stat("in1")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out1")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("in2")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out2")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	ct.fs.ClearFilesRemoved()

	require.Equal(t, 0, cleaner.CleanTargetByName("out1"))
	require.Equal(t, 0, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 0, len(ct.fs.FilesRemoved()))
}

func TestCleanTargetDryRun(t *testing.T) {
	ct := newStateTestWithBuiltinRules(t)
	test.AssertParse(t, `
build in1: cat src1
build out1: cat in1
build in2: cat src2
build out2: cat in2
`, ct.state)
	ct.fs.Create("in1", []byte{})
	ct.fs.Create("out1", []byte{})
	ct.fs.Create("in2", []byte{})
	ct.fs.Create("out2", []byte{})

	ct.config.DryRun = true
	cleaner := clean.NewCleaner(ct.state, ct.config, ct.fs)

	assert.Equal(t, 0, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 0, cleaner.CleanTargetByName("out1"))
	require.Equal(t, 2, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 0, len(ct.fs.FilesRemoved()))

	// Check they are not removed
	mtime, _ := ct.fs.Stat("in1")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out1")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("in2")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out2")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	ct.fs.ClearFilesRemoved()

	require.Equal(t, 0, cleaner.CleanTargetByName("out1"))
	require.Equal(t, 2, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 0, len(ct.fs.FilesRemoved()))
}

func TestCleanRule(t *testing.T) {
	ct := newStateTestWithBuiltinRules(t)
	test.AssertParse(t, `
rule cat_e
  command = cat -e $in > $out
build in1: cat_e src1
build out1: cat in1
build in2: cat_e src2
build out2: cat in2
`, ct.state)
	ct.fs.Create("in1", []byte{})
	ct.fs.Create("out1", []byte{})
	ct.fs.Create("in2", []byte{})
	ct.fs.Create("out2", []byte{})

	cleaner := clean.NewCleaner(ct.state, ct.config, ct.fs)

	assert.Equal(t, 0, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 0, cleaner.CleanRuleByName("cat_e"))
	require.Equal(t, 2, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 2, len(ct.fs.FilesRemoved()))

	// Check they are removed
	mtime, _ := ct.fs.Stat("in1")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out1")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("in2")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out2")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	ct.fs.ClearFilesRemoved()

	require.Equal(t, 0, cleaner.CleanRuleByName("cat_e"))
	require.Equal(t, 0, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 0, len(ct.fs.FilesRemoved()))
}

func TestCleanRuleDryRun(t *testing.T) {
	ct := newStateTestWithBuiltinRules(t)
	test.AssertParse(t, `
rule cat_e
  command = cat -e $in > $out
build in1: cat_e src1
build out1: cat in1
build in2: cat_e src2
build out2: cat in2
`, ct.state)
	ct.fs.Create("in1", []byte{})
	ct.fs.Create("out1", []byte{})
	ct.fs.Create("in2", []byte{})
	ct.fs.Create("out2", []byte{})

	ct.config.DryRun = true
	cleaner := clean.NewCleaner(ct.state, ct.config, ct.fs)

	assert.Equal(t, 0, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 0, cleaner.CleanRuleByName("cat_e"))
	require.Equal(t, 2, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 0, len(ct.fs.FilesRemoved()))

	// Check they are not removed
	mtime, _ := ct.fs.Stat("in1")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out1")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("in2")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out2")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	ct.fs.ClearFilesRemoved()

	require.Equal(t, 0, cleaner.CleanRuleByName("cat_e"))
	require.Equal(t, 2, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 0, len(ct.fs.FilesRemoved()))
}

func TestCleanRuleGenerator(t *testing.T) {
	ct := newStateTestWithBuiltinRules(t)
	test.AssertParse(t, `
rule regen
  command = cat $in > $out
  generator = 1
build out1: cat in1
build out2: regen in2
`, ct.state)
	ct.fs.Create("out1", []byte{})
	ct.fs.Create("out2", []byte{})

	cleaner := clean.NewCleaner(ct.state, ct.config, ct.fs)
	require.Equal(t, 0, cleaner.CleanAll(false /*=generator*/))
	require.Equal(t, 1, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 1, len(ct.fs.FilesRemoved()))

	ct.fs.Create("out1", []byte{})

	require.Equal(t, 0, cleaner.CleanAll(true /*=generator*/))
	require.Equal(t, 2, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 2, len(ct.fs.FilesRemoved()))
}

func TestCleanDepFile(t *testing.T) {
	ct := newStateTestWithBuiltinRules(t)
	test.AssertParse(t, `
rule cc
  command = cc $in > $out
  depfile = $out.d
build out1: cc in1
`, ct.state)
	ct.fs.Create("out1", []byte{})
	ct.fs.Create("out1.d", []byte{})

	cleaner := clean.NewCleaner(ct.state, ct.config, ct.fs)
	require.Equal(t, 0, cleaner.CleanAll(false /*=generator*/))
	require.Equal(t, 2, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 2, len(ct.fs.FilesRemoved()))
}

func TestCleanDepFileOnCleanTarget(t *testing.T) {
	ct := newStateTestWithBuiltinRules(t)
	test.AssertParse(t, `
rule cc
  command = cc $in > $out
  depfile = $out.d
build out1: cc in1
`, ct.state)
	ct.fs.Create("out1", []byte{})
	ct.fs.Create("out1.d", []byte{})

	cleaner := clean.NewCleaner(ct.state, ct.config, ct.fs)
	require.Equal(t, 0, cleaner.CleanTargetByName("out1"))
	require.Equal(t, 2, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 2, len(ct.fs.FilesRemoved()))
}

func TestCleanDepFileOnCleanRule(t *testing.T) {
	ct := newStateTestWithBuiltinRules(t)
	test.AssertParse(t, `
rule cc
  command = cc $in > $out
  depfile = $out.d
build out1: cc in1
`, ct.state)
	ct.fs.Create("out1", []byte{})
	ct.fs.Create("out1.d", []byte{})

	cleaner := clean.NewCleaner(ct.state, ct.config, ct.fs)
	require.Equal(t, 0, cleaner.CleanRuleByName("cc"))
	require.Equal(t, 2, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 2, len(ct.fs.FilesRemoved()))
}

func TestCleanDyndep(t *testing.T) {
	// Verify that a dyndep file can be loaded to discover a new output
	// to be cleaned.
	ct := newStateTestWithBuiltinRules(t)
	test.AssertParse(t, `
build out: cat in || dd
  dyndep = dd
`, ct.state)
	ct.fs.Create("in", []byte{})
	ct.fs.Create("dd", []byte(`ninja_dyndep_version = 1
build out | out.imp: dyndep
`))
	ct.fs.Create("out", []byte{})
	ct.fs.Create("out.imp", []byte{})

	cleaner := clean.NewCleaner(ct.state, ct.config, ct.fs)

	assert.Equal(t, 0, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 0, cleaner.CleanAll(false /*=generator*/))
	require.Equal(t, 2, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 2, len(ct.fs.FilesRemoved()))

	mtime, _ := ct.fs.Stat("out")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out.imp")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
}

func TestCleanDyndepMissing(t *testing.T) {
	// Verify that a missing dyndep file is tolerated.
	ct := newStateTestWithBuiltinRules(t)
	test.AssertParse(t, `
build out: cat in || dd
  dyndep = dd
`, ct.state)
	ct.fs.Create("in", []byte{})
	ct.fs.Create("out", []byte{})
	ct.fs.Create("out.imp", []byte{})

	cleaner := clean.NewCleaner(ct.state, ct.config, ct.fs)

	assert.Equal(t, 0, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 0, cleaner.CleanAll(false /*=generator*/))
	require.Equal(t, 1, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 1, len(ct.fs.FilesRemoved()))

	mtime, _ := ct.fs.Stat("out")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out.imp")
	require.Less(t, timestamp.TimeStampMissing, mtime)
}

func TestCleanRspFile(t *testing.T) {
	ct := newStateTestWithBuiltinRules(t)
	test.AssertParse(t, `
rule cc
  command = cc $in > $out
  rspfile = $rspfile
  rspfile_content=$in
build out1: cc in1
  rspfile = cc1.rsp
`, ct.state)
	ct.fs.Create("out1", []byte{})
	ct.fs.Create("cc1.rsp", []byte{})

	cleaner := clean.NewCleaner(ct.state, ct.config, ct.fs)
	require.Equal(t, 0, cleaner.CleanAll(false /*=generator*/))
	require.Equal(t, 2, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 2, len(ct.fs.FilesRemoved()))
}

func TestCleanRsp(t *testing.T) {
	ct := newStateTestWithBuiltinRules(t)
	test.AssertParse(t, `
rule cat_rsp
  command = cat $rspfile > $out
  rspfile = $rspfile
  rspfile_content = $in
build in1: cat src1
build out1: cat in1
build in2: cat_rsp src2
  rspfile=in2.rsp
build out2: cat_rsp in2
  rspfile=out2.rsp
`, ct.state)
	ct.fs.Create("in1", []byte{})
	ct.fs.Create("out1", []byte{})
	ct.fs.Create("in2.rsp", []byte{})
	ct.fs.Create("out2.rsp", []byte{})
	ct.fs.Create("in2", []byte{})
	ct.fs.Create("out2", []byte{})

	cleaner := clean.NewCleaner(ct.state, ct.config, ct.fs)
	assert.Equal(t, 0, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 0, cleaner.CleanTargetByName("out1"))
	require.Equal(t, 2, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 0, cleaner.CleanTargetByName("in2"))
	require.Equal(t, 2, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 0, cleaner.CleanRuleByName("cat_rsp"))
	require.Equal(t, 2, cleaner.TestingCleanedFilesCount())

	require.Equal(t, 6, len(ct.fs.FilesRemoved()))

	// Check they are removed
	mtime, _ := ct.fs.Stat("in1")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out1")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("in2")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out2")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("in2.rsp")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out2.rsp")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
}

func TestCleanFailure(t *testing.T) {
	ct := newStateTestWithBuiltinRules(t)
	test.AssertParse(t, "build dir: cat src1\n", ct.state)
	ct.fs.MakeDir("dir")
	cleaner := clean.NewCleaner(ct.state, ct.config, ct.fs)
	assert.NotEqual(t, 0, cleaner.CleanAll(false /*=generator*/))
}

func TestCleanPhony(t *testing.T) {
	ct := newStateTestWithBuiltinRules(t)
	test.AssertParse(t, `
build phony: phony t1 t2
build t1: cat
build t2: cat
`, ct.state)

	ct.fs.Create("phony", []byte{})
	ct.fs.Create("t1", []byte{})
	ct.fs.Create("t2", []byte{})

	// Check that CleanAll does not remove "phony"
	cleaner := clean.NewCleaner(ct.state, ct.config, ct.fs)
	require.Equal(t, 0, cleaner.CleanAll(false /*=generator*/))
	require.Equal(t, 2, cleaner.TestingCleanedFilesCount())
	mtime, _ := ct.fs.Stat("phony")
	require.Less(t, timestamp.TimeStampMissing, mtime)

	ct.fs.Create("t1", []byte{})
	ct.fs.Create("t2", []byte{})

	// Check that CleanTarget does not remove "phony"
	require.Equal(t, 0, cleaner.CleanTargetByName("phony"))
	require.Equal(t, 2, cleaner.TestingCleanedFilesCount())
	mtime, _ = ct.fs.Stat("phony")
	require.Less(t, timestamp.TimeStampMissing, mtime)
}

func TestCleanDepFileAndRspFileWithSpaces(t *testing.T) {
	ct := newStateTestWithBuiltinRules(t)
	test.AssertParse(t, `
rule cc_dep
  command = cc $in > $out
  depfile = $out.d
rule cc_rsp
  command = cc $in > $out
  rspfile = $out.rsp
  rspfile_content = $in
build out$ 1: cc_dep in$ 1
build out$ 2: cc_rsp in$ 1
`, ct.state)
	ct.fs.Create("out 1", []byte{})
	ct.fs.Create("out 2", []byte{})
	ct.fs.Create("out 1.d", []byte{})
	ct.fs.Create("out 2.rsp", []byte{})

	cleaner := clean.NewCleaner(ct.state, ct.config, ct.fs)
	require.Equal(t, 0, cleaner.CleanAll(false /*=generator*/))
	require.Equal(t, 4, cleaner.TestingCleanedFilesCount())
	require.Equal(t, 4, len(ct.fs.FilesRemoved()))

	mtime, _ := ct.fs.Stat("out 1")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out 2")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out 1.d")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out 2.rsp")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
}

const testFilename = "CleanTest-tempfile"

func TestCleanDead(t *testing.T) {
	os.Remove(testFilename)
	t.Cleanup(func() {
		os.Remove(testFilename)
	})

	state := state.New()
	ct := newStateTestWithBuiltinRules(t)

	test.AssertParse(t, `
rule cat
  command = cat $in > $out
build out1: cat in
build out2: cat in
`, state)
	test.AssertParse(t, `
build out2: cat in
`, ct.state)

	ct.fs.Create("in", []byte{})
	ct.fs.Create("out1", []byte{})
	ct.fs.Create("out2", []byte{})

	log1 := build_log.NewBuildLog()
	require.NoError(t, log1.OpenForWrite(testFilename, ct))
	log1.RecordCommand(state.Edges()[0], 15, 18, 0)
	log1.RecordCommand(state.Edges()[1], 20, 25, 0)
	log1.Close()

	log2 := build_log.NewBuildLog()
	require.NoError(t, log2.Load(testFilename))
	assert.Equal(t, 2, len(log2.Entries()))
	assert.NotNil(t, log2.LookupByOutput("out1"))
	assert.NotNil(t, log2.LookupByOutput("out2"))

	// First use the manifest that describes how to build out1
	cleaner1 := clean.NewCleaner(state, ct.config, ct.fs)
	require.Equal(t, 0, cleaner1.CleanDead(log2.Entries()))
	require.Equal(t, 0, cleaner1.TestingCleanedFilesCount())
	require.Equal(t, 0, len(ct.fs.FilesRemoved()))
	mtime, _ := ct.fs.Stat("in")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out1")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out2")
	require.Less(t, timestamp.TimeStampMissing, mtime)

	// Then use the manifest that does not build out1 anymore
	cleaner2 := clean.NewCleaner(ct.state, ct.config, ct.fs)
	require.Equal(t, 0, cleaner2.CleanDead(log2.Entries()))
	require.Equal(t, 1, cleaner2.TestingCleanedFilesCount())
	require.Equal(t, 1, len(ct.fs.FilesRemoved()))
	assert.Contains(t, ct.fs.FilesRemoved(), "out1")
	mtime, _ = ct.fs.Stat("in")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out1")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out2")
	require.Less(t, timestamp.TimeStampMissing, mtime)

	// Nothing to do now
	require.Equal(t, 0, cleaner2.CleanDead(log2.Entries()))
	require.Equal(t, 0, cleaner2.TestingCleanedFilesCount())
	require.Equal(t, 1, len(ct.fs.FilesRemoved()))
	assert.Contains(t, ct.fs.FilesRemoved(), "out1")
	mtime, _ = ct.fs.Stat("in")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out1")
	require.Equal(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out2")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	log2.Close()
}

func TestCleanDeadPreservesInputs(t *testing.T) {
	os.Remove(testFilename)
	t.Cleanup(func() {
		os.Remove(testFilename)
	})

	state := state.New()
	ct := newStateTestWithBuiltinRules(t)

	test.AssertParse(t, `
rule cat
  command = cat $in > $out
build out1: cat in
build out2: cat in
`, state)

	// This manifest does not build out1 anymore, but makes
	// it an implicit input. CleanDead should detect this
	// and preserve it.
	test.AssertParse(t, `
build out2: cat in | out1
`, ct.state)

	ct.fs.Create("in", []byte{})
	ct.fs.Create("out1", []byte{})
	ct.fs.Create("out2", []byte{})

	log1 := build_log.NewBuildLog()
	require.NoError(t, log1.OpenForWrite(testFilename, ct))
	log1.RecordCommand(state.Edges()[0], 15, 18, 0)
	log1.RecordCommand(state.Edges()[1], 20, 25, 0)
	log1.Close()

	log2 := build_log.NewBuildLog()
	require.NoError(t, log2.Load(testFilename))
	assert.Equal(t, 2, len(log2.Entries()))
	assert.NotNil(t, log2.LookupByOutput("out1"))
	assert.NotNil(t, log2.LookupByOutput("out2"))

	// First use the manifest that describes how to build out1
	cleaner1 := clean.NewCleaner(state, ct.config, ct.fs)
	require.Equal(t, 0, cleaner1.CleanDead(log2.Entries()))
	require.Equal(t, 0, cleaner1.TestingCleanedFilesCount())
	require.Equal(t, 0, len(ct.fs.FilesRemoved()))
	mtime, _ := ct.fs.Stat("in")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out1")
	require.Less(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out2")
	require.Less(t, timestamp.TimeStampMissing, mtime)

	// Then use the manifest that does not build out1 anymore
	cleaner2 := clean.NewCleaner(ct.state, ct.config, ct.fs)
	require.Equal(t, 0, cleaner2.CleanDead(log2.Entries()))
	require.Equal(t, 0, cleaner2.TestingCleanedFilesCount())
	require.Equal(t, 0, len(ct.fs.FilesRemoved()))

	mtime, _ = ct.fs.Stat("in")
	require.NotEqual(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out1")
	require.NotEqual(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out2")
	require.NotEqual(t, timestamp.TimeStampMissing, mtime)

	// Nothing to do now
	require.Equal(t, 0, cleaner2.CleanDead(log2.Entries()))
	require.Equal(t, 0, cleaner2.TestingCleanedFilesCount())
	require.Equal(t, 0, len(ct.fs.FilesRemoved()))
	mtime, _ = ct.fs.Stat("in")
	require.NotEqual(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out1")
	require.NotEqual(t, timestamp.TimeStampMissing, mtime)
	mtime, _ = ct.fs.Stat("out2")
	require.NotEqual(t, timestamp.TimeStampMissing, mtime)
	log2.Close()
}
