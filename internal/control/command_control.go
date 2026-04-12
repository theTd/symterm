package control

import (
	"context"
	"errors"

	"symterm/internal/project"
	"symterm/internal/proto"
)

func (s *Service) StartCommand(clientID string, request proto.StartCommandRequest) (proto.StartCommandResponse, error) {
	instance, clientSession, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return proto.StartCommandResponse{}, err
	}
	if request.ProjectID != clientSession.ProjectID {
		return proto.StartCommandResponse{}, proto.NewError(proto.ErrInvalidArgument, "project id does not match the authenticated session")
	}

	command, err := instance.StartCommand(clientID, request.ArgvTail, request.TTY, request.TmuxStatus, s.now())
	if err != nil {
		return proto.StartCommandResponse{}, err
	}
	if err := s.commands.Start(projectKeyForSession(clientSession), command); err != nil {
		return proto.StartCommandResponse{}, err
	}
	s.publishProjectSessions(projectKeyForSession(clientSession))
	return proto.StartCommandResponse{CommandID: command.CommandID}, nil
}

func (s *Service) AttachStdio(clientID string, request proto.AttachStdioRequest) (proto.AttachStdioResponse, error) {
	instance, _, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return proto.AttachStdioResponse{}, err
	}
	if err := ensureCommandControlAllowed(instance, clientID); err != nil {
		return proto.AttachStdioResponse{}, err
	}

	command, err := instance.Command(request.CommandID)
	if err != nil {
		return proto.AttachStdioResponse{}, err
	}
	output, err := s.commands.ReadOutput(request.CommandID, request)
	if err != nil {
		return proto.AttachStdioResponse{}, err
	}
	if !output.Complete && isTerminalCommandState(command.State) {
		output.Complete = true
	}
	return output, nil
}

func (s *Service) WaitCommandOutput(ctx context.Context, clientID string, request proto.AttachStdioRequest) error {
	instance, _, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return err
	}
	if err := ensureCommandControlAllowed(instance, clientID); err != nil {
		return err
	}

	command, err := instance.Command(request.CommandID)
	if err != nil {
		return err
	}
	if isTerminalCommandState(command.State) {
		return nil
	}
	return s.commands.WaitOutput(ctx, request.CommandID, request)
}

func (s *Service) OpenStdio(clientID string, commandID string) error {
	if err := s.ensureStdioControlAllowed(clientID, commandID); err != nil {
		return err
	}
	return s.commands.OpenStdio(commandID)
}

func (s *Service) DetachStdio(clientID string, commandID string) error {
	if err := s.ensureStdioControlAllowed(clientID, commandID); err != nil {
		return err
	}
	return s.commands.DetachStdio(commandID)
}

func (s *Service) ensureStdioControlAllowed(clientID string, commandID string) error {
	instance, _, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return err
	}
	if err := ensureCommandControlAllowed(instance, clientID); err != nil {
		return err
	}
	if _, err := runningCommand(instance, commandID); err != nil {
		return err
	}
	return nil
}

func (s *Service) WatchCommand(clientID string, request proto.WatchCommandRequest) ([]proto.CommandEvent, error) {
	instance, _, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return nil, err
	}
	if err := ensureCommandControlAllowed(instance, clientID); err != nil {
		return nil, err
	}
	if _, err := instance.Command(request.CommandID); err != nil {
		return nil, err
	}
	return s.commands.Watch(request.CommandID, request.SinceCursor)
}

func (s *Service) SubscribeCommandEvents(clientID string, request proto.WatchCommandRequest) ([]proto.CommandEvent, uint64, <-chan struct{}, func(), error) {
	instance, _, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	if err := ensureCommandControlAllowed(instance, clientID); err != nil {
		return nil, 0, nil, nil, err
	}
	if _, err := instance.Command(request.CommandID); err != nil {
		return nil, 0, nil, nil, err
	}
	return s.commands.Subscribe(request.CommandID, request.SinceCursor)
}

func (s *Service) ResizeTTY(clientID string, request proto.ResizeTTYRequest) (proto.ResizeTTYResponse, error) {
	instance, _, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return proto.ResizeTTYResponse{}, err
	}
	if err := ensureCommandControlAllowed(instance, clientID); err != nil {
		return proto.ResizeTTYResponse{}, err
	}
	if _, err := runningCommand(instance, request.CommandID); err != nil {
		return proto.ResizeTTYResponse{}, err
	}
	if err := s.commands.ResizeTTY(request.CommandID, request.Columns, request.Rows); err != nil {
		return proto.ResizeTTYResponse{}, err
	}
	return proto.ResizeTTYResponse{Applied: true}, nil
}

