package transport

import (
	"net"
	"testing"
	"time"
)

func TestOwnerFileRPCClientDoneClosesWhenChannelDisconnects(t *testing.T) {
	t.Parallel()

	clientConn, serverConn := net.Pipe()
	client := NewOwnerFileRPCClient(clientConn, clientConn, clientConn)
	defer client.Close()

	if err := serverConn.Close(); err != nil {
		t.Fatalf("Close(serverConn) error = %v", err)
	}

	select {
	case <-client.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("owner file client Done() did not close after disconnect")
	}
}
