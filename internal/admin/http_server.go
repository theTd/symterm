package admin

import (
	"context"
	"errors"
	"net"
	"net/http"

	"golang.org/x/net/websocket"
)

type HTTPServer struct {
	service *Service
}

func NewHTTPServer(service *Service) *HTTPServer {
	return &HTTPServer{service: service}
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
	mux.HandleFunc("/admin", s.handleAdminRoot)
	mux.HandleFunc("/admin/", s.handleAdminRoot)
	mux.HandleFunc("/admin/api/v1/bootstrap", s.withLoopbackAPI(s.handleV1Bootstrap))
	mux.HandleFunc("/admin/api/v1/overview", s.withLoopbackAPI(s.handleV1Overview))
	mux.HandleFunc("/admin/api/v1/sessions", s.withLoopbackAPI(s.handleV1Sessions))
	mux.HandleFunc("/admin/api/v1/sessions/", s.withLoopbackAPI(s.handleV1Session))
	mux.HandleFunc("/admin/api/v1/users", s.withLoopbackAPI(s.handleV1Users))
	mux.HandleFunc("/admin/api/v1/users/", s.withLoopbackAPI(s.handleV1User))
	mux.HandleFunc("/admin/api/v1/tokens/", s.withLoopbackAPI(s.handleV1Token))
	mux.HandleFunc("/admin/api/v1/audit", s.withLoopbackAPI(s.handleV1Audit))
	mux.Handle("/admin/ws", websocket.Handler(s.handleEventsWebsocket))
	return mux
}
