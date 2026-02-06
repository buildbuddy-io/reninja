package filetransfer

import (
	"context"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/buildbuddy-io/reninja/internal/cachetools"
	"github.com/buildbuddy-io/reninja/internal/digest"
	"github.com/buildbuddy-io/reninja/internal/grpc_client"
	"github.com/buildbuddy-io/reninja/internal/project_root"
	"github.com/buildbuddy-io/reninja/internal/remote_flags"
	"github.com/buildbuddy-io/reninja/internal/remote_headers"
	"github.com/buildbuddy-io/reninja/internal/statuserr"
	"github.com/buildbuddy-io/reninja/internal/util"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	repb "github.com/buildbuddy-io/reninja/genproto/remote_execution"
	bspb "google.golang.org/genproto/googleapis/bytestream"
)

const (
	DigestFunction = repb.DigestFunction_BLAKE3
)

// UploadableNode represents a node in the directory tree that can be uploaded to CAS.
type UploadableNode struct {
	// Digest is the content-addressed digest of this node.
	Digest *repb.Digest

	// ReadFn returns a reader for the content to upload.
	// The caller is responsible for closing the returned reader.
	ReadFn func() (io.ReadSeekCloser, error)

	// Directory is non-nil only for directory nodes.
	// Used to build the Tree proto after traversal.
	Directory *repb.Directory
}

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
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root := project_root.Root()

	cleanedFiles := make([]string, len(dirty))
	for i, dirtyPath := range dirty {
		absPath := dirtyPath
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(cwd, absPath)
		}
		cleaned, err := filepath.Rel(root, absPath)
		if err != nil {
			return nil, err
		}
		cleanedFiles[i] = cleaned
	}
	return cleanedFiles, nil
}

func hierarchicalPathCompare(p1, p2 string) int {
	b1 := []byte(p1)
	b2 := []byte(p2)

	minLen := min(len(b1), len(b2))
	for i := 0; i < minLen; i++ {
		c1, c2 := b1[i], b2[i]
		if c1 != c2 {
			// '/' sorts before everything else
			if c1 == '/' {
				return -1
			}
			if c2 == '/' {
				return 1
			}
			if c1 < c2 {
				return -1
			}
			return 1
		}
	}

	// Common prefix matches, shorter string comes first
	return len(b1) - len(b2)
}

func expandTree(cleanedFiles []string) []string {
	paths := make(map[string]struct{}, len(cleanedFiles))
	paths["."] = struct{}{} // Always include the root directory
	for _, path := range cleanedFiles {
		start := 0
		for i := strings.IndexRune(path, filepath.Separator); i >= 0; i = strings.IndexRune(path[start:], filepath.Separator) {
			subPath := path[0 : start+i]
			if subPath != "" {
				paths[subPath] = struct{}{}
			}
			start += i + 1
		}
		paths[path] = struct{}{}
	}

	// Ensure the working directory and all its parents exist in the tree.
	// REAPI requires working_directory to be present in the input root.
	wd := project_root.WorkingDirectory()
	if wd != "." {
		for wd != "." && wd != "" {
			paths[wd] = struct{}{}
			wd = filepath.Dir(wd)
		}
	}

	// Sort so that each path is directly followed by its children.
	sorted := slices.SortedFunc(maps.Keys(paths), func(i, j string) int {
		return hierarchicalPathCompare(i, j)
	})
	return sorted
}

type FlattenedTree []*UploadableNode

func (u Uploader) HashDirectoryTree(files []string) (*repb.Digest, FlattenedTree, error) {
	cleanedFiles, err := cleanPaths(files)
	if err != nil {
		return nil, nil, err
	}

	pathsToUpload := expandTree(cleanedFiles)
	visited, rootDirectoryDigest, err := computeDirTree(pathsToUpload, nil /*=visited*/)
	if err != nil {
		return nil, nil, err
	}
	return rootDirectoryDigest, visited, nil
}

