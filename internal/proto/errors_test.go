package proto

import (
	"context"
	errors "errors"
	"io"
	"io/fs"
	"testing"
)

func TestErrorFieldsUsesProtoError(t *testing.T) {
	t.Parallel()

	code, message := ErrorFields(NewError(ErrNeedsConfirmation, "blocked"), ErrInvalidArgument)
	if code != ErrNeedsConfirmation {
		t.Fatalf("code = %q, want %q", code, ErrNeedsConfirmation)
	}
	if message != "blocked" {
		t.Fatalf("message = %q, want blocked", message)
	}
}

func TestErrorFromFieldsFallsBackWhenCodeMissing(t *testing.T) {
	t.Parallel()

	err := ErrorFromFields("", "bad request", ErrInvalidArgument)
	if err.Code != ErrInvalidArgument {
		t.Fatalf("code = %q, want %q", err.Code, ErrInvalidArgument)
	}
	if err.Message != "bad request" {
		t.Fatalf("message = %q, want bad request", err.Message)
	}
}

func TestNormalizeErrorMapsInterruptedTransport(t *testing.T) {
	t.Parallel()

	err := NormalizeError(io.EOF)
	protoErr, ok := AsError(err)
	if !ok {
		t.Fatalf("NormalizeError(io.EOF) = %T, want *Error", err)
	}
	if protoErr.Code != ErrTransportInterrupted {
		t.Fatalf("code = %q, want %q", protoErr.Code, ErrTransportInterrupted)
	}
}

func TestNormalizeErrorPreservesContextCancellation(t *testing.T) {
	t.Parallel()

	err := NormalizeError(context.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("NormalizeError(context.Canceled) = %v, want context.Canceled", err)
	}
}

func TestErrorFieldsMapsMissingPathToUnknownFile(t *testing.T) {
	t.Parallel()

	code, message := ErrorFields(fs.ErrNotExist, ErrInvalidArgument)
	if code != ErrUnknownFile {
		t.Fatalf("code = %q, want %q", code, ErrUnknownFile)
	}
	if message == "" {
		t.Fatal("message is empty")
	}
}

func TestErrorFieldsMapsPermissionToPermissionDenied(t *testing.T) {
	t.Parallel()

	code, message := ErrorFields(fs.ErrPermission, ErrInvalidArgument)
	if code != ErrPermissionDenied {
		t.Fatalf("code = %q, want %q", code, ErrPermissionDenied)
	}
	if message == "" {
		t.Fatal("message is empty")
	}
}
