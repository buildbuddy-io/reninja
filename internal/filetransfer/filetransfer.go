package filetransfer

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/buildbuddy-io/gin/internal/cachetools"
	"github.com/buildbuddy-io/gin/internal/digest"
	"github.com/buildbuddy-io/gin/internal/grpc_client"
	"github.com/buildbuddy-io/gin/internal/remote_flags"
	"github.com/buildbuddy-io/gin/internal/remote_headers"
	"github.com/buildbuddy-io/gin/internal/statuserr"
	"github.com/buildbuddy-io/gin/internal/util"
	"google.golang.org/grpc/metadata"

	repb "github.com/buildbuddy-io/gin/genproto/remote_execution"
	bspb "google.golang.org/genproto/googleapis/bytestream"
)

const (
	DigestFunction = repb.DigestFunction_BLAKE3
)

var (
	once              sync.Once
	defaultUploader   *Uploader
	defaultDownloader *Downloader
)

func initializeClients() {
	once.Do(func() {
		if remote_flags.RemoteCache() == "" {
			return
		}
		conn, err := grpc_client.DialSimple(context.TODO(), remote_flags.RemoteCache())
		if err != nil {
			util.Errorf("error dialing remote cache: %s", err)
			return
		}
		bsClient := bspb.NewByteStreamClient(conn)
		casClient := repb.NewContentAddressableStorageClient(conn)
		acClient := repb.NewActionCacheClient(conn)
		defaultUploader = &Uploader{bsClient, casClient, acClient}
		defaultDownloader = &Downloader{bsClient, casClient, acClient}
	})
}

func DefaultUploader() *Uploader {
	initializeClients()
	return defaultUploader
}

func DefaultDownloader() *Downloader {
	initializeClients()
	return defaultDownloader
}

type Uploader struct {
	bsClient  bspb.ByteStreamClient
	casClient repb.ContentAddressableStorageClient
	acClient  repb.ActionCacheClient
}

func appendHeadersToCtx(ctx context.Context) context.Context {
	extraHeaders := remote_headers.GetPairs()
	if len(extraHeaders) == 0 {
		return ctx
	}
	ctx = metadata.AppendToOutgoingContext(ctx, extraHeaders...)
	return ctx
}

func (u *Uploader) UploadActionResult(ctx context.Context, r *digest.ACResourceName, ar *repb.ActionResult) error {
	ctx = appendHeadersToCtx(ctx)
	return cachetools.UploadActionResult(ctx, u.acClient, r, ar)
}

func (u *Uploader) UploadInMemoryBlob(ctx context.Context, in io.ReadSeeker) (*digest.CASResourceName, error) {
	ctx = appendHeadersToCtx(ctx)
	instanceName := remote_flags.RemoteInstanceName()
	d, err := cachetools.UploadBlob(ctx, u.bsClient, instanceName, DigestFunction, in)
	if err != nil {
		return nil, err
	}
	return digest.NewCASResourceName(d, instanceName, DigestFunction), nil
}

func (u *Uploader) UploadFile(ctx context.Context, path string) (*digest.CASResourceName, error) {
	ctx = appendHeadersToCtx(ctx)
	instanceName := remote_flags.RemoteInstanceName()
	d, err := cachetools.UploadFile(ctx, u.bsClient, instanceName, DigestFunction, path)
	if err != nil {
		return nil, err
	}
	return digest.NewCASResourceName(d, instanceName, DigestFunction), nil
}

func cleanPaths(dirty []string) ([]string, error) {
	cleanedFiles := make([]string, len(dirty))
	for i, dirtyPath := range dirty {
		if !filepath.IsAbs(dirtyPath) {
			cleaned, err := filepath.Abs(dirtyPath)
			if err != nil {
				return nil, err
			}
			cleanedFiles[i] = cleaned
		} else {
			cleanedFiles[i] = filepath.Clean(dirtyPath)
		}
	}
	return cleanedFiles, nil
}

