package diagnostic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
)

type bufferReporter struct {
	buf *bytes.Buffer
}

func (r bufferReporter) Errorf(format string, args ...any) {
	if r.buf == nil {
		return
	}
	r.buf.WriteString(strings.TrimSpace(fmt.Sprintf(format, args...)))
	r.buf.WriteByte('\n')
}

func TestCleanupIgnoresClosedNetworkConnection(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	Cleanup(bufferReporter{buf: &buf}, "close listener", net.ErrClosed)
	if buf.Len() != 0 {
		t.Fatalf("Cleanup() logged %q for net.ErrClosed", buf.String())
	}
}

func TestBackgroundIgnoresContextCancellation(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	Background(bufferReporter{buf: &buf}, "watch project stream", context.Canceled)
	if buf.Len() != 0 {
		t.Fatalf("Background() logged %q for context cancellation", buf.String())
	}
}

func TestErrorLogsUnexpectedFailure(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	Error(bufferReporter{buf: &buf}, "stop project", errors.New("boom"))
	if !strings.Contains(buf.String(), "stop project") || !strings.Contains(buf.String(), "boom") {
		t.Fatalf("Error() log = %q, want activity and error", buf.String())
	}
}
