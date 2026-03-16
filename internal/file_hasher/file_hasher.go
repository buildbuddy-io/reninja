package file_hasher

import (
	"os"
	"sync"

	"github.com/buildbuddy-io/reninja/internal/digest"
	"github.com/djherbis/times"

	repb "github.com/buildbuddy-io/reninja/genproto/remote_execution"
)

var (
	fileHashes sync.Map //map[hashKey]digest.Key
)

type hashKey struct {
	fileName   string
	mtimeNano  int64
	ctimeNano  int64
	digestType repb.DigestFunction_Value
}

func HashFile(path string, digestType repb.DigestFunction_Value) (*repb.Digest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	t, err := times.StatFile(f)
	if err != nil {
		return nil, err
	}

	key := hashKey{
		fileName:   path,
		mtimeNano:  t.ModTime().UnixNano(),
		ctimeNano:  t.ChangeTime().UnixNano(),
		digestType: digestType,
	}

	if digestKey, ok := fileHashes.Load(key); ok {
		return digestKey.(digest.Key).ToDigest(), nil
	}

	d, err := digest.Compute(f, digestType)
	if err == nil {
		fileHashes.Store(key, digest.NewKey(d))
	}
	return d, err
}
