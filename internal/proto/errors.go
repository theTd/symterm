package proto

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"syscall"
)

func AsError(err error) (*Error, bool) {
	var protoErr *Error
	if !errors.As(err, &protoErr) {
		return nil, false
	}
	return protoErr, true
}

func ErrorFields(err error, fallback ErrorCode) (ErrorCode, string) {
	if err == nil {
		return fallback, ""
	}
	if protoErr, ok := AsError(err); ok {
		code := protoErr.Code
		if code == "" {
			code = fallback
		}
		return code, protoErr.Message
	}
	if errors.Is(err, fs.ErrNotExist) {
		return ErrUnknownFile, err.Error()
	}
	if errors.Is(err, fs.ErrPermission) {
		return ErrPermissionDenied, err.Error()
	}
	return fallback, err.Error()
}

func ErrorFromFields(code string, message string, fallback ErrorCode) *Error {
	if code == "" {
		code = string(fallback)
	}
	return NewError(ErrorCode(code), message)
}

func NormalizeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if _, ok := AsError(err); ok {
		return err
	}
	if IsTransportInterrupted(err) {
		return NewError(ErrTransportInterrupted, err.Error())
	}
	return err
}

func IsTransportInterrupted(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, os.ErrClosed) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED)
}
