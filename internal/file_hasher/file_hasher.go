package file_hasher

import (
	"io/fs"
	"os"
	"sync"

	repb "github.com/buildbuddy-io/reninja/genproto/remote_execution"
	"github.com/buildbuddy-io/reninja/internal/digest"
)

var (
	mu         sync.Mutex // PROTECTS(fileHashes)
	fileHashes map[hashKey]digest.Key
)

func init() {
	fileHashes = make(map[hashKey]digest.Key, 0)
}

type hashKey struct {
	fileName   string
	mtimeNano  int64
	digestType repb.DigestFunction_Value
}

func makeKey(fileInfo fs.FileInfo, digestType repb.DigestFunction_Value) hashKey {
	return hashKey{
		fileName:   fileInfo.Name(),
		mtimeNano:  fileInfo.ModTime().UnixNano(),
		digestType: digestType,
	}
}

func HashFile(path string, digestType repb.DigestFunction_Value) (*repb.Digest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fileInfo, err := f.Stat()
	if err != nil {
		return nil, err
	}

	key := makeKey(fileInfo, digestType)
	mu.Lock()
	digestKey, ok := fileHashes[key]
	mu.Unlock()

	if ok {
		return digestKey.ToDigest(), nil
	}

	d, err := digest.Compute(f, digestType)
	if err == nil {
		mu.Lock()
		if _, ok := fileHashes[key]; !ok {
			fileHashes[key] = digest.NewKey(d)
		}
		mu.Unlock()
	}
	return d, err
}