func (s *Service) SendSignal(clientID string, request proto.SendSignalRequest) (proto.SendSignalResponse, error) {
	instance, _, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return proto.SendSignalResponse{}, err
	}
	if err := ensureCommandControlAllowed(instance, clientID); err != nil {
		return proto.SendSignalResponse{}, err
	}
	if _, err := runningCommand(instance, request.CommandID); err != nil {
		return proto.SendSignalResponse{}, err
	}
	if err := s.commands.SendSignal(request.CommandID, request.Name); err != nil {
		return proto.SendSignalResponse{}, err
	}
	return proto.SendSignalResponse{Delivered: true}, nil
}

func (s *Service) WriteCommandInput(clientID string, commandID string, data []byte) error {
	instance, _, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return err
	}
	if err := ensureCommandControlAllowed(instance, clientID); err != nil {
		return err
	}
	command, err := instance.Command(commandID)
	if err != nil {
		return err
	}
	if err := s.commands.WriteInput(commandID, data); err != nil {
		if isStaleInputError(command, err) {
			return nil
		}
		return err
	}
	return nil
}

func (s *Service) CloseCommandInput(clientID string, commandID string) error {
	instance, _, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return err
	}
	if err := ensureCommandControlAllowed(instance, clientID); err != nil {
		return err
	}
	command, err := instance.Command(commandID)
	if err != nil {
		return err
	}
	if err := s.commands.CloseInput(commandID); err != nil {
		if isStaleInputError(command, err) {
			return nil
		}
		return err
	}
	return nil
}

func ensureCommandControlAllowed(instance *project.Instance, clientID string) error {
	state, _, err := instance.SyncState(clientID)
	if err != nil {
		return err
	}
	if state == proto.ProjectStateTerminating || state == proto.ProjectStateTerminated {
		return proto.NewError(proto.ErrProjectTerminated, "project instance has already terminated")
	}
	return nil
}

func (s *Service) CompleteCommand(projectKey proto.ProjectKey, commandID string, exitCode int) error {
	instance, err := s.projects.InstanceForKey(projectKey)
	if err != nil {
		return err
	}
	command, err := instance.FinishCommand(commandID, exitCode, s.now())
	if err != nil {
		return err
	}
	if err := s.recordCommandTerminalEvents(command); err != nil {
		return err
	}
	s.publishProjectSessions(projectKey)
	return nil
}

func (s *Service) FailCommand(projectKey proto.ProjectKey, commandID string, reason string) error {
	instance, err := s.projects.InstanceForKey(projectKey)
	if err != nil {
		return err
	}
	command, err := instance.FailCommand(commandID, reason, s.now())
	if err != nil {
		return err
	}
	if err := s.recordCommandTerminalEvents(command); err != nil {
		return err
	}
	s.publishProjectSessions(projectKey)
	return nil
}

func (s *Service) recordCommandTerminalEvents(command proto.CommandSnapshot) error {
	timestamp := valueOrNow(command.ExitedAt, s.now())
	switch command.State {
	case proto.CommandStateExited:
		return s.commands.recordBatch(command.CommandID,
			proto.CommandEvent{
				CommandID: command.CommandID,
				Type:      proto.CommandEventExited,
				Timestamp: timestamp,
				ExitCode:  command.ExitCode,
				Message:   "command exited",
			},
			proto.CommandEvent{
				CommandID: command.CommandID,
				Type:      proto.CommandEventIOClosed,
				Timestamp: timestamp,
				Message:   "command exited",
			},
		)
	case proto.CommandStateFailed:
		return s.commands.recordBatch(command.CommandID,
			proto.CommandEvent{
				CommandID: command.CommandID,
				Type:      proto.CommandEventExecFailed,
				Timestamp: timestamp,
				Message:   command.FailureReason,
			},
			proto.CommandEvent{
				CommandID: command.CommandID,
				Type:      proto.CommandEventIOClosed,
				Timestamp: timestamp,
				Message:   "exec failed",
			},
		)
	default:
		return nil
	}
}

func isStaleInputError(_ proto.CommandSnapshot, err error) bool {
	var protoErr *proto.Error
	if !errors.As(err, &protoErr) {
		return false
	}
	return protoErr.Code == proto.ErrUnknownCommand
}

func runningCommand(instance *project.Instance, commandID string) (proto.CommandSnapshot, error) {
	command, err := instance.Command(commandID)
	if err != nil {
		return proto.CommandSnapshot{}, err
	}
	if command.State != proto.CommandStateRunning {
		return proto.CommandSnapshot{}, proto.NewError(proto.ErrUnknownCommand, "command is not running")
	}
	return command, nil
}

func isTerminalCommandState(state proto.CommandState) bool {
	switch state {
	case proto.CommandStateExited, proto.CommandStateFailed:
		return true
	default:
		return false
	}
}
