package localcache_test

import (
	"flag"
	"testing"

	"github.com/buildbuddy-io/reninja/internal/localcache"
	"github.com/stretchr/testify/assert"

	repb "github.com/buildbuddy-io/reninja/genproto/remote_execution"
)

func makeDigest(hash string, size int64) *repb.Digest {
	return &repb.Digest{Hash: hash, SizeBytes: size}
}

// setTTL sets the local_existence_cache_ttl flag and restores the original
// value when the test completes.
func setTTL(t *testing.T, ttl string) {
	t.Helper()
	old := flag.Lookup("local_existence_cache_ttl").Value.String()
	flag.Set("local_existence_cache_ttl", ttl)
	t.Cleanup(func() { flag.Set("local_existence_cache_ttl", old) })
}

func TestContains_UnknownDigest(t *testing.T) {
	d := makeDigest("unknownhash", 100)
	assert.False(t, localcache.Contains(d))
}

func TestMarkFound_ContainsWithinTTL(t *testing.T) {
	setTTL(t, "1h")
	d := makeDigest("hashwithinttl", 100)
	localcache.MarkFound(d)
	assert.True(t, localcache.Contains(d))
}

func TestContains_ZeroTTL(t *testing.T) {
	setTTL(t, "0s")
	d := makeDigest("hashzerottl", 100)
	localcache.MarkFound(d)
	assert.False(t, localcache.Contains(d))
}

func TestMarkFound_DifferentDigestsAreIndependent(t *testing.T) {
	setTTL(t, "1h")
	d1 := makeDigest("hashindep1", 100)
	d2 := makeDigest("hashindep2", 200)
	localcache.MarkFound(d1)
	assert.True(t, localcache.Contains(d1))
	assert.False(t, localcache.Contains(d2))
}

func TestMarkFound_SameHashDifferentSizeDistinct(t *testing.T) {
	setTTL(t, "1h")
	d1 := makeDigest("samehash", 100)
	d2 := makeDigest("samehash", 200)
	localcache.MarkFound(d1)
	assert.True(t, localcache.Contains(d1))
	assert.False(t, localcache.Contains(d2))
}

func TestMarkFound_RefreshesExpiredEntry(t *testing.T) {
	setTTL(t, "0s")
	d := makeDigest("hashrefresh", 100)
	localcache.MarkFound(d)
	assert.False(t, localcache.Contains(d))

	// Re-mark with a longer TTL — entry should now be found.
	flag.Set("local_existence_cache_ttl", "1h")
	localcache.MarkFound(d)
	assert.True(t, localcache.Contains(d))
}
