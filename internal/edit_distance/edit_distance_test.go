package edit_distance_test

import (
	"testing"

	"github.com/buildbuddy-io/reninja/internal/edit_distance"
	"github.com/stretchr/testify/require"
)

func EditDistance(s1, s2 string) int {
	return edit_distance.EditDistance(s1, s2, true /*=allowReplacements*/, 0 /*=maxEditDistance*/)
}

func TestEmpty(t *testing.T) {
	require.Equal(t, 5, EditDistance("", "ninja"))
	require.Equal(t, 5, EditDistance("ninja", ""))
	require.Equal(t, 0, EditDistance("", ""))
}

func TestMaxDistance(t *testing.T) {
	const allowReplacements = true
	for maxDistance := 1; maxDistance < 7; maxDistance++ {
		require.Equal(t, maxDistance+1,
			edit_distance.EditDistance("abcdefghijklmnop", "ponmlkjihgfedcba",
				allowReplacements, maxDistance))
	}
}

func TestAllowReplacements(t *testing.T) {
	allowReplacements := true
	require.Equal(t, 1, edit_distance.EditDistance("ninja", "njnja", allowReplacements, 0))
	require.Equal(t, 1, edit_distance.EditDistance("njnja", "ninja", allowReplacements, 0))

	allowReplacements = false
	require.Equal(t, 2, edit_distance.EditDistance("ninja", "njnja", allowReplacements, 0))
	require.Equal(t, 2, edit_distance.EditDistance("njnja", "ninja", allowReplacements, 0))
}

func TestBasics(t *testing.T) {
	require.Equal(t, 0, EditDistance("browser_tests", "browser_tests"))
	require.Equal(t, 1, EditDistance("browser_test", "browser_tests"))
	require.Equal(t, 1, EditDistance("browser_tests", "browser_test"))
}
