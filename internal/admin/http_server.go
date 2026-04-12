package admin

import (
	"context"
	"errors"
	"net"
	"net/http"

	"golang.org/x/net/websocket"
)

type HTTPServer struct {
	service  *Service
	sessions *adminSessionStore
}

func NewHTTPServer(service *Service) *HTTPServer {
	return &HTTPServer{
		service:  service,
		sessions: newAdminSessionStore(),
	}
}

func (s *HTTPServer) ServeListener(ctx context.Context, listener net.Listener) error {
	s.service.SetAdminWebAddr(listener.Addr().String())
	server := &http.Server{Handler: s.routes()}
	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()
	err := server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (s *HTTPServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin", s.handleAdminShell)
	mux.HandleFunc("/admin/api/login", s.handleLogin)
	mux.HandleFunc("/admin/api/logout", s.withAuth(s.requireCSRF(s.handleLogout)))
	mux.HandleFunc("/admin/api/snapshot", s.withAuth(s.handleSnapshot))
	mux.HandleFunc("/admin/api/daemon", s.withAuth(s.handleDaemon))
	mux.HandleFunc("/admin/api/sessions", s.withAuth(s.requireCSRF(s.handleSessions)))
	mux.HandleFunc("/admin/api/sessions/", s.withAuth(s.requireCSRF(s.handleSession)))
	mux.HandleFunc("/admin/api/users", s.withAuth(s.requireCSRF(s.handleUsers)))
	mux.HandleFunc("/admin/api/users/", s.withAuth(s.requireCSRF(s.handleUser)))
	mux.Handle("/admin/ws", websocket.Handler(s.handleEventsWebsocket))
	return mux
}
