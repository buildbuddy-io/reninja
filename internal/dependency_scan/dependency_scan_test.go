package dependency_scan_test

import (
	"testing"

	"github.com/buildbuddy-io/gin/internal/dependency_scan"
	"github.com/buildbuddy-io/gin/internal/depfile_parser"
	"github.com/buildbuddy-io/gin/internal/disk"
	"github.com/buildbuddy-io/gin/internal/explanations"
	"github.com/buildbuddy-io/gin/internal/state"
	"github.com/buildbuddy-io/gin/internal/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMissingImplicit(t *testing.T) {
	s := state.New()
	fs := disk.NewMockDiskInterface()
	test.AddCatRule(t, s)
	test.AssertParse(t, "build out: cat in | implicit\n", s)

	require.NoError(t, fs.WriteFile("in", nil))
	require.NoError(t, fs.WriteFile("out", nil))

	opts := depfile_parser.DepfileParserOptions{}
	exp := explanations.NewOptional(nil)
	scan := dependency_scan.New(s, nil, nil, fs, opts, exp)
	require.NoError(t, scan.RecomputeDirty(s.GetNode("out"), nil))

	// A missing implicit dep *should* make the output dirty.
	// (In fact, a build will fail.)
	// This is a change from prior semantics of ninja.
	assert.True(t, s.GetNode("out").Dirty())
}

func TestModifiedImplicit(t *testing.T) {
	s := state.New()
	fs := disk.NewMockDiskInterface()
	test.AddCatRule(t, s)
	test.AssertParse(t, "build out: cat in | implicit\n", s)

	require.NoError(t, fs.WriteFile("in", nil))
	require.NoError(t, fs.WriteFile("out", nil))
	fs.Tick()
	require.NoError(t, fs.WriteFile("implicit", nil))

	opts := depfile_parser.DepfileParserOptions{}
	exp := explanations.NewOptional(nil)
	scan := dependency_scan.New(s, nil, nil, fs, opts, exp)
	require.NoError(t, scan.RecomputeDirty(s.GetNode("out"), nil))

	// A modified implicit dep should make the output dirty.
	assert.True(t, s.GetNode("out").Dirty())
}

