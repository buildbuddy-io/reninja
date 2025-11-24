package request_metadata

import (
	"context"

	"github.com/buildbuddy-io/gin/internal/version"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	repb "github.com/buildbuddy-io/gin/genproto/remote_execution"
)

const headerName = "build.bazel.remote.execution.v2.requestmetadata-bin"

var invocationID string

func SetInvocationID(i string) {
	invocationID = i
}

func GetInvocationRequestMetadata() (string, string, bool) {
	return GetCacheRequestMetadata("", "", "")
}

// GetCacheRequestMetadata sets bazel request metadata.
// actionID should be a unique ID that ties multiple reqs to the same action.
// actionMnemonic should be a brief description of the action (CppCompile or GoLink)
// targetID should be an identifier for the target that produced the action.
func GetCacheRequestMetadata(actionID, actionMnemonic, targetID string) (string, string, bool) {
	rmd := &repb.RequestMetadata{
		ToolDetails: &repb.ToolDetails{
			ToolName:    "ninja",
			ToolVersion: version.NinjaVersion,
		},
		ActionId:         actionID,
		ActionMnemonic:   actionMnemonic,
		TargetId:         targetID,
		ToolInvocationId: invocationID,
	}

	buf, err := proto.Marshal(rmd)
	if err != nil {
		return "", "", false
	}
	return headerName, string(buf), true
}

func AttachCacheRequestMetadata(ctx context.Context, actionID, actionMnemonic, targetID string) context.Context {
	key, val, ok := GetCacheRequestMetadata(actionID, actionMnemonic, targetID)
	if !ok {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, key, val)
}
