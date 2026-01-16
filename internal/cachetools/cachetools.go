package cachetools

import (
	"bytes"
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/buildbuddy-io/reninja/internal/bytebufferpool"
	"github.com/buildbuddy-io/reninja/internal/compression"
	"github.com/buildbuddy-io/reninja/internal/digest"
	"github.com/buildbuddy-io/reninja/internal/ioutil"
	"github.com/buildbuddy-io/reninja/internal/retry"
	"github.com/buildbuddy-io/reninja/internal/rpcutil"
	"github.com/buildbuddy-io/reninja/internal/statuserr"
	"github.com/buildbuddy-io/reninja/internal/util"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"

	repb "github.com/buildbuddy-io/reninja/genproto/remote_execution"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	gcodes "google.golang.org/grpc/codes"
	gstatus "google.golang.org/grpc/status"
)

const (
	// uploadBufSizeBytes controls the size of the buffers used for uploading
	// to bytestream.Write. This means it also controls the payload size for
	// each WriteRequest. https://github.com/grpc/grpc.github.io/issues/371
	// that 16KiB-64KiB payloads work best, but our experiments and benchmarks
	// show that 128KiB works best. Values bigger and slower than that are both
	// slower. Values bigger than that allocate more bytes, and values smaller
	// than that allocate the same number of bytes but with more allocations.
	uploadBufSizeBytes = 128 * 1024

	// Matches https://github.com/bazelbuild/bazel/blob/9c22032c8dc0eb2ec20d8b5a5c73d1f5f075ae37/src/main/java/com/google/devtools/build/lib/remote/options/RemoteOptions.java#L461-L464
	minSizeBytesToCompress = 100

	// batchUploadLimitBytes controls how big an object or batch can be in a
	// BatchUploadBlobs RPC. In experiments, 2MiB blobs are 5-10% faster to
	// upload using the bytestream.Write api.
	BatchUploadLimitBytes = min(2*1024*1024, rpcutil.GRPCMaxSizeBytes)
)

var (
	enableCompression = flag.Bool("enable_compression", true, "If true, enable compression of transfers to/from remote caches")
	casRPCTimeout     = flag.Duration("cas_rpc_timeout", 1*time.Minute, "Maximum time a single batch RPC or a single ByteStream chunk read can take.")
	acRPCTimeout      = flag.Duration("ac_rpc_timeout", 15*time.Second, "Maximum time a single Action Cache RPC can take.")

	uploadBufPool = bytebufferpool.VariableSize(uploadBufSizeBytes)
)

func retryOptions(name string) *retry.Options {
	opts := retry.DefaultOptions()
	opts.MaxRetries = 3
	opts.Name = name
	return opts
}

type nopCloser struct {
	io.Writer
}

func (nopCloser) Close() error { return nil }

func getBlob(ctx context.Context, bsClient bspb.ByteStreamClient, r *digest.CASResourceName, out io.Writer) error {
	if bsClient == nil {
		return statuserr.FailedPreconditionError("ByteStreamClient not configured")
	}
	if r.IsEmpty() {
		return nil
	}

	req := &bspb.ReadRequest{
		ResourceName: r.DownloadString(),
	}
	stream, err := bsClient.Read(ctx, req)
	if err != nil {
		if gstatus.Code(err) == gcodes.NotFound {
			return digest.MissingDigestError(r.GetDigest())
		}
		return err
	}
	checksum, err := digest.HashForDigestType(r.GetDigestFunction())
	if err != nil {
		return err
	}
	w := io.MultiWriter(checksum, out)

	close := func() error { return nil }
	if r.GetCompressor() == repb.Compressor_ZSTD {
		decompressor, err := compression.NewZstdDecompressor(w)
		if err != nil {
			return err
		}
		w = decompressor
		close = sync.OnceValue(decompressor.Close)
	}
	defer close()

	receiver := rpcutil.NewReceiver[*bspb.ReadResponse](ctx, stream)
	for {
		rsp, err := receiver.RecvWithTimeoutCause(*casRPCTimeout, statuserr.DeadlineExceededError("timed out waiting for Read response"))
		if err == io.EOF {
			// Close before returning from this loop to make sure all bytes are
			// flushed from the decompressor to the output/checksum writers.
			// Note: this is safe even though we also defer close() above, since we
			// wrap decompressor.Close with sync.OnceValue.
			if err := close(); err != nil {
				return err
			}
			break
		}
		if err != nil {
			return err
		}
		if _, err := w.Write(rsp.Data); err != nil {
			return err
		}
	}
	computedDigest := hex.EncodeToString(checksum.Sum(nil))
	if computedDigest != r.GetDigest().GetHash() {
		return statuserr.DataLossErrorf("Downloaded content (hash %q) did not match expected (hash %q)", computedDigest, r.GetDigest().GetHash())
	}
	return nil
}

