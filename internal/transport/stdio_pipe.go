package transport

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"symterm/internal/diagnostic"
	"symterm/internal/proto"
)

const (
	stdioFrameStdout byte = 1
	stdioFrameStderr byte = 2
	stdioFrameEnd    byte = 3
	stdioFrameStdin  byte = 4
	stdioFrameClose  byte = 5
	stdioFrameError  byte = 6
)

type StdioPipeClient struct {
	mu          sync.Mutex
	reader      *bufio.Reader
	writer      *bufio.Writer
	openChannel sshStdioChannelOpener
}

func NewSSHStdioPipeClient(openChannel sshStdioChannelOpener) *StdioPipeClient {
	return &StdioPipeClient{
		openChannel: openChannel,
	}
}

func (c *StdioPipeClient) StreamAttach(
	ctx context.Context,
	clientID string,
	request Request,
	stdin io.Reader,
	onStdout func([]byte) error,
	onStderr func([]byte) error,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	reader := c.reader
	writer := c.writer
	closeChannel := func() {}
	if c.openChannel != nil {
		var params proto.AttachStdioRequest
		if err := decodeParams(request.Params, &params); err != nil {
			return err
		}
		conn, err := c.openChannel(ctx, clientID, params.CommandID)
		if err != nil {
			return err
		}
		reader = bufio.NewReader(conn)
		writer = bufio.NewWriter(conn)
		closeChannel = func() {
			diagnostic.Cleanup(diagnostic.Default(), "close SSH stdio channel", conn.Close())
		}
	} else {
		request.ClientID = clientID
		line, err := json.Marshal(request)
		if err != nil {
			return err
		}
		if _, err := writer.Write(append(line, '\n')); err != nil {
			return proto.NormalizeError(err)
		}
		if err := writer.Flush(); err != nil {
			return proto.NormalizeError(err)
		}
	}
	defer closeChannel()

	if stdin != nil {
		go c.streamInput(ctx, stdin, writer)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		frameType, payload, err := readStdioFrame(reader)
		if err != nil {
			return proto.NormalizeError(err)
		}
		switch frameType {
		case stdioFrameStdout:
			if err := onStdout(payload); err != nil {
				return err
			}
		case stdioFrameStderr:
			if err := onStderr(payload); err != nil {
				return err
			}
		case stdioFrameEnd:
			return nil
		case stdioFrameError:
			if len(payload) == 0 {
				return errors.New("remote stdio pipe error")
			}
			var responseErr ResponseError
			if err := json.Unmarshal(payload, &responseErr); err == nil && responseErr.Code != "" {
				return proto.ErrorFromFields(responseErr.Code, responseErr.Message, proto.ErrInvalidArgument)
			}
			return errors.New(string(payload))
		default:
			return errors.New("unknown stdio frame type")
		}
	}
}

func (c *StdioPipeClient) streamInput(ctx context.Context, stdin io.Reader, writer *bufio.Writer) {
	buf := make([]byte, 32*1024)
	for {
		n, err := stdin.Read(buf)
		if n > 0 {
			if writeErr := writeStdioFrame(writer, stdioFrameStdin, buf[:n]); writeErr != nil {
				return
			}
			if writeErr := writer.Flush(); writeErr != nil {
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				diagnostic.Cleanup(diagnostic.Default(), "write stdio close frame", writeStdioFrame(writer, stdioFrameClose, nil))
				diagnostic.Cleanup(diagnostic.Default(), "flush stdio close frame", writer.Flush())
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func writeStdioFrame(writer io.Writer, frameType byte, payload []byte) error {
	header := make([]byte, 5)
	header[0] = frameType
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if _, err := writer.Write(header); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := writer.Write(payload)
	return err
}

func readStdioFrame(reader io.Reader) (byte, []byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, nil, err
	}
	size := binary.BigEndian.Uint32(header[1:])
	payload := make([]byte, size)
	if size > 0 {
		if _, err := io.ReadFull(reader, payload); err != nil {
			return 0, nil, err
		}
	}
	return header[0], payload, nil
}
