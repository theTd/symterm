package admin

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:webdist
var adminWebDist embed.FS

func (s *HTTPServer) handleAdminRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !adminUIAvailable() {
		http.Error(w, "embedded admin UI is unavailable", http.StatusServiceUnavailable)
		return
	}
	fileServer := http.FileServer(http.FS(adminUIFS()))
	adminPath := strings.TrimPrefix(r.URL.Path, "/admin")
	if adminPath == "/legacy" || adminPath == "/api" || strings.HasPrefix(adminPath, "/api/") {
		http.NotFound(w, r)
		return
	}
	if adminPath == "" || adminPath == "/" || !strings.Contains(path.Base(adminPath), ".") {
		http.ServeFileFS(w, r, adminUIFS(), "index.html")
		return
	}
	http.StripPrefix("/admin/", fileServer).ServeHTTP(w, r)
}

func adminUIAvailable() bool {
	_, err := fs.Stat(adminUIFS(), "index.html")
	return err == nil
}

func adminUIFS() fs.FS {
	sub, err := fs.Sub(adminWebDist, "webdist")
	if err != nil {
		return emptyUIFS{}
	}
	return sub
}

type emptyUIFS struct{}

func (emptyUIFS) Open(string) (fs.File, error) {
	return nil, fs.ErrNotExist
}
