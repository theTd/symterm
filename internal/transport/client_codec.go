package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"symterm/internal/proto"
)

// DefaultClientIdleTimeout bounds how long Client.readLoop will block on a
// silently dead peer (no FIN/RST, no bytes, no JSON-RPC reply). When the
// timeout elapses, the underlying closer (if any) is closed which forces the
// blocked ReadBytes to return and unwedges every pending caller. Long-lived
// JSON-RPC streams that legitimately idle for longer than this should arrange
// their own keepalive at the SSH or application layer.
const DefaultClientIdleTimeout = 5 * time.Minute

type Client struct {
	mu           sync.Mutex
	nextID       uint64
	reader       *bufio.Reader
	writer       *bufio.Writer
	writeMu      sync.Mutex
	pending      map[uint64]chan Response
	readLoopOnce sync.Once
	done         chan struct{}
	doneOnce     sync.Once
	readErr      error
	closer       io.Closer
	idleTimeout  time.Duration
}

func NewClient(reader io.Reader, writer io.Writer) *Client {
	client := &Client{
		reader:  bufio.NewReader(reader),
		writer:  bufio.NewWriter(writer),
		pending: make(map[uint64]chan Response),
		done:    make(chan struct{}),
	}
	client.ensureReadLoop()
	return client
}

// NewClientWithIdleTimeout returns a Client that closes the supplied closer
// when no bytes have been read from the underlying reader for idleTimeout. A
// zero idleTimeout disables the watchdog; a nil closer is also a no-op (the
// caller must supply something that can sever the connection for the timeout
// to actually unwedge a stuck readLoop).
func NewClientWithIdleTimeout(reader io.Reader, writer io.Writer, closer io.Closer, idleTimeout time.Duration) *Client {
	client := &Client{
		reader:      bufio.NewReader(reader),
		writer:      bufio.NewWriter(writer),
		pending:     make(map[uint64]chan Response),
		done:        make(chan struct{}),
		closer:      closer,
		idleTimeout: idleTimeout,
	}
	client.ensureReadLoop()
	return client
}

func (c *Client) Call(ctx context.Context, method string, clientID string, params any, result any) error {
	return c.CallWithBinaryPayload(ctx, method, clientID, params, result, nil)
}

func (c *Client) CallWithBinaryPayload(ctx context.Context, method string, clientID string, params any, result any, payload []byte) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	request, replies, err := c.prepareRequest(method, clientID, params)
	if err != nil {
		return err
	}
	defer c.unregisterPending(request.ID)

	if err := c.writeRequest(request); err != nil {
		return err
	}
	if len(payload) > 0 {
		c.writeMu.Lock()
		if _, err := c.writer.Write(payload); err != nil {
			c.writeMu.Unlock()
			return proto.NormalizeError(err)
		}
		if err := c.writer.Flush(); err != nil {
			c.writeMu.Unlock()
			return proto.NormalizeError(err)
		}
		c.writeMu.Unlock()
	}

	response, err := c.awaitResponse(ctx, request.ID, replies)
	if err != nil {
		return err
	}
	if result == nil || len(response.Result) == 0 {
		return nil
	}
	return json.Unmarshal(response.Result, result)
}

func (c *Client) Done() <-chan struct{} {
	c.ensureReadLoop()
	return c.done
}

func (c *Client) StreamProjectEvents(
	ctx context.Context,
	clientID string,
	request proto.WatchProjectRequest,
	onEvent func(proto.ProjectEvent) error,
) error {
	return streamEvents(c, ctx, "watch_project_stream", clientID, request, onEvent)
}

func (c *Client) StreamCommandEvents(
	ctx context.Context,
	clientID string,
	request proto.WatchCommandRequest,
	onEvent func(proto.CommandEvent) error,
) error {
	return streamEvents(c, ctx, "watch_command_stream", clientID, request, onEvent)
}

func (c *Client) StreamInvalidateEvents(
	ctx context.Context,
	clientID string,
	request proto.WatchInvalidateRequest,
	onEvent func(proto.InvalidateEvent) error,
) error {
	return streamEvents(c, ctx, internalWatchInvalidateMethod+"_stream", clientID, request, onEvent)
}

