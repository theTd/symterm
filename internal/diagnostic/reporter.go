package diagnostic

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"strings"
	"syscall"
)

type Reporter interface {
	Errorf(format string, args ...any)
}

type loggerReporter struct {
	logger *log.Logger
}

func (r loggerReporter) Errorf(format string, args ...any) {
	if r.logger == nil {
		return
	}
	r.logger.Printf(format, args...)
}

type nopReporter struct{}

func (nopReporter) Errorf(string, ...any) {}

var defaultReporter Reporter = loggerReporter{
	logger: log.New(os.Stderr, "symterm diagnostic: ", log.LstdFlags),
}

func Default() Reporter {
	return defaultReporter
}

func Nop() Reporter {
	return nopReporter{}
}

func Error(reporter Reporter, activity string, err error) {
	report(reporter, activity, err, false)
}

func Background(reporter Reporter, activity string, err error) {
	report(reporter, activity, err, true)
}

func Cleanup(reporter Reporter, activity string, err error) {
	if isIgnorableCleanupError(err) {
		return
	}
	report(reporter, activity, err, true)
}

func report(reporter Reporter, activity string, err error, ignoreTransient bool) {
	if err == nil {
		return
	}
	if ignoreTransient && isIgnorableBackgroundError(err) {
		return
	}
	if reporter == nil {
		reporter = Default()
	}
	reporter.Errorf("%s: %v", strings.TrimSpace(activity), err)
}

func isIgnorableBackgroundError(err error) bool {
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) ||
		isIgnorableCleanupError(err)
}

func isIgnorableCleanupError(err error) bool {
	if err == nil {
		return true
	}
	return errors.Is(err, net.ErrClosed) ||
		errors.Is(err, os.ErrClosed) ||
		errors.Is(err, fs.ErrClosed) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) ||
		strings.Contains(err.Error(), "use of closed network connection") ||
		strings.Contains(err.Error(), "already closed") ||
		strings.Contains(err.Error(), "file already closed")
}
