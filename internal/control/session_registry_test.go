package control

import (
	"sync/atomic"
	"testing"
	"time"

	"symterm/internal/proto"
)

type countingCloser struct {
	closes atomic.Int32
}

type noopCloser struct{}

func (noopCloser) Close() error { return nil }

func (c *countingCloser) Close() error {
	c.closes.Add(1)
	return nil
}

func (c *countingCloser) Count() int32 {
	if c == nil {
		return 0
	}
	return c.closes.Load()
}

func TestSessionRegistryRetainReleaseAndOwnerFileLifecycle(t *testing.T) {
	t.Parallel()

	registry := newSessionRegistry()
	hello := registry.Hello("alice", proto.HelloRequest{
		ProjectID:          "demo",
		LocalWorkspaceRoot: "/workspace/demo",
		WorkspaceDigest:    testWorkspaceDigest(1, "root-a"),
	})

	if err := registry.RetainClient(hello.ClientID); err != nil {
		t.Fatalf("RetainClient() error = %v", err)
	}

	firstClient := &fakeOwnerFileClient{}
	registration, err := registry.RegisterOwnerFileClient(hello.ClientID, firstClient)
	if err != nil {
		t.Fatalf("RegisterOwnerFileClient(first) error = %v", err)
	}
	if registration.Previous != nil {
		t.Fatalf("RegisterOwnerFileClient(first) previous = %#v, want nil", registration.Previous)
	}
	if registration.Session.ClientID != hello.ClientID {
		t.Fatalf("RegisterOwnerFileClient(first) session = %#v", registration.Session)
	}

	secondClient := &fakeOwnerFileClient{}
	registration, err = registry.RegisterOwnerFileClient(hello.ClientID, secondClient)
	if err != nil {
		t.Fatalf("RegisterOwnerFileClient(second) error = %v", err)
	}
	if registration.Previous != firstClient {
		t.Fatalf("RegisterOwnerFileClient(second) previous = %#v, want first client", registration.Previous)
	}

	release, err := registry.ReleaseClient(hello.ClientID)
	if err != nil {
		t.Fatalf("ReleaseClient(retained) error = %v", err)
	}
	if release.Released {
		t.Fatalf("ReleaseClient(retained) = %#v, want unreleased ref decrement", release)
	}

	release, err = registry.ReleaseClient(hello.ClientID)
	if err != nil {
		t.Fatalf("ReleaseClient(final) error = %v", err)
	}
	if !release.Released {
		t.Fatalf("ReleaseClient(final) = %#v, want released", release)
	}
	if release.OwnerFileClient != secondClient {
		t.Fatalf("ReleaseClient(final) owner file client = %#v, want second client", release.OwnerFileClient)
	}
	if _, err := registry.Session(hello.ClientID); err == nil {
		t.Fatal("Session() succeeded after final release")
	}
	if registry.OwnerFileClient(hello.ClientID) != nil {
		t.Fatal("OwnerFileClient() retained released client")
	}
}

func TestSessionRegistryHelloCapturesWorkspaceIdentity(t *testing.T) {
	t.Parallel()

	registry := newSessionRegistry()
	hello := registry.Hello("alice", proto.HelloRequest{
		ProjectID:           "demo",
		WorkspaceInstanceID: "wsi-demo",
		SessionKind:         proto.SessionKindAuthority,
	})

	session, err := registry.Session(hello.ClientID)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if session.WorkspaceInstanceID != "wsi-demo" {
		t.Fatalf("WorkspaceInstanceID = %q, want wsi-demo", session.WorkspaceInstanceID)
	}
	if session.SessionKind != proto.SessionKindAuthority {
		t.Fatalf("SessionKind = %q, want authority", session.SessionKind)
	}

	snapshots := registry.ListLiveSessions()
	if len(snapshots) != 1 {
		t.Fatalf("ListLiveSessions() len = %d, want 1", len(snapshots))
	}
	if snapshots[0].WorkspaceInstanceID != "wsi-demo" {
		t.Fatalf("LiveSessionSnapshot.WorkspaceInstanceID = %q, want wsi-demo", snapshots[0].WorkspaceInstanceID)
	}
	if snapshots[0].SessionKind != proto.SessionKindAuthority {
		t.Fatalf("LiveSessionSnapshot.SessionKind = %q, want authority", snapshots[0].SessionKind)
	}
}

