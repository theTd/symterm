package admin

import (
	"time"

	"symterm/internal/proto"
)

type SessionPrincipal struct {
	Username        string    `json:"username"`
	UserDisabled    bool      `json:"user_disabled"`
	TokenID         string    `json:"token_id"`
	TokenSource     string    `json:"token_source"`
	AuthenticatedAt time.Time `json:"authenticated_at"`
}

type SessionChannelMeta struct {
	TransportKind string    `json:"transport_kind"`
	RemoteAddr    string    `json:"remote_addr"`
	LocalAddr     string    `json:"local_addr"`
	ConnectedAt   time.Time `json:"connected_at"`
	ChannelKind   string    `json:"channel_kind"`
}

type SessionChannelSnapshot struct {
	ChannelID string             `json:"channel_id"`
	Meta      SessionChannelMeta `json:"meta"`
	BytesIn   uint64             `json:"bytes_in"`
	BytesOut  uint64             `json:"bytes_out"`
}

type SessionSnapshot struct {
	SessionID            string                   `json:"session_id"`
	ClientID             string                   `json:"client_id"`
	ProjectID            string                   `json:"project_id"`
	WorkspaceRoot        string                   `json:"workspace_root"`
	WorkspaceDigest      string                   `json:"workspace_digest"`
	Principal            SessionPrincipal         `json:"principal"`
	ConnectedAt          time.Time                `json:"connected_at"`
	LastActivityAt       time.Time                `json:"last_activity_at"`
	CloseReason          string                   `json:"close_reason"`
	Role                 proto.Role               `json:"role"`
	ProjectState         proto.ProjectState       `json:"project_state"`
	SyncEpoch            uint64                   `json:"sync_epoch"`
	NeedsConfirmation    bool                     `json:"needs_confirmation"`
	Control              *SessionChannelSnapshot  `json:"control,omitempty"`
	Channels             []SessionChannelSnapshot `json:"channels"`
	ControlBytesIn       uint64                   `json:"control_bytes_in"`
	ControlBytesOut      uint64                   `json:"control_bytes_out"`
	StdioBytesIn         uint64                   `json:"stdio_bytes_in"`
	StdioBytesOut        uint64                   `json:"stdio_bytes_out"`
	OwnerFSBytesIn       uint64                   `json:"ownerfs_bytes_in"`
	OwnerFSBytesOut      uint64                   `json:"ownerfs_bytes_out"`
	AttachedCommandCount int                      `json:"attached_command_count"`
}

func cloneSessionSnapshot(snapshot SessionSnapshot) SessionSnapshot {
	if snapshot.Control != nil {
		copy := *snapshot.Control
		snapshot.Control = &copy
	}
	snapshot.Channels = append([]SessionChannelSnapshot(nil), snapshot.Channels...)
	return snapshot
}
