//go:build windows

package util

import (
	"os"
)

// ReplaceFileContent replaces the content of destPath with the content of
// tempPath by removing destPath and renaming tempPath to destPath.
// On Windows, file ownership is handled differently and is not preserved.
func ReplaceFileContent(destPath, tempPath string) error {
	if err := os.Remove(destPath); err != nil {
		return err
	}

	if err := os.Rename(tempPath, destPath); err != nil {
		return err
	}

	return nil
}
