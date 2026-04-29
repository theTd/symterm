package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"symterm/internal/control"
	"symterm/internal/proto"
)

func ServeStdioChannel(
	ctx context.Context,
	service control.ClientService,
	clientID string,
	commandID string,
	reader io.Reader,
	writer io.Writer,
	closer io.Closer,
	connMeta control.ConnMeta,
	tracef func(string, ...any),
) error {
	counters := &control.TrafficCounters{}
	bufferedReader := bufio.NewReader(countingReader{reader: reader, counters: counters})
	bufferedWriter := bufio.NewWriter(countingWriter{writer: writer, counters: counters})
	channelID, err := service.AttachSessionChannel(clientID, connMeta, counters, closer)
	if err != nil {
		return writeStdioChannelError(bufferedWriter, err)
	}
	defer service.DetachSessionChannel(clientID, channelID)

	liveAttach := true
	if err := service.OpenStdio(clientID, commandID); err != nil {
		if !isPostExitStdioRead(err) {
			return writeStdioChannelError(bufferedWriter, err)
		}
		liveAttach = false
	}
	if liveAttach {
		defer func() {
			service.DetachStdio(clientID, commandID)
		}()
	}

	inputErrCh := make(chan error, 1)
	go func() {
		inputErrCh <- consumeStdioPipeInput(ctx, service, bufferedReader, clientID, commandID)
	}()

	params := proto.AttachStdioRequest{CommandID: commandID}
	for {
		select {
		case err := <-inputErrCh:
			if err != nil {
				return writeStdioChannelError(bufferedWriter, err)
			}
			inputErrCh = nil
		default:
		}

		result, err := service.AttachStdio(clientID, params)
		if err != nil {
			return writeStdioChannelError(bufferedWriter, err)
		}
		service.NoteSessionActivity(clientID)
		if len(result.Stdout) > 0 {
			if err := writeStdioFrame(bufferedWriter, stdioFrameStdout, result.Stdout); err != nil {
				return err
			}
			if err := bufferedWriter.Flush(); err != nil {
				return err
			}
		}
		if len(result.Stderr) > 0 {
			if err := writeStdioFrame(bufferedWriter, stdioFrameStderr, result.Stderr); err != nil {
				return err
			}
			if err := bufferedWriter.Flush(); err != nil {
				return err
			}
		}
		if result.Complete {
			if err := writeStdioFrame(bufferedWriter, stdioFrameEnd, nil); err != nil {
				return err
			}
			return bufferedWriter.Flush()
		}
		params.StdoutOffset = result.StdoutOffset
		params.StderrOffset = result.StderrOffset
		waitCtx, cancelWait := context.WithCancel(ctx)
		outputWaitCh := make(chan error, 1)
		go func(waitParams proto.AttachStdioRequest) {
			outputWaitCh <- service.WaitCommandOutput(waitCtx, clientID, waitParams)
		}(params)
		select {
		case <-ctx.Done():
			cancelWait()
			return ctx.Err()
		case err := <-inputErrCh:
			cancelWait()
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				return writeStdioChannelError(bufferedWriter, err)
			}
			inputErrCh = nil
		case err := <-outputWaitCh:
			cancelWait()
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				return writeStdioChannelError(bufferedWriter, err)
			}
		}
		if tracef != nil {
			tracef("stdio channel wait completed client_id=%s command_id=%s stdout_offset=%d stderr_offset=%d", clientID, commandID, params.StdoutOffset, params.StderrOffset)
		}
	}
}

func writeStdioChannelError(writer *bufio.Writer, err error) error {
	if err == nil {
		err = errors.New("stdio pipe error")
	}
	code, message := proto.ErrorFields(err, proto.ErrInvalidArgument)
	responseErr := ResponseError{
		Code:    string(code),
		Message: message,
	}
	payload, marshalErr := json.Marshal(responseErr)
	if marshalErr != nil {
		return marshalErr
	}
	if writeErr := writeStdioFrame(writer, stdioFrameError, payload); writeErr != nil {
		return writeErr
	}
	return writer.Flush()
}

func consumeStdioPipeInput(ctx context.Context, service control.ClientService, reader *bufio.Reader, clientID string, commandID string) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		frameType, payload, err := readStdioFrame(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch frameType {
		case stdioFrameStdin:
			service.NoteSessionActivity(clientID)
			if err := service.WriteCommandInput(clientID, commandID, payload); err != nil {
				return err
			}
		case stdioFrameClose:
			service.NoteSessionActivity(clientID)
			return service.CloseCommandInput(clientID, commandID)
		default:
			return fmt.Errorf("unsupported stdio input frame type %d", frameType)
		}
	}
}