func FindMissingBlobs(ctx context.Context, casClient repb.ContentAddressableStorageClient, req *repb.FindMissingBlobsRequest) ([]*digest.CASResourceName, error) {
	return retry.Do(ctx, retryOptions("FindMissingBlobs"), func(ctx context.Context) ([]*digest.CASResourceName, error) {
		ctx, cancel := context.WithTimeout(ctx, *casRPCTimeout)
		defer cancel()
		return findMissingBlobs(ctx, casClient, req)
	})
}

func findMissingBlobs(ctx context.Context, casClient repb.ContentAddressableStorageClient, req *repb.FindMissingBlobsRequest) ([]*digest.CASResourceName, error) {
	rsp, err := casClient.FindMissingBlobs(ctx, req)
	if err != nil {
		return nil, err
	}
	missing := make([]*digest.CASResourceName, len(rsp.GetMissingBlobDigests()))
	for i, d := range rsp.GetMissingBlobDigests() {
		missing[i] = digest.NewCASResourceName(d, req.GetInstanceName(), req.GetDigestFunction())
	}
	return missing, nil
}

func GetBlob(ctx context.Context, bsClient bspb.ByteStreamClient, r *digest.CASResourceName, out io.Writer) error {
	maybeSetCompressor(r)
	// We can only retry if we can rewind the writer back to the beginning.
	seeker, retryable := out.(io.Seeker)
	if retryable {
		return retry.DoVoid(ctx, retryOptions("ByteStream.Read"), func(ctx context.Context) error {
			if _, err := seeker.Seek(0, io.SeekStart); err != nil {
				return retry.NonRetryableError(err)
			}
			ctx, cancel := context.WithTimeout(ctx, *casRPCTimeout)
			defer cancel()
			err := getBlob(ctx, bsClient, r, out)
			if statuserr.IsNotFoundError(err) {
				return retry.NonRetryableError(err)
			}
			return err
		})
	} else {
		return getBlob(ctx, bsClient, r, out)
	}
}

// BlobResponse is a response to an individual blob in a BatchReadBlobs request.
type BlobResponse struct {
	// Digest identifies the blob that was requested.
	Digest *repb.Digest

	// Data contains the blob contents if it was fetched successfully.
	Data []byte
	// Err holds any error encountered when fetching the blob.
	Err error
}

// BatchReadBlobs issues a BatchReadBlobs request and returns a mapping from
// digest hash to byte payload.
//
// It validates the response so that if the returned err is nil, then all
// digests in the request are guaranteed to have a corresponding map entry.
func BatchReadBlobs(ctx context.Context, casClient repb.ContentAddressableStorageClient, req *repb.BatchReadBlobsRequest) ([]*BlobResponse, error) {
	return retry.Do(ctx, retryOptions("BatchReadBlobs"), func(ctx context.Context) ([]*BlobResponse, error) {
		ctx, cancel := context.WithTimeout(ctx, *casRPCTimeout)
		defer cancel()
		return batchReadBlobs(ctx, casClient, req)
	})
}

