package client

import (
	"io"
	"log"
)

type traceLogger struct {
	logger *log.Logger
}

func newTraceLogger(enabled bool, writer io.Writer) traceLogger {
	if !enabled || writer == nil {
		return traceLogger{}
	}
	return traceLogger{
		logger: log.New(writer, "symterm trace: ", log.LstdFlags|log.Lmicroseconds),
	}
}

func (t traceLogger) Enabled() bool {
	return t.logger != nil
}

func (t traceLogger) Printf(format string, args ...any) {
	if t.logger == nil {
		return
	}
	t.logger.Printf(format, args...)
}
