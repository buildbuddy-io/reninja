package localcache

import (
	"flag"
	"sync"
	"time"

	"github.com/buildbuddy-io/reninja/internal/digest"

	repb "github.com/buildbuddy-io/reninja/genproto/remote_execution"
)

var (
	lastSeen sync.Map //map[digest.Key]int64

	localExistenceCacheTTL = flag.Duration("local_existence_cache_ttl", 5*time.Minute, "How long to cache blob existence locally")
)

func Contains(d *repb.Digest) bool {
	key := digest.NewKey(d)

	lastFound, ok := lastSeen.Load(key)
	if ok {
		lastFoundTime := time.UnixMilli(lastFound.(int64))
		if time.Since(lastFoundTime) < *localExistenceCacheTTL {
			return true
		}
		lastSeen.Delete(key)
	}
	return false
}

func MarkFound(d *repb.Digest) {
	key := digest.NewKey(d)
	lastSeen.Store(key, time.Now().UnixMilli())
}
