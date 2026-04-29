package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"time"

	"symterm/internal/ownerfs"
	"symterm/internal/proto"
)

type OwnerFileHandler interface {
	FsRead(context.Context, proto.FsOperation, proto.FsRequest) (proto.FsReply, error)
	Apply(context.Context, proto.OwnerFileApplyRequest) error
	BeginFileUpload(context.Context, proto.OwnerFileBeginRequest) (proto.OwnerFileBeginResponse, error)
	ApplyFileChunk(context.Context, proto.OwnerFileApplyChunkRequest) error
	CommitFileUpload(context.Context, proto.OwnerFileCommitRequest) error
	AbortFileUpload(context.Context, proto.OwnerFileAbortRequest) error
}

type ownerFileRPCClient struct {
	client *Client
	closer io.Closer
}

func NewOwnerFileRPCClient(reader io.Reader, writer io.Writer, closer io.Closer) ownerfs.Client {
	return &ownerFileRPCClient{
		client: NewClient(reader, writer),
		closer: closer,
	}
}

// NewOwnerFileRPCClientWithIdleTimeout returns an ownerfs.Client whose
// underlying JSON-RPC reader will force the channel closed if no bytes arrive
// for idleTimeout. This is the defence against a silently dead owner stdio
// channel (no FIN/RST, no SSH-level keepalive failure surfaced) — without it,
// a stuck readLoop wedges every FUSE handler waiting on owner-RPC calls.
func NewOwnerFileRPCClientWithIdleTimeout(reader io.Reader, writer io.Writer, closer io.Closer, idleTimeout time.Duration) ownerfs.Client {
	return &ownerFileRPCClient{
		client: NewClientWithIdleTimeout(reader, writer, closer, idleTimeout),
		closer: closer,
	}
}

func (c *ownerFileRPCClient) FsRead(ctx context.Context, op proto.FsOperation, request proto.FsRequest) (proto.FsReply, error) {
	var reply proto.FsReply
	err := c.client.Call(ctx, "owner_fs_read", "", struct {
		Op      proto.FsOperation `json:"op"`
		Request proto.FsRequest   `json:"request"`
	}{
		Op:      op,
		Request: request,
	}, &reply)
	return reply, err
}

func (c *ownerFileRPCClient) Apply(ctx context.Context, request proto.OwnerFileApplyRequest) error {
	return c.client.Call(ctx, "owner_fs_apply", "", request, nil)
}

func (c *ownerFileRPCClient) BeginFileUpload(ctx context.Context, request proto.OwnerFileBeginRequest) (proto.OwnerFileBeginResponse, error) {
	var response proto.OwnerFileBeginResponse
	err := c.client.Call(ctx, "owner_fs_begin_file", "", request, &response)
	return response, err
}

func (c *ownerFileRPCClient) ApplyFileChunk(ctx context.Context, request proto.OwnerFileApplyChunkRequest) error {
	return c.client.Call(ctx, "owner_fs_apply_chunk", "", request, nil)
}

func (c *ownerFileRPCClient) CommitFileUpload(ctx context.Context, request proto.OwnerFileCommitRequest) error {
	return c.client.Call(ctx, "owner_fs_commit_file", "", request, nil)
}

func (c *ownerFileRPCClient) AbortFileUpload(ctx context.Context, request proto.OwnerFileAbortRequest) error {
	return c.client.Call(ctx, "owner_fs_abort_file", "", request, nil)
}

func (c *ownerFileRPCClient) Done() <-chan struct{} {
	return c.client.Done()
}

func (c *ownerFileRPCClient) Close() error {
	if c.closer == nil {
		return nil
	}
	return c.closer.Close()
}

func ServeOwnerFileChannel(ctx context.Context, reader io.Reader, writer io.Writer, handler OwnerFileHandler) error {
	bufferedReader := bufio.NewReader(reader)
	bufferedWriter := bufio.NewWriter(writer)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line, err := bufferedReader.ReadBytes('\n')
		if err != nil {
			return err
		}

		var request Request
		if err := json.Unmarshal(line, &request); err != nil {
			if err := writeOwnerFileResponse(bufferedWriter, errorResponse(0, err)); err != nil {
				return err
			}
			continue
		}

		var response Response
		switch request.Method {
		case "owner_fs_read":
			var params struct {
				Op      proto.FsOperation `json:"op"`
				Request proto.FsRequest   `json:"request"`
			}
			if err := decodeParams(request.Params, &params); err != nil {
				response = errorResponse(request.ID, err)
				break
			}
			reply, err := handler.FsRead(ctx, params.Op, params.Request)
			if err != nil {
				response = errorResponse(request.ID, err)
				break
			}
			response = resultResponse(request.ID, reply)
		case "owner_fs_apply":
			var params proto.OwnerFileApplyRequest
			if err := decodeParams(request.Params, &params); err != nil {
				response = errorResponse(request.ID, err)
				break
			}
			if err := handler.Apply(ctx, params); err != nil {
				response = errorResponse(request.ID, err)
				break
			}
			response = resultResponse(request.ID, struct{}{})
		case "owner_fs_begin_file":
			var params proto.OwnerFileBeginRequest
			if err := decodeParams(request.Params, &params); err != nil {
				response = errorResponse(request.ID, err)
				break
			}
			reply, err := handler.BeginFileUpload(ctx, params)
			if err != nil {
				response = errorResponse(request.ID, err)
				break
			}
			response = resultResponse(request.ID, reply)
		case "owner_fs_apply_chunk":
			var params proto.OwnerFileApplyChunkRequest
			if err := decodeParams(request.Params, &params); err != nil {
				response = errorResponse(request.ID, err)
				break
			}
			if err := handler.ApplyFileChunk(ctx, params); err != nil {
				response = errorResponse(request.ID, err)
				break
			}
			response = resultResponse(request.ID, struct{}{})
		case "owner_fs_commit_file":
			var params proto.OwnerFileCommitRequest
			if err := decodeParams(request.Params, &params); err != nil {
				response = errorResponse(request.ID, err)
				break
			}
			if err := handler.CommitFileUpload(ctx, params); err != nil {
				response = errorResponse(request.ID, err)
				break
			}
			response = resultResponse(request.ID, struct{}{})
		case "owner_fs_abort_file":
			var params proto.OwnerFileAbortRequest
			if err := decodeParams(request.Params, &params); err != nil {
				response = errorResponse(request.ID, err)
				break
			}
			if err := handler.AbortFileUpload(ctx, params); err != nil {
				response = errorResponse(request.ID, err)
				break
			}
			response = resultResponse(request.ID, struct{}{})
		default:
			response = errorResponse(request.ID, proto.NewError(proto.ErrInvalidArgument, "unknown owner fs method"))
		}

		if err := writeOwnerFileResponse(bufferedWriter, response); err != nil {
			return err
		}
	}
}

func writeOwnerFileResponse(writer *bufio.Writer, response Response) error {
	line, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if _, err := writer.Write(append(line, '\n')); err != nil {
		return err
	}
	return writer.Flush()
}
