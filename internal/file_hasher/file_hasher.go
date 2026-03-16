package file_hasher

import (
	"os"
	"sync"
	"syscall"

	"github.com/buildbuddy-io/reninja/internal/digest"
	"github.com/djherbis/times"

	repb "github.com/buildbuddy-io/reninja/genproto/remote_execution"
)

var (
	fileHashes sync.Map //map[hashKey]digest.Key
)

type hashKey struct {
	dev        uint64
	inode      uint64
	ctimeNano  int64
	digestType int32
}

func HashFile(path string, digestType repb.DigestFunction_Value) (*repb.Digest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	t := times.Get(fi)

	var key hashKey
	stat, ok := fi.Sys().(*syscall.Stat_t)
	if ok {
		key = hashKey{
			dev:        stat.Dev,
			inode:      stat.Ino,
			ctimeNano:  t.ChangeTime().UnixNano(),
			digestType: int32(digestType),
		}

		if digestKey, ok := fileHashes.Load(key); ok {
			return digestKey.(digest.Key).ToDigest(), nil
		}
	}

	d, err := digest.Compute(f, digestType)
	if ok && err == nil {
		fileHashes.Store(key, digest.NewKey(d))
	}
	return d, err
}
