package daemoncmd

import (
	"net"
	"reflect"
	"sync"
	"testing"
)

func TestDaemonShutdownCoordinatorClosesListenersBeforeStoppingProjectsAndIsIdempotent(t *testing.T) {
	t.Parallel()

	var (
		mu    sync.Mutex
		order []string
	)
	record := func(name string) {
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
	}

	sshListener := &shutdownTestListener{name: "ssh", record: record}
	adminSocketListener := &shutdownTestListener{name: "admin-socket", record: record}
	adminHTTPListener := &shutdownTestListener{name: "admin-http", record: record}
	stopper := &shutdownTestStopper{record: record}

	coordinator := newDaemonShutdownCoordinator(nil,
		daemonShutdownStep{name: "close ssh listener", run: func() error { return closeDaemonListener(sshListener) }},
		daemonShutdownStep{name: "close admin socket listener", run: func() error { return closeDaemonListener(adminSocketListener) }},
		daemonShutdownStep{name: "close admin http listener", run: func() error { return closeDaemonListener(adminHTTPListener) }},
		daemonShutdownStep{name: "stop active project runtimes", run: stopper.StopAllProjects},
	)

	if err := coordinator.Shutdown("context canceled"); err != nil {
		t.Fatalf("Shutdown(first) error = %v", err)
	}
	if err := coordinator.Shutdown("second call"); err != nil {
		t.Fatalf("Shutdown(second) error = %v", err)
	}

	want := []string{"ssh", "admin-socket", "admin-http", "stop-projects"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("shutdown order = %#v, want %#v", order, want)
	}
	if sshListener.closeCount != 1 || adminSocketListener.closeCount != 1 || adminHTTPListener.closeCount != 1 {
		t.Fatalf("listener close counts = ssh:%d socket:%d http:%d, want all 1", sshListener.closeCount, adminSocketListener.closeCount, adminHTTPListener.closeCount)
	}
	if stopper.calls != 1 {
		t.Fatalf("stopper calls = %d, want 1", stopper.calls)
	}
}

type shutdownTestListener struct {
	name       string
	record     func(string)
	closeCount int
}

func (l *shutdownTestListener) Accept() (net.Conn, error) {
	return nil, net.ErrClosed
}

func (l *shutdownTestListener) Close() error {
	l.closeCount++
	if l.record != nil {
		l.record(l.name)
	}
	if l.closeCount > 1 {
		return net.ErrClosed
	}
	return nil
}

func (l *shutdownTestListener) Addr() net.Addr {
	return shutdownTestAddr(l.name)
}

type shutdownTestAddr string

func (a shutdownTestAddr) Network() string {
	return "test"
}

func (a shutdownTestAddr) String() string {
	return string(a)
}

type shutdownTestStopper struct {
	record func(string)
	calls  int
}

func (s *shutdownTestStopper) StopAllProjects() error {
	s.calls++
	if s.record != nil {
		s.record("stop-projects")
	}
	return nil
}
