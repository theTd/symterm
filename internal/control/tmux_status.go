package control

import (
	"strings"

	"symterm/internal/proto"
)

func (s *Service) TmuxStatus(clientID string, commandID string) (proto.TmuxStatusSnapshot, error) {
	if strings.TrimSpace(clientID) == "" {
		return proto.TmuxStatusSnapshot{}, proto.NewError(proto.ErrInvalidArgument, "client id is required")
	}
	if strings.TrimSpace(commandID) == "" {
		return proto.TmuxStatusSnapshot{}, proto.NewError(proto.ErrInvalidArgument, "command id is required")
	}

	sessionSnapshot, ok := s.sessions.ClientSnapshot(clientID)
	if !ok {
		return proto.TmuxStatusSnapshot{}, proto.NewError(proto.ErrUnknownClient, "client session does not exist")
	}
	sessionSnapshot = s.enrichSessionSnapshot(sessionSnapshot)

	instance, _, err := s.projects.InstanceForClient(s.sessions, clientID)
	if err != nil {
		return proto.TmuxStatusSnapshot{}, err
	}
	command, err := instance.Command(commandID)
	if err != nil {
		return proto.TmuxStatusSnapshot{}, err
	}

	return proto.TmuxStatusSnapshot{
		ClientID:         clientID,
		ProjectID:        sessionSnapshot.ProjectID,
		Role:             sessionSnapshot.Role,
		CommandID:        commandID,
		CommandState:     command.State,
		ProjectState:     sessionSnapshot.ProjectState,
		ControlConnected: sessionSnapshot.Control != nil,
		StdioConnected:   sessionHasChannelKind(sessionSnapshot, ChannelKindStdio),
		StdioBytesIn:     sessionSnapshot.StdioBytesIn,
		StdioBytesOut:    sessionSnapshot.StdioBytesOut,
		LastActivityAt:   sessionSnapshot.LastActivityAt,
		AttachedCommands: sessionSnapshot.AttachedCommandCount,
	}, nil
}

func sessionHasChannelKind(snapshot LiveSessionSnapshot, kind ChannelKind) bool {
	for _, channel := range snapshot.Channels {
		if channel.Meta.ChannelKind == kind {
			return true
		}
	}
	return false
}
