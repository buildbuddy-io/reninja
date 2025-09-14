package util

import (
	"path/filepath"
	"strings"
)

// CanonicalizePath normalizes a path to use forward slashes and returns slash bits
func CanonicalizePath(path string) (outp string, outs uint64) {
	if !strings.ContainsRune(path, '\\') {
		return filepath.Clean(path), 0
	}

	var slashBits uint64
	bit := uint64(1)
	result := strings.Builder{}
	result.Grow(len(path))

	for _, ch := range path {
		if ch == '\\' {
			result.WriteByte('/')
			slashBits |= bit
			bit <<= 1
		} else {
			result.WriteRune(ch)
			if ch == '/' {
				bit <<= 1
			}
		}
	}

	return filepath.Clean(result.String()), slashBits
}
