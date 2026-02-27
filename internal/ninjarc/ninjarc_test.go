package ninjarc_test

import (
	"flag"
	"testing"
	"testing/fstest"

	"github.com/buildbuddy-io/reninja/internal/ninjarc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBasicDefaultRule(t *testing.T) {
	fsys := fstest.MapFS{
		"home/user/.ninjarc": &fstest.MapFile{
			Data: []byte("build --jobs=8\n"),
		},
	}
	rc, err := ninjarc.ParseRCFiles(fsys, "/workspace", "home/user/.ninjarc")
	require.NoError(t, err)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	jobs := fs.String("jobs", "", "")
	rc.Apply("build", "", fs)
	assert.Equal(t, "8", *jobs)
}

func TestNamedConfig(t *testing.T) {
	fsys := fstest.MapFS{
		"etc/.ninjarc": &fstest.MapFile{
			Data: []byte("build:local --remote_cache=grpc://localhost:1985\n"),
		},
	}
	rc, err := ninjarc.ParseRCFiles(fsys, "/workspace", "etc/.ninjarc")
	require.NoError(t, err)

	// Without config, the flag should not be set.
	fs1 := flag.NewFlagSet("test", flag.ContinueOnError)
	cache1 := fs1.String("remote_cache", "", "")
	rc.Apply("build", "", fs1)
	assert.Empty(t, *cache1)

	// With config=local, the flag should be set.
	fs2 := flag.NewFlagSet("test", flag.ContinueOnError)
	cache2 := fs2.String("remote_cache", "", "")
	rc.Apply("build", "local", fs2)
	assert.Equal(t, "grpc://localhost:1985", *cache2)
}

func TestMultiplePhases(t *testing.T) {
	fsys := fstest.MapFS{
		"rc": &fstest.MapFile{
			Data: []byte("build --jobs=8\nclean --keep_going\n"),
		},
	}
	rc, err := ninjarc.ParseRCFiles(fsys, "/workspace", "rc")
	require.NoError(t, err)

	// Build phase should get --jobs but not --keep_going.
	fsBuild := flag.NewFlagSet("test", flag.ContinueOnError)
	jobs := fsBuild.String("jobs", "", "")
	keepGoing := fsBuild.Bool("keep_going", false, "")
	rc.Apply("build", "", fsBuild)
	assert.Equal(t, "8", *jobs)
	assert.False(t, *keepGoing)

	// Clean phase should get --keep_going but not --jobs.
	fsClean := flag.NewFlagSet("test", flag.ContinueOnError)
	jobs2 := fsClean.String("jobs", "", "")
	keepGoing2 := fsClean.Bool("keep_going", false, "")
	rc.Apply("clean", "", fsClean)
	assert.Empty(t, *jobs2)
	assert.True(t, *keepGoing2)
}

func TestCommonPhase(t *testing.T) {
	fsys := fstest.MapFS{
		"rc": &fstest.MapFile{
			Data: []byte("common --verbose\n"),
		},
	}
	rc, err := ninjarc.ParseRCFiles(fsys, "/workspace", "rc")
	require.NoError(t, err)

	// Common rules should apply to any tool name.
	for _, tool := range []string{"build", "clean", "anything"} {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		verbose := fs.Bool("verbose", false, "")
		rc.Apply(tool, "", fs)
		assert.True(t, *verbose, "tool=%s", tool)
	}
}

func TestLineContinuation(t *testing.T) {
	fsys := fstest.MapFS{
		"rc": &fstest.MapFile{
			Data: []byte("build \\\n--jobs=8\n"),
		},
	}
	rc, err := ninjarc.ParseRCFiles(fsys, "/workspace", "rc")
	require.NoError(t, err)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	jobs := fs.String("jobs", "", "")
	rc.Apply("build", "", fs)
	assert.Equal(t, "8", *jobs)
}

func TestImportDirective(t *testing.T) {
	fsys := fstest.MapFS{
		"main.ninjarc": &fstest.MapFile{
			Data: []byte("import other.ninjarc\nbuild --jobs=4\n"),
		},
		"other.ninjarc": &fstest.MapFile{
			Data: []byte("build --verbose\n"),
		},
	}
	rc, err := ninjarc.ParseRCFiles(fsys, "/workspace", "main.ninjarc")
	require.NoError(t, err)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	jobs := fs.String("jobs", "", "")
	verbose := fs.Bool("verbose", false, "")
	rc.Apply("build", "", fs)
	assert.Equal(t, "4", *jobs)
	assert.True(t, *verbose)
}

