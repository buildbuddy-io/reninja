package version

import (
	"fmt"
	"strings"

	"github.com/buildbuddy-io/reninja/internal/util"
)

const NinjaVersion = "1.14.0.git"

func find(haystack, needle string, startOffset int) int {
	h := haystack[startOffset:]
	e := strings.Index(h, needle)
	if e == -1 {
		return len(haystack)
	}
	return e + startOffset
}

// TODO(tylerw): use semver or something?
// Returns (major, minor)
func ParseVersion(version string) (int, int) {
	end := find(version, ".", 0)
	var major, minor int

	// can't use stronv.Atoi because it's stricter than
	// cpp atoi.

	_, _ = fmt.Sscan(version[0:end], &major)
	if end != len(version) {
		start := end + 1
		end = find(version, ".", start)
		_, _ = fmt.Sscan(version[start:end], &minor)
	}
	return major, minor
}

func CheckNinjaVersion(fileVersion string) {
	binMajor, binMinor := ParseVersion(NinjaVersion)
	fileMajor, fileMinor := ParseVersion(fileVersion)

	if binMajor > fileMajor {
		util.Warningf("ninja executable version (%s) greater than build file "+
			"ninja_required_version (%s); versions may be incompatible.",
			NinjaVersion, fileVersion)
	}
	if (binMajor == fileMajor && binMinor < fileMinor) || binMajor < fileMajor {
		util.Fatalf("ninja executable version (%s) greater than build file "+
			"ninja_required_version (%s); versions may be incompatible.",
			NinjaVersion, fileVersion)
	}
}
