package dyndep_parser_test

import (
	//"fmt"
	"testing"

	"github.com/buildbuddy-io/gin/internal/disk"
	"github.com/buildbuddy-io/gin/internal/dyndep_parser"
	"github.com/buildbuddy-io/gin/internal/state"
	"github.com/buildbuddy-io/gin/internal/test"
	"github.com/stretchr/testify/require"
)

func setUpState(t *testing.T) *state.State {
	t.Helper()

	s := state.New()
	test.AssertParse(t,
		`rule touch
  command = touch $out
build out otherout: touch
`, s)
	return s
}

func assertParse(t *testing.T, input string) {
	t.Helper()

	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest(input)
	require.NoError(t, err)
}

func TestEmpty(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("")
	require.Error(t, err)
	require.Contains(t, err.Error(), "input:1:1: expected 'ninja_dyndep_version = ...")
}

func TestVersion1(t *testing.T) {
	assertParse(t, "ninja_dyndep_version = 1\n")
}

func TestVersion1Extra(t *testing.T) {
	assertParse(t, "ninja_dyndep_version = 1-extra\n")
}

func TestVersion1_0(t *testing.T) {
	assertParse(t, "ninja_dyndep_version = 1.0\n")
}

func TestVersion1_0Extra(t *testing.T) {
	assertParse(t, "ninja_dyndep_version = 1.0-extra\n")
}

func TestCommentVersion(t *testing.T) {
	assertParse(t, "# comment\nninja_dyndep_version = 1\n")
}

func TestBlankLineVersion(t *testing.T) {
	assertParse(t, "\nninja_dyndep_version = 1\n")
}

func TestVersionCRLF(t *testing.T) {
	assertParse(t, "ninja_dyndep_version = 1\r\n")
}

func TestCommentVersionCRLF(t *testing.T) {
	assertParse(t, "# comment\r\nninja_dyndep_version = 1\r\n")
}

func TestBlankLineVersionCRLF(t *testing.T) {
	assertParse(t, "\r\nninja_dyndep_version = 1\r\n")
}

func TestVersionUnexpectedEOF(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1.0")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unexpected EOF")
	require.Contains(t, err.Error(), "ninja_dyndep_version = 1.0")
}

func TestUnsupportedVersion0(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 0\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported 'ninja_dyndep_version = 0'")
}

func TestUnsupportedVersion1_1(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1.1\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported 'ninja_dyndep_version = 1.1'")
}

func TestDuplicateVersion(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nninja_dyndep_version = 1\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unexpected identifier")
}

func TestMissingVersionOtherVar(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("not_ninja_dyndep_version = 1\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected 'ninja_dyndep_version = ...")
}

func TestMissingVersionBuild(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("build out: dyndep\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected 'ninja_dyndep_version = ...")
}

func TestUnexpectedEqual(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("= 1\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unexpected '='")
}

func TestUnexpectedIndent(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest(" = 1\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unexpected indent")
}

func TestOutDuplicate(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild out: dyndep\nbuild out: dyndep\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "multiple statements for 'out'")
}

func TestOutDuplicateThroughOther(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild out: dyndep\nbuild otherout: dyndep\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "multiple statements for 'otherout'")
}

func TestNoOutEOF(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unexpected EOF")
}

func TestNoOutColon(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild :\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected path")
}

func TestOutNoStatement(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild missing: dyndep\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no build statement exists for 'missing'")
}

func TestOutEOF(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild out")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unexpected EOF")
}

func TestOutNoRule(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild out:")
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected build command name 'dyndep'")
}

func TestOutBadRule(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild out: touch")
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected build command name 'dyndep'")
}

func TestBuildEOF(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild out: dyndep")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unexpected EOF")
}

func TestExplicitOut(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild out exp: dyndep\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "explicit outputs not supported")
}

func TestExplicitIn(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild out: dyndep exp\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "explicit inputs not supported")
}

func TestOrderOnlyIn(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild out: dyndep ||\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "order-only inputs not supported")
}

func TestBadBinding(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild out: dyndep\n  not_restat = 1\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "binding is not 'restat'")
}

func TestRestatTwice(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild out: dyndep\n  restat = 1\n  restat = 1\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unexpected indent")
}

func TestNoImplicit(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild out: dyndep\n")
	require.NoError(t, err)

	require.Equal(t, 1, len(dyndepFile))
	edge := s.Edges()[0]
	info, found := dyndepFile[edge]
	require.True(t, found)
	require.False(t, info.Restat)
	require.Len(t, info.ImplicitOutputs, 0)
	require.Len(t, info.ImplicitInputs, 0)
}

func TestEmptyImplicit(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild out | : dyndep |\n")
	require.NoError(t, err)

	require.Equal(t, 1, len(dyndepFile))
	edge := s.Edges()[0]
	info, found := dyndepFile[edge]
	require.True(t, found)
	require.False(t, info.Restat)
	require.Len(t, info.ImplicitOutputs, 0)
	require.Len(t, info.ImplicitInputs, 0)
}