func TestSessionRegistryOwnerDisconnectAndProjectRemoval(t *testing.T) {
	t.Parallel()

	registry := newSessionRegistry()
	owner := registry.Hello("alice", proto.HelloRequest{ProjectID: "demo"})
	follower := registry.Hello("alice", proto.HelloRequest{ProjectID: "demo"})
	other := registry.Hello("alice", proto.HelloRequest{ProjectID: "other"})

	ownerClient := &fakeOwnerFileClient{}
	followerClient := &fakeOwnerFileClient{}
	otherClient := &fakeOwnerFileClient{}
	if _, err := registry.RegisterOwnerFileClient(owner.ClientID, ownerClient); err != nil {
		t.Fatalf("RegisterOwnerFileClient(owner) error = %v", err)
	}
	if _, err := registry.RegisterOwnerFileClient(follower.ClientID, followerClient); err != nil {
		t.Fatalf("RegisterOwnerFileClient(follower) error = %v", err)
	}
	if _, err := registry.RegisterOwnerFileClient(other.ClientID, otherClient); err != nil {
		t.Fatalf("RegisterOwnerFileClient(other) error = %v", err)
	}

	disconnect, err := registry.OwnerFileDisconnected(owner.ClientID, &fakeOwnerFileClient{})
	if err != nil {
		t.Fatalf("OwnerFileDisconnected(mismatch) error = %v", err)
	}
	if disconnect.Matched {
		t.Fatalf("OwnerFileDisconnected(mismatch) = %#v, want unmatched", disconnect)
	}

	disconnect, err = registry.OwnerFileDisconnected(owner.ClientID, ownerClient)
	if err != nil {
		t.Fatalf("OwnerFileDisconnected(match) error = %v", err)
	}
	if !disconnect.Matched || disconnect.Session.ClientID != owner.ClientID {
		t.Fatalf("OwnerFileDisconnected(match) = %#v", disconnect)
	}
	if registry.OwnerFileClient(owner.ClientID) != nil {
		t.Fatal("OwnerFileClient() retained disconnected client")
	}

	removed := registry.RemoveProjectOwnerFileClients(proto.ProjectKey{Username: "alice", ProjectID: "demo"}, follower.ClientID)
	if len(removed) != 0 {
		t.Fatalf("RemoveProjectOwnerFileClients(exclude follower) = %#v, want no removals", removed)
	}
	if registry.OwnerFileClient(follower.ClientID) != followerClient {
		t.Fatal("RemoveProjectOwnerFileClients() removed excluded client")
	}

	removed = registry.RemoveProjectOwnerFileClients(proto.ProjectKey{Username: "alice", ProjectID: "demo"}, "")
	if len(removed) != 1 || removed[0] != followerClient {
		t.Fatalf("RemoveProjectOwnerFileClients() = %#v, want follower client", removed)
	}
	if registry.OwnerFileClient(other.ClientID) != otherClient {
		t.Fatal("RemoveProjectOwnerFileClients() touched other project")
	}
}

func TestSessionRegistrySnapshotTracksTrafficAndConnMetadata(t *testing.T) {
	t.Parallel()

	registry := newSessionRegistry()
	connectedAt := time.Unix(1_700_000_000, 0).UTC()
	hello := registry.HelloPrincipal(AuthenticatedPrincipal{
		Username:        "alice",
		TokenID:         "tok-1",
		TokenSource:     TokenSourceManaged,
		AuthenticatedAt: connectedAt,
	}, proto.HelloRequest{
		ProjectID:       "demo",
		TransportKind:   string(TransportKindSSH),
		WorkspaceDigest: testWorkspaceDigest(2, "root-a"),
	}, connectedAt)

	controlCounters := &TrafficCounters{}
	controlCounters.AddIn(11)
	controlCounters.AddOut(13)
	if err := registry.BindControl(hello.ClientID, ConnMeta{
		TransportKind: TransportKindUnknown,
		RemoteAddr:    "203.0.113.10:40123",
		LocalAddr:     "127.0.0.1:7000",
		ConnectedAt:   connectedAt,
	}, controlCounters, noopCloser{}); err != nil {
		t.Fatalf("BindControl() error = %v", err)
	}

	stdioCounters := &TrafficCounters{}
	stdioCounters.AddIn(17)
	stdioCounters.AddOut(19)
	if _, err := registry.AttachChannel(hello.ClientID, ConnMeta{
		TransportKind: TransportKindSSH,
		RemoteAddr:    "local",
		LocalAddr:     "local",
		ConnectedAt:   connectedAt.Add(2 * time.Second),
		ChannelKind:   ChannelKindStdio,
	}, stdioCounters, noopCloser{}); err != nil {
		t.Fatalf("AttachChannel(stdio) error = %v", err)
	}

	ownerCounters := &TrafficCounters{}
	ownerCounters.AddIn(23)
	ownerCounters.AddOut(29)
	if _, err := registry.AttachChannel(hello.ClientID, ConnMeta{
		TransportKind: TransportKindSSH,
		RemoteAddr:    "203.0.113.10:40123",
		LocalAddr:     "127.0.0.1:7000",
		ConnectedAt:   connectedAt.Add(3 * time.Second),
		ChannelKind:   ChannelKindOwnerFS,
	}, ownerCounters, noopCloser{}); err != nil {
		t.Fatalf("AttachChannel(ownerfs) error = %v", err)
	}

	snapshots := registry.ListLiveSessions()
	if len(snapshots) != 1 {
		t.Fatalf("ListLiveSessions() len = %d, want 1", len(snapshots))
	}
	snapshot := snapshots[0]
	if snapshot.SessionID != hello.SessionID || snapshot.ClientID != hello.ClientID {
		t.Fatalf("snapshot ids = %#v", snapshot)
	}
	if snapshot.Principal.Username != "alice" || snapshot.Principal.TokenID != "tok-1" {
		t.Fatalf("snapshot principal = %#v", snapshot.Principal)
	}
	if snapshot.Control == nil {
		t.Fatal("snapshot.Control = nil")
	}
	if snapshot.Control.Meta.TransportKind != TransportKindSSH {
		t.Fatalf("control transport = %q, want %q", snapshot.Control.Meta.TransportKind, TransportKindSSH)
	}
	if snapshot.Control.Meta.RemoteAddr != "203.0.113.10:40123" || snapshot.Control.Meta.LocalAddr != "127.0.0.1:7000" {
		t.Fatalf("control meta = %#v", snapshot.Control.Meta)
	}
	if snapshot.ControlBytesIn != 11 || snapshot.ControlBytesOut != 13 {
		t.Fatalf("control bytes = in:%d out:%d", snapshot.ControlBytesIn, snapshot.ControlBytesOut)
	}
	if snapshot.StdioBytesIn != 17 || snapshot.StdioBytesOut != 19 {
		t.Fatalf("stdio bytes = in:%d out:%d", snapshot.StdioBytesIn, snapshot.StdioBytesOut)
	}
	if snapshot.OwnerFSBytesIn != 23 || snapshot.OwnerFSBytesOut != 29 {
		t.Fatalf("ownerfs bytes = in:%d out:%d", snapshot.OwnerFSBytesIn, snapshot.OwnerFSBytesOut)
	}
	if len(snapshot.Channels) != 2 {
		t.Fatalf("channels len = %d, want 2", len(snapshot.Channels))
	}
	if !snapshot.LastActivityAt.Equal(connectedAt.Add(3 * time.Second)) {
		t.Fatalf("LastActivityAt = %v, want %v", snapshot.LastActivityAt, connectedAt.Add(3*time.Second))
	}
}

