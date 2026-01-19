//go:build !windows

package util

import (
	"os"
	"syscall"
)

// ReplaceFileContent replaces the content of destPath with the content of
// tempPath by removing destPath and renaming tempPath to destPath.
// On Unix systems, it preserves the original file's uid and gid ownership.
func ReplaceFileContent(destPath, tempPath string) error {
	// Get the original file's uid and gid before removing it.
	var origUid, origGid int = -1, -1
	if info, err := os.Stat(destPath); err == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			origUid = int(stat.Uid)
			origGid = int(stat.Gid)
		}
	}

	if err := os.Remove(destPath); err != nil {
		return err
	}

	if err := os.Rename(tempPath, destPath); err != nil {
		return err
	}

	// Restore the original ownership if we captured it.
	if origUid != -1 && origGid != -1 {
		// Ignore errors from chown - we may not have permission to change
		// ownership (e.g., if we're not root), but the file replacement
		// itself succeeded.
		_ = os.Chown(destPath, origUid, origGid)
	}

	return nil
}
