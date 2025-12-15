package filetransfer

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
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
	"google.golang.org/protobuf/proto"

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
	bspb.ByteStreamClient
	repb.ContentAddressableStorageClient
	repb.ActionCacheClient
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
	return cachetools.UploadActionResult(ctx, u, r, ar)
}

func (u *Uploader) UploadInMemoryBlob(ctx context.Context, in io.ReadSeeker) (*digest.CASResourceName, error) {
	ctx = appendHeadersToCtx(ctx)
	instanceName := remote_flags.RemoteInstanceName()
	d, err := cachetools.UploadBlob(ctx, u, instanceName, DigestFunction, in)
	if err != nil {
		return nil, err
	}
	return digest.NewCASResourceName(d, instanceName, DigestFunction), nil
}

func (u *Uploader) UploadProto(ctx context.Context, in proto.Message) (*digest.CASResourceName, error) {
	ctx = appendHeadersToCtx(ctx)
	instanceName := remote_flags.RemoteInstanceName()
	d, err := cachetools.UploadProto(ctx, u, instanceName, DigestFunction, in)
	if err != nil {
		return nil, err
	}
	return digest.NewCASResourceName(d, instanceName, DigestFunction), nil
}

func (u *Uploader) UploadFile(ctx context.Context, path string) (*digest.CASResourceName, error) {
	ctx = appendHeadersToCtx(ctx)
	instanceName := remote_flags.RemoteInstanceName()
	d, err := cachetools.UploadFile(ctx, u, instanceName, DigestFunction, path)
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

func expandTree(cleanedFiles []string) []string {
	paths := make(map[string]struct{}, len(cleanedFiles))
	for _, path := range cleanedFiles {
		start := 0
		i := strings.IndexRune(path, filepath.Separator)
		for ; i >= 0; i = strings.IndexRune(path[start:], filepath.Separator) {
			subPath := path[0 : start+i]
			if subPath != "" {
				paths[subPath] = struct{}{}
			}
			start += i + 1
		}
		paths[path] = struct{}{}
	}

	pathSet := slices.Sorted(maps.Keys(paths))
	sepString := string(filepath.Separator)

	// Sort paths by depth, increasing.
	slices.SortFunc(pathSet, func(a, b string) int {
		return cmp.Compare(strings.Count(a, sepString), strings.Count(b, sepString))
	})
	return pathSet
}

func (u Uploader) HashDirectoryTree(ctx context.Context, files []string) (*digest.CASResourceName, *digest.CASResourceName, error) {
	cleanedFiles, err := cleanPaths(files)
	if err != nil {
		return nil, nil, err
	}

	pathsToUpload := expandTree(cleanedFiles)
	// Recursively find and upload all descendant dirs.
	visited, rootDirectoryDigest, err := uploadDir(nil, pathsToUpload, nil /*=visited*/)
	if err != nil {
		return nil, nil, err
	}
	if len(visited) == 0 {
		return nil, nil, statuserr.InternalError("empty directory list after uploading directory tree; this should never happen")
	}
	// Upload the tree, which consists of the root dir as well as all descendant
	// dirs.
	rootTree := &repb.Tree{Root: visited[0], Children: visited[1:]}
	treeDigest, err := digest.ComputeForMessage(rootTree, DigestFunction)
	if err != nil {
		return nil, nil, err
	}
	instanceName := remote_flags.RemoteInstanceName()
	return digest.NewCASResourceName(rootDirectoryDigest, instanceName, DigestFunction), digest.NewCASResourceName(treeDigest, instanceName, DigestFunction), nil
}

// UploadDirectoryToCAS uploads all the files in a given directory to the CAS
// as well as the directory structure, and returns the digest of the root
// Directory proto that can be used to fetch the uploaded contents.
func (u Uploader) UploadDirectoryToCAS(ctx context.Context, files []string) (*digest.CASResourceName, *digest.CASResourceName, error) {
	cleanedFiles, err := cleanPaths(files)
	if err != nil {
		return nil, nil, err
	}
	pathsToUpload := expandTree(cleanedFiles)

	instanceName := remote_flags.RemoteInstanceName()
	ul := cachetools.NewBatchCASUploader(ctx, u, u, instanceName, DigestFunction)

	// Recursively find and upload all descendant dirs.
	visited, rootDirectoryDigest, err := uploadDir(ul, pathsToUpload, nil /*=visited*/)
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
	return digest.NewCASResourceName(rootDirectoryDigest, instanceName, DigestFunction), digest.NewCASResourceName(treeDigest, instanceName, DigestFunction), nil
}

func uploadDir(ul *cachetools.BatchCASUploader, pathsToUpload []string, visited []*repb.Directory) ([]*repb.Directory, *repb.Digest, error) {
	dir := &repb.Directory{}
	// Append the directory before doing any other work, so that the root
	// directory is located at visited[0] at the end of recursion.
	visited = append(visited, dir)

	if len(visited) > 10 {
		panic("recursion error")
	}
	root := pathsToUpload[0]
	rest := pathsToUpload[1:]

	for i, path := range rest {
		name := strings.TrimPrefix(path, root)
		parts := strings.Count(name, string(filepath.Separator))
		if parts != 1 {
			continue
		}
		entry, err := os.Stat(path)
		if err != nil {
			return nil, nil, err
		}
		doPrint := ul != nil && false // Enable for debugging.
		if entry.IsDir() {
			if doPrint {
				fmt.Printf("D%s%s\n", strings.Repeat(" ", len(visited)), path)
			}

			var d *repb.Digest
			visited, d, err = uploadDir(ul, rest[i:], visited)
			if err != nil {
				return nil, nil, err
			}
			dir.Directories = append(dir.Directories, &repb.DirectoryNode{
				Name:   name,
				Digest: d,
			})
		} else if entry.Mode().IsRegular() {
			if doPrint {
				fmt.Printf("F%s%s\n", strings.Repeat(" ", len(visited)), path)
			}
			info := entry
			var d *repb.Digest
			if ul != nil {
				d, err = ul.UploadFile(path)
			} else {
				d, err = digest.ComputeForFile(path, DigestFunction)
			}
			if err != nil {
				return nil, nil, err
			}
			dir.Files = append(dir.Files, &repb.FileNode{
				Name:         name,
				Digest:       d,
				IsExecutable: isExecutable(info),
			})
		} else if entry.Mode()&os.ModeSymlink == os.ModeSymlink {
			if doPrint {
				fmt.Printf("L%s%s\n", strings.Repeat(" ", len(visited)), path)
			}
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
	var err error
	var d *repb.Digest
	if ul != nil {
		d, err = ul.UploadProto(dir)
	} else {
		d, err = digest.ComputeForMessage(dir, DigestFunction)
	}
	if err != nil {
		return nil, nil, err
	}
	return visited, d, nil
}

func isExecutable(info os.FileInfo) bool {
	return info.Mode()&0100 != 0
}

type Downloader struct {
	bspb.ByteStreamClient
	repb.ContentAddressableStorageClient
	repb.ActionCacheClient
}

func (d *Downloader) DownloadActionResult(ctx context.Context, ar *digest.ACResourceName) (*repb.ActionResult, error) {
	ctx = appendHeadersToCtx(ctx)
	return cachetools.GetActionResult(ctx, d, ar)
}

func (d *Downloader) GetBlob(ctx context.Context, r *digest.CASResourceName, out io.Writer) error {
	ctx = appendHeadersToCtx(ctx)
	r.SetCompressor(repb.Compressor_ZSTD)
	return cachetools.GetBlob(ctx, d, r, out)
}