func UploadDirectoryTreeToCAS(ul *cachetools.BatchCASUploader, flatTree FlattenedTree) error {
	if len(flatTree) == 0 {
		return statuserr.InternalError("empty tree")
	}
	var dirs []*repb.Directory
	for _, node := range flatTree {
		if node.Directory != nil {
			dirs = append(dirs, node.Directory)
		}
		rsc, err := node.ReadFn()
		if err != nil {
			return err
		}
		if err := ul.Upload(node.Digest, rsc); err != nil {
			return err
		}
	}
	if len(dirs) == 0 {
		return statuserr.InternalError("no directory nodes found; this should never happen")
	}
	rootTree := &repb.Tree{Root: dirs[0], Children: dirs[1:]}
	_, err := ul.UploadProto(rootTree)
	return err
}

// computeDirTree traverses the directory tree rooted at pathsToUpload[0] and
// computes digests for all files and directories. It returns a slice of
// *UploadableNode containing all nodes that need to be uploaded, along with
// the digest of the root directory.
//
// The visited slice accumulates nodes in traversal order, with directory nodes
// appearing before their contents. The first directory node in the returned
// slice is the root directory.
//
// The paths in pathsToUpload must have already been sorted hierarchically,
// using something like hierarchicalPathCompare. That does not happen directly
// in this method because it recurses.
func computeDirTree(pathsToUpload []string, visited []*UploadableNode) ([]*UploadableNode, *repb.Digest, error) {
	dir := &repb.Directory{}
	uploadableNode := &UploadableNode{}
	visited = append(visited, uploadableNode)

	var root string
	var rest []string

	if len(pathsToUpload) > 0 {
		root = pathsToUpload[0]
		rest = pathsToUpload[1:]
	}

	projRoot := project_root.Root()
	for i, path := range rest {
		if filepath.Dir(path) != root {
			continue
		}
		name := filepath.Base(path)
		diskPath := filepath.Join(projRoot, path)
		entry, err := os.Lstat(diskPath) // NB: Lstat.
		if err != nil {
			return nil, nil, err
		}
		if entry.IsDir() {
			var d *repb.Digest
			visited, d, err = computeDirTree(rest[i:], visited)
			if err != nil {
				return nil, nil, err
			}
			dir.Directories = append(dir.Directories, &repb.DirectoryNode{
				Name:   name,
				Digest: d,
			})
		} else if entry.Mode().IsRegular() {
			info := entry
			d, err := digest.ComputeForFile(diskPath, DigestFunction)
			if err != nil {
				return nil, nil, err
			}
			dir.Files = append(dir.Files, &repb.FileNode{
				Name:         name,
				Digest:       d,
				IsExecutable: isExecutable(info),
			})
			diskPath := diskPath
			visited = append(visited, &UploadableNode{
				Digest: d,
				ReadFn: func() (io.ReadSeekCloser, error) {
					return os.Open(diskPath)
				},
			})
		} else if entry.Mode()&os.ModeSymlink == os.ModeSymlink {
			target, err := os.Readlink(diskPath)
			if err != nil {
				return nil, nil, err
			}
			dir.Symlinks = append(dir.Symlinks, &repb.SymlinkNode{
				Name:   name,
				Target: target,
			})
			// Symlinks don't need to be uploaded separately; they're part of the Directory proto
		}
	}

	d, err := digest.ComputeForMessage(dir, DigestFunction)
	if err != nil {
		return nil, nil, err
	}

	uploadableNode.Digest = d
	uploadableNode.Directory = dir
	uploadableNode.ReadFn = func() (io.ReadSeekCloser, error) {
		data, err := proto.Marshal(uploadableNode.Directory)
		if err != nil {
			return nil, err
		}
		return cachetools.NewBytesReadSeekCloser(data), nil
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

func (d *Downloader) DownloadActionResult(ctx context.Context, action *repb.Action) (*repb.ActionResult, error) {
	ctx = appendHeadersToCtx(ctx)

	di, err := digest.ComputeForMessage(action, DigestFunction)
	if err != nil {
		return nil, err
	}

	acrn := digest.NewACResourceName(di, remote_flags.RemoteInstanceName(), DigestFunction)
	return cachetools.GetActionResult(ctx, d, acrn)
}

func (d *Downloader) GetBlob(ctx context.Context, r *digest.CASResourceName, out io.Writer) error {
	ctx = appendHeadersToCtx(ctx)
	r.SetCompressor(repb.Compressor_ZSTD)
	return cachetools.GetBlob(ctx, d, r, out)
}
