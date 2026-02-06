package project_root

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/buildbuddy-io/reninja/internal/remote_flags"
)

var (
	once             sync.Once
	cachedRoot       string
	cachedWorkingDir string
)

// Root returns the absolute path to the project root.
// It checks --project_root flag first, then auto-detects by walking up from
// CWD looking for .gclient or .git markers (outermost wins).
// Falls back to CWD if no marker is found.
func Root() string {
	once.Do(func() {
		if flagVal := remote_flags.ProjectRoot(); flagVal != "" {
			absPath, err := filepath.Abs(flagVal)
			if err != nil {
				absPath = flagVal
			}
			cachedRoot = absPath
		} else {
			cwd, err := os.Getwd()
			if err != nil {
				cachedRoot = "."
				cachedWorkingDir = "."
				return
			}
			cachedRoot = detectFrom(cwd)
		}
		cwd, err := os.Getwd()
		if err != nil {
			cachedWorkingDir = "."
			return
		}
		rel, err := filepath.Rel(cachedRoot, cwd)
		if err != nil {
			cachedWorkingDir = "."
			return
		}
		cachedWorkingDir = rel
	})
	return cachedRoot
}

// WorkingDirectory returns the relative path from the project root to the
// current working directory. This is used as Command.WorkingDirectory in REAPI.
func WorkingDirectory() string {
	Root() // ensure once.Do has run
	return cachedWorkingDir
}

// markers are the filenames we look for when detecting the project root.
var markers = []string{".gclient", ".git"}

// detectFrom walks from startDir up to the filesystem root, looking for
// marker files/directories. Returns the outermost directory containing a
// marker, or startDir if none found.
func detectFrom(startDir string) string {
	outermost := ""
	dir := startDir
	for {
		for _, marker := range markers {
			candidate := filepath.Join(dir, marker)
			if _, err := os.Stat(candidate); err == nil {
				outermost = dir
				break
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if outermost == "" {
		return startDir
	}
	return outermost
}