// UploadDirectoryToCAS uploads all the files in a given directory to the CAS
// as well as the directory structure, and returns the digest of the root
// Directory proto that can be used to fetch the uploaded contents.
func (u Uploader) UploadDirectoryToCAS(ctx context.Context, files []string) (*repb.Digest, *repb.Digest, error) {
	cleanedFiles, err := cleanPaths(files)
	if err != nil {
		return nil, nil, err
	}

	ul := cachetools.NewBatchCASUploader(ctx, u.bsClient, u.casClient, remote_flags.RemoteInstanceName(), DigestFunction)

	rootDirPath := "/"
	pathsToUpload := map[string]struct{}{
		rootDirPath: {},
	}
	for _, path := range cleanedFiles {
		start := 0
		i := strings.IndexRune(path, filepath.Separator)
		for ; i >= 0; i = strings.IndexRune(path[start:], filepath.Separator) {
			subPath := path[0 : start+i]
			pathsToUpload[subPath] = struct{}{}
			start += i + 1
		}
		pathsToUpload[path] = struct{}{}
	}

	// Recursively find and upload all descendant dirs.
	visited, rootDirectoryDigest, err := uploadDir(ul, rootDirPath, pathsToUpload, nil /*=visited*/)
	if err != nil {
		return nil, nil, err
	}
	if len(visited) == 0 {
		return nil, nil, statuserr.InternalError("empty directory list after uploading directory tree; this should never happen")
	}
	// Upload the tree, which consists of the root dir as well as all descendant
	// dirs.
	rootTree := &repb.Tree{Root: visited[0], Children: visited[1:]}
	treeDigest, err := ul.UploadProto(rootTree)
	if err != nil {
		return nil, nil, err
	}
	if err := ul.Wait(); err != nil {
		return nil, nil, err
	}
	return rootDirectoryDigest, treeDigest, nil
}

func uploadDir(ul *cachetools.BatchCASUploader, dirPath string, pathsToUpload map[string]struct{}, visited []*repb.Directory) ([]*repb.Directory, *repb.Digest, error) {
	dir := &repb.Directory{}
	// Append the directory before doing any other work, so that the root
	// directory is located at visited[0] at the end of recursion.
	visited = append(visited, dir)
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(dirPath, name)

		if _, ok := pathsToUpload[path]; !ok {
			continue
		}

		if entry.IsDir() {
			var d *repb.Digest
			visited, d, err = uploadDir(ul, path, pathsToUpload, visited)
			if err != nil {
				return nil, nil, err
			}
			dir.Directories = append(dir.Directories, &repb.DirectoryNode{
				Name:   name,
				Digest: d,
			})
		} else if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return nil, nil, err
			}
			d, err := ul.UploadFile(path)
			if err != nil {
				return nil, nil, err
			}
			dir.Files = append(dir.Files, &repb.FileNode{
				Name:         name,
				Digest:       d,
				IsExecutable: isExecutable(info),
			})
		} else if entry.Type()&os.ModeSymlink == os.ModeSymlink {
			target, err := os.Readlink(path)
			if err != nil {
				return nil, nil, err
			}
			dir.Symlinks = append(dir.Symlinks, &repb.SymlinkNode{
				Name:   name,
				Target: target,
			})
		}
	}
	digest, err := ul.UploadProto(dir)
	if err != nil {
		return nil, nil, err
	}
	return visited, digest, nil
}

func isExecutable(info os.FileInfo) bool {
	return info.Mode()&0100 != 0
}

type Downloader struct {
	bsClient  bspb.ByteStreamClient
	casClient repb.ContentAddressableStorageClient
	acClient  repb.ActionCacheClient
}

func (d *Downloader) GetActionResult(ctx context.Context, ar *digest.ACResourceName) (*repb.ActionResult, error) {
	ctx = appendHeadersToCtx(ctx)
	return cachetools.GetActionResult(ctx, d.acClient, ar)
}

func (d *Downloader) GetBlob(ctx context.Context, r *digest.CASResourceName, out io.Writer) error {
	ctx = appendHeadersToCtx(ctx)
	r.SetCompressor(repb.Compressor_ZSTD)
	return cachetools.GetBlob(ctx, d.bsClient, r, out)
}
