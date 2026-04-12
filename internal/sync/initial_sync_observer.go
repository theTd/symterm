package sync

import (
	"fmt"
	"strings"

	"symterm/internal/proto"
)

type InitialSyncObserver struct {
	OnOperation func(string)
	OnProgress  func(proto.SyncProgress)
}

func (o *InitialSyncObserver) Operation(message string) {
	if o == nil || o.OnOperation == nil {
		return
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	o.OnOperation(message)
}

func (o *InitialSyncObserver) Operationf(format string, args ...any) {
	if o == nil || o.OnOperation == nil {
		return
	}
	o.Operation(fmt.Sprintf(format, args...))
}

func (o *InitialSyncObserver) Progress(progress proto.SyncProgress) {
	if o == nil || o.OnProgress == nil {
		return
	}
	o.OnProgress(progress)
}