func TestSessionRegistryTerminateSessionClosesChannelsAndRetainsFinalSnapshot(t *testing.T) {
	t.Parallel()

	registry := newSessionRegistry()
	connectedAt := time.Unix(1_700_000_100, 0).UTC()
	hello := registry.HelloPrincipal(AuthenticatedPrincipal{
		Username:        "alice",
		TokenID:         "tok-terminate",
		TokenSource:     TokenSourceManaged,
		AuthenticatedAt: connectedAt,
	}, proto.HelloRequest{
		ProjectID:       "demo",
		WorkspaceDigest: testWorkspaceDigest(1, "root-b"),
	}, connectedAt)

	controlCounters := &TrafficCounters{}
	controlCounters.AddIn(5)
	controlCounters.AddOut(7)
	controlCloser := &countingCloser{}
	if err := registry.BindControl(hello.ClientID, ConnMeta{
		TransportKind: TransportKindSSH,
		RemoteAddr:    "198.51.100.10:5000",
		LocalAddr:     "127.0.0.1:7000",
		ConnectedAt:   connectedAt,
	}, controlCounters, controlCloser); err != nil {
		t.Fatalf("BindControl() error = %v", err)
	}

	stdioCloser := &countingCloser{}
	stdioCounters := &TrafficCounters{}
	stdioCounters.AddIn(9)
	stdioCounters.AddOut(11)
	if _, err := registry.AttachChannel(hello.ClientID, ConnMeta{
		TransportKind: TransportKindSSH,
		RemoteAddr:    "local",
		LocalAddr:     "local",
		ConnectedAt:   connectedAt.Add(time.Second),
		ChannelKind:   ChannelKindStdio,
	}, stdioCounters, stdioCloser); err != nil {
		t.Fatalf("AttachChannel() error = %v", err)
	}

	if err := registry.TerminateSession(hello.SessionID, "terminated by admin"); err != nil {
		t.Fatalf("TerminateSession() error = %v", err)
	}
	if controlCloser.Count() != 1 || stdioCloser.Count() != 1 {
		t.Fatalf("close counts = control:%d stdio:%d", controlCloser.Count(), stdioCloser.Count())
	}

	release, err := registry.ReleaseClient(hello.ClientID)
	if err != nil {
		t.Fatalf("ReleaseClient() error = %v", err)
	}
	if !release.Released {
		t.Fatalf("ReleaseClient() = %#v, want Released", release)
	}

	snapshot, ok := registry.SessionSnapshot(hello.SessionID)
	if !ok {
		t.Fatal("SessionSnapshot() missing closed session")
	}
	if snapshot.CloseReason != "terminated by admin" {
		t.Fatalf("CloseReason = %q, want terminated by admin", snapshot.CloseReason)
	}
	if snapshot.ControlBytesIn != 5 || snapshot.ControlBytesOut != 7 {
		t.Fatalf("control bytes after close = in:%d out:%d", snapshot.ControlBytesIn, snapshot.ControlBytesOut)
	}
	if snapshot.StdioBytesIn != 9 || snapshot.StdioBytesOut != 11 {
		t.Fatalf("stdio bytes after close = in:%d out:%d", snapshot.StdioBytesIn, snapshot.StdioBytesOut)
	}
}