func batchReadBlobs(ctx context.Context, casClient repb.ContentAddressableStorageClient, req *repb.BatchReadBlobsRequest) ([]*BlobResponse, error) {
	res, err := casClient.BatchReadBlobs(ctx, req)
	if err != nil {
		return nil, err
	}
	expected := map[string]struct{}{}
	for _, d := range req.GetDigests() {
		expected[d.GetHash()] = struct{}{}
	}
	// Validate that the response doesn't contain any unexpected digests.
	for _, res := range res.Responses {
		if _, ok := expected[res.GetDigest().GetHash()]; !ok {
			return nil, statuserr.UnknownErrorf("unexpected digest in batch response: %q", digest.String(res.GetDigest()))
		}
	}
	// Build the results map, decompressing if needed and validating digests.
	results := make([]*BlobResponse, 0, len(res.Responses))
	for _, res := range res.Responses {
		delete(expected, res.GetDigest().GetHash())

		err := gstatus.ErrorProto(res.GetStatus())
		if err != nil {
			results = append(results, &BlobResponse{
				Digest: res.GetDigest(),
				Err:    err,
			})
			continue
		}
		// TODO: parallel decompression
		// TODO: accept decompression buffer map as optional arg
		data, err := decompressBytes(res.Data, res.GetDigest(), res.GetCompressor())
		if err != nil {
			return nil, statuserr.WrapError(err, "decompress blob")
		}
		// Validate digest
		downloadedContentDigest, err := digest.Compute(bytes.NewReader(data), req.GetDigestFunction())
		if err != nil {
			return nil, err
		}
		if downloadedContentDigest.GetHash() != res.GetDigest().GetHash() || downloadedContentDigest.GetSizeBytes() != res.GetDigest().GetSizeBytes() {
			return nil, statuserr.UnknownErrorf("digest validation failed: expected %q, got %q", digest.String(res.GetDigest()), digest.String(downloadedContentDigest))
		}
		results = append(results, &BlobResponse{
			Digest: res.GetDigest(),
			Data:   data,
		})
	}
	if len(expected) > 0 {
		return nil, statuserr.UnknownErrorf("missing digests in response: %s", slices.Collect(maps.Keys(expected)))
	}
	return results, nil
}

func computeDigest(in io.ReadSeeker, instanceName string, digestFunction repb.DigestFunction_Value) (*digest.CASResourceName, error) {
	d, err := digest.Compute(in, digestFunction)
	if err != nil {
		return nil, err
	}
	return digest.NewCASResourceName(d, instanceName, digestFunction), nil
}

