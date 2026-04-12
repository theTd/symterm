package control

import (
	"io"
	"sync/atomic"
	"time"

	"symterm/internal/proto"
)

type TokenSource string

const (
	TokenSourceManaged TokenSource = "managed"
)

type AuthenticatedPrincipal struct {
	Username        string
	UserDisabled    bool
	TokenID         string
	TokenSource     TokenSource
	AuthenticatedAt time.Time
}

type ChannelKind string

const (
	ChannelKindControl ChannelKind = "control"
	ChannelKindStdio   ChannelKind = "stdio"
	ChannelKindOwnerFS ChannelKind = "ownerfs"
)

type TransportKind string

const (
	TransportKindUnknown TransportKind = "unknown"
	TransportKindSSH     TransportKind = "ssh"
)

type ConnMeta struct {
	TransportKind TransportKind
	RemoteAddr    string
	LocalAddr     string
	ConnectedAt   time.Time
	ChannelKind   ChannelKind
}

type TrafficCounters struct {
	bytesIn  atomic.Uint64
	bytesOut atomic.Uint64
}

func (c *TrafficCounters) AddIn(n int) {
	if c == nil || n <= 0 {
		return
	}
	c.bytesIn.Add(uint64(n))
}

func (c *TrafficCounters) AddOut(n int) {
	if c == nil || n <= 0 {
		return
	}
	c.bytesOut.Add(uint64(n))
}

func (c *TrafficCounters) Snapshot() (uint64, uint64) {
	if c == nil {
		return 0, 0
	}
	return c.bytesIn.Load(), c.bytesOut.Load()
}

type SessionChannelSnapshot struct {
	ChannelID string
	Meta      ConnMeta
	BytesIn   uint64
	BytesOut  uint64
}

type LiveSessionSnapshot struct {
	SessionID            string
	ClientID             string
	ProjectID            string
	WorkspaceRoot        string
	WorkspaceInstanceID  string
	WorkspaceDigest      string
	SessionKind          proto.SessionKind
	Principal            AuthenticatedPrincipal
	ConnectedAt          time.Time
	LastActivityAt       time.Time
	CloseReason          string
	Role                 proto.Role
	ProjectState         proto.ProjectState
	SyncEpoch            uint64
	NeedsConfirmation    bool
	Control              *SessionChannelSnapshot
	Channels             []SessionChannelSnapshot
	ControlBytesIn       uint64
	ControlBytesOut      uint64
	StdioBytesIn         uint64
	StdioBytesOut        uint64
	OwnerFSBytesIn       uint64
	OwnerFSBytesOut      uint64
	AttachedCommandCount int
}

type liveChannelBinding struct {
	channelID string
	meta      ConnMeta
	counters  *TrafficCounters
	closer    io.Closer
}
