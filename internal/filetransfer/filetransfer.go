package filetransfer

import (
	"context"

	"github.com/buildbuddy-io/gin/internal/cachetools"
	"github.com/buildbuddy-io/gin/internal/digest"
	"github.com/buildbuddy-io/gin/internal/grpc_client"
	"github.com/buildbuddy-io/gin/internal/remote_headers"
	"github.com/buildbuddy-io/gin/internal/statuserr"
	"google.golang.org/grpc/metadata"

	repb "github.com/buildbuddy-io/gin/genproto/remote_execution"
	bspb "google.golang.org/genproto/googleapis/bytestream"
)

const (
	digestFunction = repb.DigestFunction_BLAKE3
)

type Uploader struct {
	bsClient bspb.ByteStreamClient
}

func NewUploader(remoteCache string) (*Uploader, error) {
	conn, err := grpc_client.DialSimple(context.TODO(), remoteCache)
	if err != nil {
		return nil, statuserr.WrapError(err, "error dialing remote cache")
	}
	bsClient := bspb.NewByteStreamClient(conn)

	return &Uploader{
		bsClient: bsClient,
	}, nil
}

func (u *Uploader) UploadFile(ctx context.Context, instanceName string, path string) (*digest.CASResourceName, error) {
	extraHeaders := remote_headers.GetPairs()
	if len(extraHeaders) > 0 {
		ctx = metadata.AppendToOutgoingContext(ctx, extraHeaders...)
	}

	d, err := cachetools.UploadFile(ctx, u.bsClient, instanceName, digestFunction, path)
	if err != nil {
		return nil, err
	}
	return digest.NewCASResourceName(d, instanceName, digestFunction), nil
}
