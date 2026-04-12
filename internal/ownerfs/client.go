package ownerfs

import (
	"context"

	"symterm/internal/proto"
)

type Client interface {
	FsRead(context.Context, proto.FsOperation, proto.FsRequest) (proto.FsReply, error)
	Apply(context.Context, proto.OwnerFileApplyRequest) error
	BeginFileUpload(context.Context, proto.OwnerFileBeginRequest) (proto.OwnerFileBeginResponse, error)
	ApplyFileChunk(context.Context, proto.OwnerFileApplyChunkRequest) error
	CommitFileUpload(context.Context, proto.OwnerFileCommitRequest) error
	AbortFileUpload(context.Context, proto.OwnerFileAbortRequest) error
	Done() <-chan struct{}
	Close() error
}
