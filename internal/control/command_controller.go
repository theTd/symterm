package control

import (
	"context"
	"fmt"
	"sync"
	"time"

	"symterm/internal/eventstream"
	"symterm/internal/proto"
)

type CommandController struct {
	mu      sync.Mutex
	backend CommandBackend
	now     Clock
	owners  map[string]proto.ProjectKey
	streams map[string]*eventstream.Store[proto.CommandEvent]
}

func newCommandController(backend CommandBackend, now Clock) (*CommandController, error) {
	if _, err := newCommandEventStream(); err != nil {
		return nil, err
	}
	return &CommandController{
		backend: backend,
		now:     now,
		owners:  make(map[string]proto.ProjectKey),
		streams: make(map[string]*eventstream.Store[proto.CommandEvent]),
	}, nil
}

func (c *CommandController) Start(projectKey proto.ProjectKey, command proto.CommandSnapshot) error {
	c.mu.Lock()
	c.owners[command.CommandID] = projectKey
	stream, err := c.streamLocked(command.CommandID)
	if err != nil {
		c.mu.Unlock()
		delete(c.owners, command.CommandID)
		return err
	}
	backend := c.backend
	c.mu.Unlock()

	stream.Append(proto.CommandEvent{
		CommandID: command.CommandID,
		Type:      proto.CommandEventExecStarted,
		Timestamp: command.StartedAt,
		Message:   "command launched",
	})
	if backend != nil {
		backend.Launch(CommandLaunch{
			ProjectKey: projectKey,
			Command:    command,
		})
	}
	return nil
}

func (c *CommandController) ReadOutput(commandID string, request proto.AttachStdioRequest) (proto.AttachStdioResponse, error) {
	projectKey, backend, ok := c.backendForCommand(commandID)
	if !ok {
		return proto.AttachStdioResponse{}, proto.NewError(proto.ErrInvalidArgument, "command output is unavailable")
	}
	return backend.ReadOutput(projectKey, request)
}

func (c *CommandController) WaitOutput(ctx context.Context, commandID string, request proto.AttachStdioRequest) error {
	projectKey, backend, ok := c.backendForCommand(commandID)
	if !ok {
		return proto.NewError(proto.ErrInvalidArgument, "command output is unavailable")
	}
	return backend.WaitOutput(ctx, projectKey, request)
}

func (c *CommandController) OpenStdio(commandID string) error {
	return c.record(commandID, proto.CommandEvent{
		CommandID: commandID,
		Type:      proto.CommandEventAttachStdio,
		Timestamp: c.now(),
		Message:   "stdio attached",
	})
}

func (c *CommandController) DetachStdio(commandID string) error {
	return c.record(commandID, proto.CommandEvent{
		CommandID: commandID,
		Type:      proto.CommandEventDetachStdio,
		Timestamp: c.now(),
		Message:   "stdio detached",
	})
}

func (c *CommandController) Watch(commandID string, sinceCursor uint64) ([]proto.CommandEvent, error) {
	stream, err := c.stream(commandID)
	if err != nil {
		return nil, err
	}
	return stream.EventsSince(sinceCursor)
}

func (c *CommandController) Subscribe(commandID string, sinceCursor uint64) ([]proto.CommandEvent, uint64, <-chan struct{}, func(), error) {
	stream, err := c.stream(commandID)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	events, watcherID, ch, err := stream.Subscribe(sinceCursor)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	return events, watcherID, ch, func() {
		stream.Unsubscribe(watcherID)
	}, nil
}

func (c *CommandController) ResizeTTY(commandID string, columns int, rows int) error {
	projectKey, backend, ok := c.backendForCommand(commandID)
	if !ok {
		return nil
	}
	if err := backend.ResizeTTY(projectKey, commandID, columns, rows); err != nil {
		return err
	}
	return c.record(commandID, proto.CommandEvent{
		CommandID: commandID,
		Type:      proto.CommandEventTTYResized,
		Timestamp: c.now(),
		Message:   fmt.Sprintf("%dx%d", columns, rows),
	})
}

func (c *CommandController) SendSignal(commandID string, name string) error {
	projectKey, backend, ok := c.backendForCommand(commandID)
	if !ok {
		return proto.NewError(proto.ErrInvalidArgument, "command signal is unavailable")
	}
	if err := backend.SendSignal(projectKey, commandID, name); err != nil {
		return err
	}
	return c.record(commandID, proto.CommandEvent{
		CommandID: commandID,
		Type:      proto.CommandEventSignalSent,
		Timestamp: c.now(),
		Message:   name,
	})
}

func (c *CommandController) WriteInput(commandID string, data []byte) error {
	projectKey, backend, ok := c.backendForCommand(commandID)
	if !ok {
		return proto.NewError(proto.ErrInvalidArgument, "command input is unavailable")
	}
	return backend.WriteInput(projectKey, commandID, data)
}

func (c *CommandController) CloseInput(commandID string) error {
	projectKey, backend, ok := c.backendForCommand(commandID)
	if !ok {
		return proto.NewError(proto.ErrInvalidArgument, "command input is unavailable")
	}
	if err := backend.CloseInput(projectKey, commandID); err != nil {
		return err
	}
	return c.record(commandID, proto.CommandEvent{
		CommandID: commandID,
		Type:      proto.CommandEventIOClosed,
		Timestamp: c.now(),
		Message:   "stdin closed",
	})
}

func (c *CommandController) StopProject(projectKey proto.ProjectKey) error {
	if c == nil || c.backend == nil {
		return nil
	}
	return c.backend.StopProject(projectKey)
}

func (c *CommandController) CleanupProject(projectKey proto.ProjectKey) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for commandID, owner := range c.owners {
		if owner != projectKey {
			continue
		}
		delete(c.owners, commandID)
		if stream := c.streams[commandID]; stream != nil {
			stream.CloseAll()
			delete(c.streams, commandID)
		}
	}
}

func (c *CommandController) record(commandID string, event proto.CommandEvent) error {
	return c.recordBatch(commandID, event)
}

func (c *CommandController) recordBatch(commandID string, events ...proto.CommandEvent) error {
	if len(events) == 0 {
		return nil
	}
	stream, err := c.stream(commandID)
	if err != nil {
		return err
	}
	stream.AppendBatch(events...)
	return nil
}

func (c *CommandController) stream(commandID string) (*eventstream.Store[proto.CommandEvent], error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.streamLocked(commandID)
}

func (c *CommandController) streamLocked(commandID string) (*eventstream.Store[proto.CommandEvent], error) {
	stream := c.streams[commandID]
	if stream == nil {
		var err error
		stream, err = newCommandEventStream()
		if err != nil {
			return nil, err
		}
		c.streams[commandID] = stream
	}
	return stream, nil
}

func (c *CommandController) backendForCommand(commandID string) (proto.ProjectKey, CommandBackend, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	projectKey, ok := c.owners[commandID]
	if !ok || c.backend == nil {
		return proto.ProjectKey{}, nil, false
	}
	return projectKey, c.backend, true
}

func valueOrNow(value *time.Time, fallback time.Time) time.Time {
	if value != nil {
		return *value
	}
	return fallback
}
