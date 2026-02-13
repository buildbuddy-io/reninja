package project_root

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/buildbuddy-io/reninja/internal/remote_flags"
)

// markers are the filenames we look for when detecting the project root.
var markers = []string{".gclient", ".git"}

// walkUpDirsToFindRoot walks from startDir up to the filesystem root,
// looking for marker files/directories. The *outermost* directory that contains
// a marker will be retured, or startDir if no markers were found.
func WalkUpDirsToFindRoot(startDir string) string {
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

func flagOrRoot(cwd string) string {
	if flagVal := remote_flags.ProjectRoot(); flagVal != "" {
		absPath, err := filepath.Abs(flagVal)
		if err != nil {
			return "."
		}
		return absPath
	}
	return WalkUpDirsToFindRoot(cwd)
}

// Root returns the absolute path to the project root.
// It checks --project_root flag first, then auto-detects by walking up from
// CWD looking for .gclient or .git markers (outermost wins).
// Falls back to CWD if no marker is found.
func Root() string {
	root := sync.OnceValue(func() string {
		cwd, err := os.Getwd()
		if err != nil {
			return "."
		}
		return flagOrRoot(cwd)
	})
	return root()
}

// skipEntry returns true for directories and files that should never be
// included in remote uploads (e.g. .git/ directories, .ninja_* metadata).
func skipEntry(name string, isDir bool) bool {
	if isDir {
		return name == ".git"
	}
	return strings.HasPrefix(name, ".ninja_")
}

// WalkFiles walks the project root directory and returns absolute paths of all
// files, excluding .git/ directories and .ninja_* metadata files. This is used
// to upload the entire project root for remote execution of commands whose
// inputs can't be statically determined.
func WalkFiles() ([]string, error) {
	root := Root()
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if skipEntry(d.Name(), d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// WorkingDirectory returns the relative path from the project root to the
// current working directory. This is used as Command.WorkingDirectory in REAPI.
func WorkingDirectory() string {
	workingDir := sync.OnceValue(func() string {
		cwd, err := os.Getwd()
		if err != nil {
			return "."
		}
		root := flagOrRoot(cwd)
		rel, err := filepath.Rel(root, cwd)
		if err != nil {
			return "."
		}
		return rel
	})
	wd := workingDir()
	if wd == "." {
		return ""
	}
	return wd
}
