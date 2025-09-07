package version

import (
	"log"
	"strconv"
	"strings"
)

const NinjaVersion = "1.14.0.git"

// TODO(tylerw): use semver or something
// Returns (major, minor)
func ParseVersion(version string) (int, int) {
	parts := strings.Split(version, ".")
	major, _ := strconv.Atoi(parts[0])
	minor := 0

	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	return major, minor
}

func CheckNinjaVersion(fileVersion string) {
	binMajor, binMinor := ParseVersion(NinjaVersion)
	fileMajor, fileMinor := ParseVersion(fileVersion)

	if binMajor > fileMajor {
		log.Printf("ninja executable version (%s) greater than build file "+
			"ninja_required_version (%s); versions may be incompatible.",
			NinjaVersion, fileVersion)
	}
	if (binMajor == fileMajor && binMinor < fileMinor) || binMajor < fileMajor {
		log.Fatalf("ninja executable version (%s) greater than build file "+
			"ninja_required_version (%s); versions may be incompatible.",
			NinjaVersion, fileVersion)
	}
}
