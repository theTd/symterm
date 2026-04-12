package transport

import (
	"io"
	"net"
	"strings"
	"time"

	"symterm/internal/control"
)

type countingReader struct {
	reader   io.Reader
	counters *control.TrafficCounters
}

func (r countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.counters.AddIn(n)
	return n, err
}

type countingWriter struct {
	writer   io.Writer
	counters *control.TrafficCounters
}

func (w countingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.counters.AddOut(n)
	return n, err
}

func detectConnMeta(reader io.Reader, writer io.Writer) control.ConnMeta {
	meta := control.ConnMeta{
		TransportKind: control.TransportKindUnknown,
		RemoteAddr:    "unknown",
		LocalAddr:     "unknown",
		ConnectedAt:   time.Now().UTC(),
	}
	if conn, ok := writer.(net.Conn); ok {
		meta.TransportKind = control.TransportKindSSH
		meta.RemoteAddr = normalizeSocketAddr(conn.RemoteAddr())
		meta.LocalAddr = normalizeSocketAddr(conn.LocalAddr())
		return meta
	}
	if conn, ok := reader.(net.Conn); ok {
		meta.TransportKind = control.TransportKindSSH
		meta.RemoteAddr = normalizeSocketAddr(conn.RemoteAddr())
		meta.LocalAddr = normalizeSocketAddr(conn.LocalAddr())
	}
	return meta
}

func normalizeSocketAddr(addr net.Addr) string {
	if addr == nil {
		return "unknown"
	}
	value := strings.TrimSpace(addr.String())
	if value == "" {
		return "unknown"
	}
	return value
}