func (c *Client) ReportSyncProgress(ctx context.Context, clientID string, request proto.ReportSyncProgressRequest) error {
	return c.Call(ctx, internalReportSyncProgressMethod, clientID, request, nil)
}

func streamEvents[T any](
	c *Client,
	ctx context.Context,
	method string,
	clientID string,
	params any,
	onEvent func(T) error,
) error {
	return c.stream(ctx, method, clientID, params, func(raw json.RawMessage) error {
		var item StreamItem[T]
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		if item.Done {
			return io.EOF
		}
		if item.Event == nil {
			return nil
		}
		return onEvent(*item.Event)
	})
}

func (c *Client) stream(
	ctx context.Context,
	method string,
	clientID string,
	params any,
	onItem func(json.RawMessage) error,
) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	request, replies, err := c.prepareRequest(method, clientID, params)
	if err != nil {
		return err
	}
	defer c.unregisterPending(request.ID)

	if err := c.writeRequest(request); err != nil {
		return err
	}

	for {
		response, err := c.awaitResponse(ctx, request.ID, replies)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if len(response.Result) == 0 {
			continue
		}
		if err := onItem(response.Result); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func (c *Client) prepareRequest(method string, clientID string, params any) (Request, chan Response, error) {
	c.ensureReadLoop()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.readErr != nil {
		return Request{}, nil, proto.NormalizeError(c.readErr)
	}

	c.nextID++
	request := Request{
		ID:       c.nextID,
		Method:   method,
		ClientID: clientID,
	}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return Request{}, nil, err
		}
		request.Params = raw
	}

	replies := make(chan Response, 32)
	c.pending[request.ID] = replies
	return request, replies, nil
}

func (c *Client) writeRequest(request Request) error {
	line, err := json.Marshal(request)
	if err != nil {
		return err
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if _, err := c.writer.Write(append(line, '\n')); err != nil {
		return proto.NormalizeError(err)
	}
	return proto.NormalizeError(c.writer.Flush())
}

func (c *Client) awaitResponse(ctx context.Context, requestID uint64, replies <-chan Response) (Response, error) {
	select {
	case <-ctx.Done():
		return Response{}, ctx.Err()
	case response, ok := <-replies:
		if !ok {
			c.mu.Lock()
			err := c.readErr
			c.mu.Unlock()
			if err == nil {
				err = io.EOF
			}
			return Response{}, proto.NormalizeError(err)
		}
		if response.ID != requestID {
			return Response{}, fmt.Errorf("unexpected response id: got %d want %d", response.ID, requestID)
		}
		if response.Error != nil {
			return Response{}, proto.ErrorFromFields(response.Error.Code, response.Error.Message, proto.ErrInvalidArgument)
		}
		return response, nil
	}
}

func (c *Client) unregisterPending(requestID uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.pending, requestID)
}

func (c *Client) ensureReadLoop() {
	c.readLoopOnce.Do(func() {
		go c.readLoop()
	})
}

func (c *Client) readLoop() {
	for {
		var watchdog *time.Timer
		if c.idleTimeout > 0 && c.closer != nil {
			watchdog = time.AfterFunc(c.idleTimeout, func() {
				if !c.hasPendingCalls() {
					return
				}
				_ = c.closer.Close()
			})
		}
		replyLine, err := c.reader.ReadBytes('\n')
		if watchdog != nil {
			watchdog.Stop()
		}
		if err != nil {
			c.failPending(err)
			return
		}

		var response Response
		if err := json.Unmarshal(replyLine, &response); err != nil {
			c.failPending(err)
			return
		}

		c.mu.Lock()
		replies := c.pending[response.ID]
		c.mu.Unlock()
		if replies == nil {
			continue
		}
		replies <- response
	}
}

func (c *Client) hasPendingCalls() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending) > 0
}

func (c *Client) failPending(err error) {
	if err == nil {
		err = io.EOF
	}

	c.mu.Lock()
	if c.readErr != nil {
		c.mu.Unlock()
		return
	}
	c.readErr = proto.NormalizeError(err)
	pending := c.pending
	c.pending = make(map[uint64]chan Response)
	c.mu.Unlock()

	for _, replies := range pending {
		close(replies)
	}
	c.doneOnce.Do(func() {
		close(c.done)
	})
}
