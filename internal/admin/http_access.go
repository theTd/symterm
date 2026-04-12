package admin

import (
	"net"
	"net/http"
	"strings"

	"symterm/internal/proto"
)

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}

func adminActorForRequest(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	host = strings.TrimSpace(host)
	if host == "" {
		host = "loopback"
	}
	return "loopback-admin:" + host
}

func (s *HTTPServer) withLoopbackAPI(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackRequest(r) {
			writeAPIError(w, http.StatusForbidden, proto.NewError(proto.ErrPermissionDenied, "admin access requires loopback"))
			return
		}
		next(w, r, adminActorForRequest(r))
	}
}
