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

func TestSelfReferentialConfigExpansion(t *testing.T) {
	// Config "loop" references itself via --config=loop. This should not
	// cause infinite recursion.
	fsys := fstest.MapFS{
		"rc": &fstest.MapFile{
			Data: []byte("build:loop --config=loop\nbuild:loop --jobs=4\n"),
		},
	}
	rc, err := ninjarc.ParseRCFiles(fsys, "/workspace", "rc")
	require.NoError(t, err)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	config := fs.String("config", "", "")
	jobs := fs.String("jobs", "", "")
	rc.Apply("build", "loop", fs)
	assert.Equal(t, "loop", *config)
	assert.Equal(t, "4", *jobs)
}

func TestMutuallyRecursiveConfigExpansion(t *testing.T) {
	// Config "a" references "b" and "b" references "a". This should not
	// cause infinite recursion.
	fsys := fstest.MapFS{
		"rc": &fstest.MapFile{
			Data: []byte("build:a --config=b\nbuild:a --jobs=1\nbuild:b --config=a\nbuild:b --jobs=2\n"),
		},
	}
	rc, err := ninjarc.ParseRCFiles(fsys, "/workspace", "rc")
	require.NoError(t, err)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	jobs := fs.String("jobs", "", "")
	config := fs.String("config", "", "")
	rc.Apply("build", "a", fs)
	// Should complete without hanging. The last --jobs write wins.
	assert.NotEmpty(t, *config)
	assert.NotEmpty(t, *jobs)
}

func TestDuplicateConfigExpansion(t *testing.T) {
	// Two default rules both reference --config=dev. The dev config
	// rules should only be expanded once.
	fsys := fstest.MapFS{
		"rc": &fstest.MapFile{
			Data: []byte("build --config=dev\nbuild --config=dev\nbuild:dev --jobs=4\n"),
		},
	}
	rc, err := ninjarc.ParseRCFiles(fsys, "/workspace", "rc")
	require.NoError(t, err)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	jobs := fs.String("jobs", "", "")
	config := fs.String("config", "", "")
	rc.Apply("build", "", fs)
	assert.Equal(t, "dev", *config)
	assert.Equal(t, "4", *jobs)
}

func TestCommonNamedConfig(t *testing.T) {
	// build:common is a special named config that should be applied to all
	// build invocations, similar to Bazel's behavior.
	fsys := fstest.MapFS{
		"rc": &fstest.MapFile{
			Data: []byte(
				"build:common --remote_header=x-buildbuddy-api-key=placeholder\n" +
					"build:remote --remote_executor=remote.buildbuddy.io\n" +
					"build:remote --remote_cache=remote.buildbuddy.io\n",
			),
		},
	}
	rc, err := ninjarc.ParseRCFiles(fsys, "/workspace", "rc")
	require.NoError(t, err)

	// Applying with config=remote should pick up build:common rules.
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	header := fs.String("remote_header", "", "")
	remoteExec := fs.String("remote_executor", "", "")
	remoteCache := fs.String("remote_cache", "", "")
	rc.Apply("build", "remote", fs)
	assert.Equal(t, "x-buildbuddy-api-key=placeholder", *header)
	assert.Equal(t, "remote.buildbuddy.io", *remoteExec)
	assert.Equal(t, "remote.buildbuddy.io", *remoteCache)

	// Applying with no explicit config should also pick up build:common rules.
	fs2 := flag.NewFlagSet("test", flag.ContinueOnError)
	header2 := fs2.String("remote_header", "", "")
	rc.Apply("build", "", fs2)
	assert.Equal(t, "x-buildbuddy-api-key=placeholder", *header2)

	// build:common should NOT apply when the tool is not "build".
	fs3 := flag.NewFlagSet("test", flag.ContinueOnError)
	header3 := fs3.String("remote_header", "", "")
	rc.Apply("clean", "", fs3)
	assert.Empty(t, *header3)
}

func TestApplyPreservesPositionalArgs(t *testing.T) {
	fsys := fstest.MapFS{
		"rc": &fstest.MapFile{
			Data: []byte("build --jobs=4\n"),
		},
	}
	rc, err := ninjarc.ParseRCFiles(fsys, "/workspace", "rc")
	require.NoError(t, err)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	jobs := fs.String("jobs", "", "")
	// Simulate a prior parse that left positional args behind.
	fs.Parse([]string{"--jobs=8", "target1", "target2"})
	assert.Equal(t, []string{"target1", "target2"}, fs.Args())

	rc.Apply("build", "", fs)
	assert.Equal(t, "4", *jobs)
	assert.Equal(t, []string{"target1", "target2"}, fs.Args())
}