func TestTryImportMissingFile(t *testing.T) {
	fsys := fstest.MapFS{
		"main.ninjarc": &fstest.MapFile{
			Data: []byte("try-import missing.ninjarc\nbuild --jobs=4\n"),
		},
	}
	rc, err := ninjarc.ParseRCFiles(fsys, "/workspace", "main.ninjarc")
	require.NoError(t, err)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	jobs := fs.String("jobs", "", "")
	rc.Apply("build", "", fs)
	assert.Equal(t, "4", *jobs)
}

func TestCircularImportDetection(t *testing.T) {
	fsys := fstest.MapFS{
		"a.ninjarc": &fstest.MapFile{
			Data: []byte("import b.ninjarc\n"),
		},
		"b.ninjarc": &fstest.MapFile{
			Data: []byte("import a.ninjarc\n"),
		},
	}
	_, err := ninjarc.ParseRCFiles(fsys, "/workspace", "a.ninjarc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circular import")
}

func TestConfigExpansion(t *testing.T) {
	fsys := fstest.MapFS{
		"rc": &fstest.MapFile{
			Data: []byte("build:dev --remote_cache=grpc://dev:1985\nbuild --jobs=4\n"),
		},
	}
	rc, err := ninjarc.ParseRCFiles(fsys, "/workspace", "rc")
	require.NoError(t, err)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	jobs := fs.String("jobs", "", "")
	cache := fs.String("remote_cache", "", "")
	rc.Apply("build", "dev", fs)
	assert.Equal(t, "4", *jobs)
	assert.Equal(t, "grpc://dev:1985", *cache)
}

func TestMultipleRCFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"first.ninjarc": &fstest.MapFile{
			Data: []byte("build --jobs=4\n"),
		},
		"second.ninjarc": &fstest.MapFile{
			Data: []byte("build --verbose\n"),
		},
	}
	rc, err := ninjarc.ParseRCFiles(fsys, "/workspace", "first.ninjarc", "second.ninjarc")
	require.NoError(t, err)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	jobs := fs.String("jobs", "", "")
	verbose := fs.Bool("verbose", false, "")
	rc.Apply("build", "", fs)
	assert.Equal(t, "4", *jobs)
	assert.True(t, *verbose)
}

func TestEmptyAndCommentLines(t *testing.T) {
	fsys := fstest.MapFS{
		"rc": &fstest.MapFile{
			Data: []byte("\n\nsingletoken\n\nbuild --jobs=8\n\n"),
		},
	}
	rc, err := ninjarc.ParseRCFiles(fsys, "/workspace", "rc")
	require.NoError(t, err)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	jobs := fs.String("jobs", "", "")
	rc.Apply("build", "", fs)
	assert.Equal(t, "8", *jobs)
}

func TestWorkspacePathExpansion(t *testing.T) {
	fsys := fstest.MapFS{
		"workspace/.ninjarc": &fstest.MapFile{
			Data: []byte("build --jobs=16\n"),
		},
	}
	rc, err := ninjarc.ParseRCFiles(fsys, "/workspace", "%workspace%/.ninjarc")
	require.NoError(t, err)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	jobs := fs.String("jobs", "", "")
	rc.Apply("build", "", fs)
	assert.Equal(t, "16", *jobs)
}

func TestConfigExpansionViaFlag(t *testing.T) {
	// When a default rule contains --config=dev, it should expand
	// the "dev" named config rules.
	fsys := fstest.MapFS{
		"rc": &fstest.MapFile{
			Data: []byte("build --config=dev\nbuild:dev --remote_cache=grpc://dev:1985\n"),
		},
	}
	rc, err := ninjarc.ParseRCFiles(fsys, "/workspace", "rc")
	require.NoError(t, err)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	config := fs.String("config", "", "")
	cache := fs.String("remote_cache", "", "")
	rc.Apply("build", "", fs)
	assert.Equal(t, "dev", *config)
	assert.Equal(t, "grpc://dev:1985", *cache)
}