func TestImplicitIn(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild out: dyndep | impin\n")
	require.NoError(t, err)

	require.Equal(t, 1, len(dyndepFile))
	edge := s.Edges()[0]
	info, found := dyndepFile[edge]
	require.True(t, found)
	require.False(t, info.Restat)
	require.Len(t, info.ImplicitOutputs, 0)
	require.Len(t, info.ImplicitInputs, 1)
	require.Equal(t, "impin", info.ImplicitInputs[0].Path())
}

func TestImplicitIns(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild out: dyndep | impin1 impin2\n")
	require.NoError(t, err)

	require.Equal(t, 1, len(dyndepFile))
	edge := s.Edges()[0]
	info, found := dyndepFile[edge]
	require.True(t, found)
	require.False(t, info.Restat)
	require.Len(t, info.ImplicitOutputs, 0)
	require.Len(t, info.ImplicitInputs, 2)
	require.Equal(t, "impin1", info.ImplicitInputs[0].Path())
	require.Equal(t, "impin2", info.ImplicitInputs[1].Path())
}

func TestImplicitOut(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild out | impout: dyndep\n")
	require.NoError(t, err)

	require.Equal(t, 1, len(dyndepFile))
	edge := s.Edges()[0]
	info, found := dyndepFile[edge]
	require.True(t, found)
	require.False(t, info.Restat)
	require.Len(t, info.ImplicitOutputs, 1)
	require.Equal(t, "impout", info.ImplicitOutputs[0].Path())
	require.Len(t, info.ImplicitInputs, 0)
}

func TestImplicitOuts(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild out | impout1 impout2 : dyndep\n")
	require.NoError(t, err)

	require.Equal(t, 1, len(dyndepFile))
	edge := s.Edges()[0]
	info, found := dyndepFile[edge]
	require.True(t, found)
	require.False(t, info.Restat)
	require.Len(t, info.ImplicitOutputs, 2)
	require.Equal(t, "impout1", info.ImplicitOutputs[0].Path())
	require.Equal(t, "impout2", info.ImplicitOutputs[1].Path())
	require.Len(t, info.ImplicitInputs, 0)
}

func TestImplicitInsAndOuts(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild out | impout1 impout2: dyndep | impin1 impin2\n")
	require.NoError(t, err)

	require.Equal(t, 1, len(dyndepFile))
	edge := s.Edges()[0]
	info, found := dyndepFile[edge]
	require.True(t, found)
	require.False(t, info.Restat)
	require.Len(t, info.ImplicitOutputs, 2)
	require.Equal(t, "impout1", info.ImplicitOutputs[0].Path())
	require.Equal(t, "impout2", info.ImplicitOutputs[1].Path())
	require.Len(t, info.ImplicitInputs, 2)
	require.Equal(t, "impin1", info.ImplicitInputs[0].Path())
	require.Equal(t, "impin2", info.ImplicitInputs[1].Path())
}

func TestRestat(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild out: dyndep\n  restat = 1\n")
	require.NoError(t, err)

	require.Equal(t, 1, len(dyndepFile))
	edge := s.Edges()[0]
	info, found := dyndepFile[edge]
	require.True(t, found)
	require.True(t, info.Restat)
	require.Len(t, info.ImplicitOutputs, 0)
	require.Len(t, info.ImplicitInputs, 0)
}

func TestOtherOutput(t *testing.T) {
	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()
	s := setUpState(t)

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild otherout: dyndep\n")
	require.NoError(t, err)

	require.Equal(t, 1, len(dyndepFile))
	edge := s.Edges()[0]
	info, found := dyndepFile[edge]
	require.True(t, found)
	require.False(t, info.Restat)
	require.Len(t, info.ImplicitOutputs, 0)
	require.Len(t, info.ImplicitInputs, 0)
}

func TestMultipleEdges(t *testing.T) {
	s := state.New()
	test.AssertParse(t,
		`rule touch
  command = touch $out
build out otherout: touch
build out2: touch
`, s)

	require.Len(t, s.Edges(), 2)
	require.Len(t, s.Edges()[1].Outputs(), 1)
	require.Equal(t, "out2", s.Edges()[1].Outputs()[0].Path())
	require.Len(t, s.Edges()[0].Inputs(), 0)

	fs := disk.NewMockDiskInterface()
	dyndepFile := dyndep_parser.NewDyndepFile()

	parser := dyndep_parser.New(s, fs, dyndepFile)
	err := parser.ParseTest("ninja_dyndep_version = 1\nbuild out: dyndep\nbuild out2: dyndep\n  restat = 1\n")
	require.NoError(t, err)

	require.Equal(t, 2, len(dyndepFile))

	edge0 := s.Edges()[0]
	info0, found := dyndepFile[edge0]
	require.True(t, found)
	require.False(t, info0.Restat)
	require.Len(t, info0.ImplicitOutputs, 0)
	require.Len(t, info0.ImplicitInputs, 0)

	edge1 := s.Edges()[1]
	info1, found := dyndepFile[edge1]
	require.True(t, found)
	require.True(t, info1.Restat)
	require.Len(t, info1.ImplicitOutputs, 0)
	require.Len(t, info1.ImplicitInputs, 0)
}