func ComputeFileDigest(fullFilePath, instanceName string, digestFunction repb.DigestFunction_Value) (*digest.CASResourceName, error) {
	f, err := os.Open(fullFilePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return computeDigest(f, instanceName, digestFunction)
}

func uploadFromReader(ctx context.Context, bsClient bspb.ByteStreamClient, r *digest.CASResourceName, in io.Reader) (*repb.Digest, int64, error) {
	if bsClient == nil {
		return nil, 0, statuserr.FailedPreconditionError("ByteStreamClient not configured")
	}
	if r.IsEmpty() {
		return r.GetDigest(), 0, nil
	}
	stream, err := bsClient.Write(ctx)
	if err != nil {
		return nil, 0, err
	}

	bufSize := int64(digest.SafeBufferSize(r.ToProto(), uploadBufSizeBytes))
	var rc io.ReadCloser = io.NopCloser(in)
	if r.GetCompressor() == repb.Compressor_ZSTD {
		reader, err := compression.NewBufferedZstdCompressingReader(rc, uploadBufPool, bufSize)
		if err != nil {
			return nil, 0, statuserr.InternalErrorf("Failed to compress blob: %s", err)
		}
		rc = reader
	}
	defer rc.Close()

	buf := uploadBufPool.Get(bufSize)
	defer uploadBufPool.Put(buf)
	bytesUploaded := int64(0)
	sender := rpcutil.NewSender(ctx, stream)
	resourceName := r.NewUploadString()
	for {
		n, err := ioutil.ReadTryFillBuffer(rc, buf)
		if err != nil && err != io.EOF {
			return nil, bytesUploaded, err
		}
		readDone := err == io.EOF

		req := &bspb.WriteRequest{
			Data:         buf[:n],
			ResourceName: resourceName,
			WriteOffset:  bytesUploaded,
			FinishWrite:  readDone,
		}
		resourceName = "" // Only set resource name on first request

		err = sender.SendWithTimeoutCause(req, *casRPCTimeout, statuserr.DeadlineExceededError("Timed out sending Write request"))
		if err != nil {
			// If the blob already exists in the CAS, the server will respond EOF.
			// It is safe to stop sending writes.
			if err == io.EOF {
				break
			}
			return nil, bytesUploaded, err
		}
		bytesUploaded += int64(len(req.Data))
		if readDone {
			break
		}

	}
	rsp, err := stream.CloseAndRecv()
	if err != nil {
		// If there is a hash mismatch and the reader supports seeking, re-hash
		// to check whether a concurrent mutation has occurred.
		if rs, ok := in.(io.ReadSeeker); ok && statuserr.IsDataLossError(err) {
			if err := checkConcurrentMutation(r.GetDigest(), r.GetDigestFunction(), rs); err != nil {
				return nil, 0, retry.NonRetryableError(statuserr.WrapError(err, "check for concurrent mutation during upload"))
			}
		}
		return nil, bytesUploaded, err
	}

	remoteSize := rsp.GetCommittedSize()
	if r.GetCompressor() == repb.Compressor_IDENTITY {
		// Either the write succeeded or was short-circuited, but in
		// either case, the remoteSize for uncompressed uploads should
		// match the file size.
		if remoteSize != r.GetDigest().GetSizeBytes() {
			return nil, bytesUploaded, statuserr.DataLossErrorf("Remote size (%d) != uploaded size: (%d)", remoteSize, r.GetDigest().GetSizeBytes())
		}
	} else {
		// -1 is returned if the blob already exists, otherwise the
		// remoteSize should agree with what we uploaded.
		if remoteSize != bytesUploaded && remoteSize != -1 {
			return nil, bytesUploaded, statuserr.DataLossErrorf("Remote size (%d) != uploaded size: (%d)", remoteSize, r.GetDigest().GetSizeBytes())
		}
	}

	return r.GetDigest(), bytesUploaded, nil
}

type uploadRetryResult = struct {
	digest        *repb.Digest
	uploadedBytes int64
}

// UploadFromReader attempts to read all bytes from the `in` `Reader` until encountering an EOF
// and write all those bytes to the CAS.
// If the input Reader is also a Seeker, UploadFromReader will retry the upload until success.
//
// On success, it returns the digest of the uploaded blob and the number of bytes confirmed uploaded.
// If the blob already exists, this call will succeed and return the number of bytes uploaded before the server short-circuited the upload.
// On error, it returns the number of bytes uploaded before the error (and the error).
// UploadFromReader confirms that the expected number of bytes have been written to the CAS
// and returns a DataLossError if not.
func UploadFromReader(ctx context.Context, bsClient bspb.ByteStreamClient, r *digest.CASResourceName, in io.Reader) (*repb.Digest, int64, error) {
	if digest.IsEmptyHash(r.GetDigest(), r.GetDigestFunction()) {
		// Skipping empty digest upload.
		return r.GetDigest(), 0, nil
	}
	// We can only retry if we can rewind the reader back to the beginning.
	seeker, retryable := in.(io.Seeker)
	if retryable {
		result, err := retry.Do(ctx, retryOptions("ByteStream.Write"), func(ctx context.Context) (uploadRetryResult, error) {
			if casClient, ok := bsClient.(repb.ContentAddressableStorageClient); ok {
				missing, err := findMissingBlobs(ctx, casClient, &repb.FindMissingBlobsRequest{
					InstanceName:   r.GetInstanceName(),
					DigestFunction: r.GetDigestFunction(),
					BlobDigests:    []*repb.Digest{r.GetDigest()},
				})
				if err == nil && len(missing) == 0 {
					return uploadRetryResult{digest: r.GetDigest(), uploadedBytes: 0}, nil
				}
			}
			if _, err := seeker.Seek(0, io.SeekStart); err != nil {
				return uploadRetryResult{digest: nil, uploadedBytes: 0}, retry.NonRetryableError(err)
			}
			d, u, err := uploadFromReader(ctx, bsClient, r, in)
			return uploadRetryResult{
				digest:        d,
				uploadedBytes: u,
			}, err
		})
		return result.digest, result.uploadedBytes, err
	} else {
		return uploadFromReader(ctx, bsClient, r, in)
	}
}

func GetActionResultCustom(ctx context.Context, acClient repb.ActionCacheClient, req *repb.GetActionResultRequest) (*repb.ActionResult, error) {
	if acClient == nil {
		return nil, statuserr.FailedPreconditionError("ActionCacheClient not configured")
	}
	return retry.Do(ctx, retryOptions("GetActionResult"), func(ctx context.Context) (*repb.ActionResult, error) {
		ctx, cancel := context.WithTimeout(ctx, *acRPCTimeout)
		defer cancel()
		rsp, err := acClient.GetActionResult(ctx, req)
		if statuserr.IsNotFoundError(err) {
			return nil, retry.NonRetryableError(err)
		}
		return rsp, err
	})
}

func GetActionResult(ctx context.Context, acClient repb.ActionCacheClient, ar *digest.ACResourceName) (*repb.ActionResult, error) {
	if acClient == nil {
		return nil, statuserr.FailedPreconditionError("ActionCacheClient not configured")
	}
	req := &repb.GetActionResultRequest{
		ActionDigest:   ar.GetDigest(),
		InstanceName:   ar.GetInstanceName(),
		DigestFunction: ar.GetDigestFunction(),
	}
	return GetActionResultCustom(ctx, acClient, req)
}

func UploadActionResult(ctx context.Context, acClient repb.ActionCacheClient, r *digest.ACResourceName, ar *repb.ActionResult) error {
	if acClient == nil {
		return statuserr.FailedPreconditionError("ActionCacheClient not configured")
	}

	req := &repb.UpdateActionResultRequest{
		InstanceName:   r.GetInstanceName(),
		ActionDigest:   r.GetDigest(),
		ActionResult:   ar,
		DigestFunction: r.GetDigestFunction(),
	}
	_, err := retry.Do(ctx, retryOptions("UpdateActionResult"), func(ctx context.Context) (*repb.ActionResult, error) {
		ctx, cancel := context.WithTimeout(ctx, *acRPCTimeout)
		defer cancel()
		return acClient.UpdateActionResult(ctx, req)
	})
	return err
}

func UploadProto(ctx context.Context, bsClient bspb.ByteStreamClient, instanceName string, digestFunction repb.DigestFunction_Value, in proto.Message) (*repb.Digest, error) {
	data, err := proto.Marshal(in)
	if err != nil {
		return nil, err
	}
	reader := bytes.NewReader(data)
	resourceName, err := computeDigest(reader, instanceName, digestFunction)
	if err != nil {
		return nil, err
	}
	maybeSetCompressor(resourceName)
	// Go back to the beginning so we can re-read the file contents as we upload.
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	result, _, err := UploadFromReader(ctx, bsClient, resourceName, reader)
	return result, err
}

func UploadBlob(ctx context.Context, bsClient bspb.ByteStreamClient, instanceName string, digestFunction repb.DigestFunction_Value, in io.ReadSeeker) (*repb.Digest, error) {
	resourceName, err := computeDigest(in, instanceName, digestFunction)
	if err != nil {
		return nil, err
	}
	maybeSetCompressor(resourceName)
	// Go back to the beginning so we can re-read the file contents as we upload.
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	result, _, err := UploadFromReader(ctx, bsClient, resourceName, in)
	return result, err
}

func UploadFile(ctx context.Context, bsClient bspb.ByteStreamClient, instanceName string, digestFunction repb.DigestFunction_Value, fullFilePath string) (*repb.Digest, error) {
	f, err := os.Open(fullFilePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	resourceName, err := computeDigest(f, instanceName, digestFunction)
	if err != nil {
		return nil, err
	}
	maybeSetCompressor(resourceName)
	// Go back to the beginning so we can re-read the file contents as we upload.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	result, _, err := UploadFromReader(ctx, bsClient, resourceName, f)
	return result, err
}

// byteWriteSeeker implements an io.WriterAt with a []byte array. In turn, this
// allows using io.OffsetWriter to implement a Writer + Seeker that can be
// passed to GetBlob, which allows retrying failed downloads. We don't use a
// bytes.Buffer because it does not implement the io.Seeker interface.
type byteWriteSeeker []byte

func (ws byteWriteSeeker) WriteAt(p []byte, off int64) (int, error) {
	if len(p)+int(off) > len(ws) {
		return 0, statuserr.FailedPreconditionError("Write off end of byte array")
	}
	return copy(ws[off:], p), nil
}

func GetBlobAsProto(ctx context.Context, bsClient bspb.ByteStreamClient, r *digest.CASResourceName, out proto.Message) error {
	buf := byteWriteSeeker(make([]byte, r.GetDigest().GetSizeBytes()))
	bufWriter := io.NewOffsetWriter(buf, 0)

	if err := GetBlob(ctx, bsClient, r, bufWriter); err != nil {
		return err
	}
	return proto.Unmarshal([]byte(buf), out)
}

func UploadBlobToCAS(ctx context.Context, bsClient bspb.ByteStreamClient, instanceName string, digestFunction repb.DigestFunction_Value, blob []byte) (*repb.Digest, error) {
	reader := bytes.NewReader(blob)
	d, err := digest.Compute(reader, digestFunction)
	if err != nil {
		return nil, err
	}
	resourceName := digest.NewCASResourceName(d, instanceName, digestFunction)
	if resourceName.IsEmpty() {
		return d, nil
	}
	maybeSetCompressor(resourceName)
	result, _, err := UploadFromReader(ctx, bsClient, resourceName, reader)
	return result, err
}

// BatchCASUploader uploads many files to CAS concurrently, batching small
// uploads together and falling back to bytestream uploads for large files.
type BatchCASUploader struct {
	bspb.ByteStreamClient
	repb.ContentAddressableStorageClient
	ctx             context.Context
	eg              *errgroup.Group
	unsentBatchReq  *repb.BatchUpdateBlobsRequest
	uploads         map[digest.Key]struct{}
	instanceName    string
	digestFunction  repb.DigestFunction_Value
	unsentBatchSize int64
	stats           UploadStats
}

// NewBatchCASUploader returns an uploader to be used only for the given request
// context (it should not be used outside the lifecycle of the request).
func NewBatchCASUploader(ctx context.Context, bsClient bspb.ByteStreamClient, casClient repb.ContentAddressableStorageClient, instanceName string, digestFunction repb.DigestFunction_Value) *BatchCASUploader {
	eg, ctx := errgroup.WithContext(ctx)
	return &BatchCASUploader{
		ByteStreamClient:                bsClient,
		ContentAddressableStorageClient: casClient,
		ctx:                             ctx,
		eg:                              eg,
		unsentBatchReq:                  &repb.BatchUpdateBlobsRequest{InstanceName: instanceName, DigestFunction: digestFunction},
		unsentBatchSize:                 0,
		instanceName:                    instanceName,
		digestFunction:                  digestFunction,
		uploads:                         make(map[digest.Key]struct{}),
	}
}

// Upload adds the given content to the current batch or begins a streaming
// upload if it exceeds the maximum batch size. It closes r when it is no
// longer needed.
func (ul *BatchCASUploader) Upload(d *repb.Digest, rsc io.ReadSeekCloser) error {
	// De-dupe uploads by digest.
	dk := digest.NewKey(d)
	if _, ok := ul.uploads[dk]; ok {
		ul.stats.DuplicateBytes += d.GetSizeBytes()
		return rsc.Close()
	}
	ul.uploads[dk] = struct{}{}
	ul.stats.UploadedObjects++
	ul.stats.UploadedBytes += d.GetSizeBytes()

	rsc.Seek(0, 0)
	r := io.ReadCloser(rsc)

	compressor := repb.Compressor_IDENTITY
	if *enableCompression && d.GetSizeBytes() >= minSizeBytesToCompress {
		compressor = repb.Compressor_ZSTD
	}

	if d.GetSizeBytes() > BatchUploadLimitBytes {
		resourceName := digest.NewCASResourceName(d, ul.instanceName, ul.digestFunction)
		resourceName.SetCompressor(compressor)

		byteStreamClient := ul.ByteStreamClient
		if byteStreamClient == nil {
			return statuserr.InvalidArgumentError("missing bytestream client")
		}
		ul.eg.Go(func() error {
			defer r.Close()
			_, _, err := UploadFromReader(ul.ctx, ul, resourceName, r)
			return err
		})
		return nil
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if err := r.Close(); err != nil {
		return err
	}

	if compressor == repb.Compressor_ZSTD {
		b = compression.CompressZstd(nil, b)
	}
	additionalSize := int64(len(b))
	if ul.unsentBatchSize+additionalSize > BatchUploadLimitBytes {
		ul.flushCurrentBatch()
	}
	ul.unsentBatchReq.Requests = append(ul.unsentBatchReq.Requests, &repb.BatchUpdateBlobsRequest_Request{
		Digest:     d,
		Data:       b,
		Compressor: compressor,
	})
	ul.unsentBatchSize += additionalSize
	return nil
}

func (ul *BatchCASUploader) UploadProto(in proto.Message) (*repb.Digest, error) {
	data, err := proto.Marshal(in)
	if err != nil {
		return nil, err
	}
	return ul.UploadBlob(data)
}

func (ul *BatchCASUploader) UploadBlob(data []byte) (*repb.Digest, error) {
	d, err := digest.Compute(bytes.NewReader(data), ul.digestFunction)
	if err != nil {
		return nil, err
	}
	if err := ul.Upload(d, NewBytesReadSeekCloser(data)); err != nil {
		return nil, err
	}
	return d, nil
}

func (ul *BatchCASUploader) UploadFile(path string) (*repb.Digest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	d, err := digest.Compute(f, ul.digestFunction)
	if err != nil {
		return nil, err
	}

	// Note: uploader.Upload will close the file.
	if err := ul.Upload(d, f); err != nil {
		return nil, err
	}
	return d, nil
}

func (ul *BatchCASUploader) flushCurrentBatch() error {
	casClient := ul.ContentAddressableStorageClient
	if casClient == nil {
		return statuserr.InvalidArgumentError("missing CAS client")
	}

	req := ul.unsentBatchReq
	ul.unsentBatchReq = &repb.BatchUpdateBlobsRequest{
		InstanceName:   ul.instanceName,
		DigestFunction: ul.digestFunction,
	}
	ul.unsentBatchSize = 0
	ul.eg.Go(func() error {
		rsp, err := retry.Do(ul.ctx, retryOptions("BatchUpdateBlobs"), func(ctx context.Context) (*repb.BatchUpdateBlobsResponse, error) {
			ctx, cancel := context.WithTimeout(ctx, *casRPCTimeout)
			defer cancel()

			findMissingReq := &repb.FindMissingBlobsRequest{
				InstanceName:   req.GetInstanceName(),
				DigestFunction: req.GetDigestFunction(),
				BlobDigests:    make([]*repb.Digest, len(req.GetRequests())),
			}

			for i, req := range req.GetRequests() {
				findMissingReq.BlobDigests[i] = req.GetDigest()
			}
			missing, err := findMissingBlobs(ctx, ul, findMissingReq)
			if err == nil {
				if len(missing) == 0 {
					return &repb.BatchUpdateBlobsResponse{}, nil
				}
				missingSet := make(map[digest.Key]struct{}, len(missing))
				for _, missingResource := range missing {
					missingSet[digest.NewKey(missingResource.GetDigest())] = struct{}{}
				}
				req.Requests = slices.DeleteFunc(req.Requests, func(br *repb.BatchUpdateBlobsRequest_Request) bool {
					_, missing := missingSet[digest.NewKey(br.GetDigest())]
					return !missing
				})
			}
			return casClient.BatchUpdateBlobs(ctx, req)
		})
		if err != nil {
			return err
		}
		for i, fileResponse := range rsp.GetResponses() {
			if fileResponse.GetStatus().GetCode() == int32(gcodes.DataLoss) && i < len(req.GetRequests()) {
				// If there is a hash mismatch, re-hash the uncompressed payload
				// to check whether a concurrent mutation occurred after we
				// computed the original digest.
				ri := req.GetRequests()[i]
				b, err := decompressBytes(ri.GetData(), ri.GetDigest(), ri.GetCompressor())
				if err != nil {
					util.Warningf("Error decompressing blob while checking for concurrent mutation: %s", err)
				} else {
					if err := checkConcurrentMutation(fileResponse.GetDigest(), ul.digestFunction, bytes.NewReader(b)); err != nil {
						return statuserr.WrapError(err, "check for concurrent mutation during upload")
					}
				}
			}
			if fileResponse.GetStatus().GetCode() != int32(gcodes.OK) {
				return gstatus.Error(gcodes.Code(fileResponse.GetStatus().GetCode()), fmt.Sprintf("Error uploading file: %v", fileResponse.GetDigest()))
			}
		}
		return nil
	})
	return nil
}

func (ul *BatchCASUploader) Wait() error {
	if len(ul.unsentBatchReq.GetRequests()) > 0 {
		if err := ul.flushCurrentBatch(); err != nil {
			return err
		}
	}
	return ul.eg.Wait()
}

func decompressBytes(b []byte, d *repb.Digest, compressor repb.Compressor_Value) ([]byte, error) {
	if compressor == repb.Compressor_ZSTD {
		buf := make([]byte, 0, d.GetSizeBytes())
		return compression.DecompressZstd(buf, b)
	}
	return b, nil
}

// Re-computes the digest for the given reader and compares it to a digest
// computed previously. Returns an error if the digests do not match or if
// there is an error re-computing the digest.
func checkConcurrentMutation(originalDigest *repb.Digest, digestFunction repb.DigestFunction_Value, r io.ReadSeeker) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return statuserr.DataLossErrorf("seek: %s", err)
	}
	computedDigest, err := digest.Compute(r, digestFunction)
	if err != nil {
		return statuserr.DataLossErrorf("recompute digest: %s", err)
	}
	if !digest.Equal(computedDigest, originalDigest) {
		return statuserr.DataLossErrorf("possible concurrent mutation detected: digest changed from %s to %s", digest.String(originalDigest), digest.String(computedDigest))
	}
	return nil
}

// UploadStats contains the statistics for a batch of uploads.
type UploadStats struct {
	UploadedObjects, UploadedBytes, DuplicateBytes int64
}

type bytesReadSeekCloser struct {
	io.ReadSeeker
}

func NewBytesReadSeekCloser(b []byte) io.ReadSeekCloser {
	return &bytesReadSeekCloser{bytes.NewReader(b)}
}
func (*bytesReadSeekCloser) Close() error { return nil }

func maybeSetCompressor(rn *digest.CASResourceName) {
	if *enableCompression && rn.GetDigest().GetSizeBytes() >= minSizeBytesToCompress {
		rn.SetCompressor(repb.Compressor_ZSTD)
	}
}

func IsExecutable(info os.FileInfo) bool {
	return info.Mode()&0100 != 0
}
