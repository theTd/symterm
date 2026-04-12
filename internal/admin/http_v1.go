package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"symterm/internal/proto"
)

type apiEnvelope struct {
	OK    bool      `json:"ok"`
	Data  any       `json:"data,omitempty"`
	Error *apiError `json:"error,omitempty"`
	Meta  any       `json:"meta,omitempty"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ActionResult struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type BootstrapPayload struct {
	Actor         string     `json:"actor,omitempty"`
	Daemon        DaemonInfo `json:"daemon"`
	APIBase       string     `json:"api_base"`
	WebSocketPath string     `json:"websocket_path"`
}

type SessionListResponse struct {
	Items []SessionSnapshot `json:"items"`
}

type SessionDetailResponse struct {
	Session      SessionSnapshot `json:"session"`
	RelatedAudit []AuditRecord   `json:"related_audit"`
}

type UserListResponse struct {
	Items []UserRecord `json:"items"`
}

type UserDetailResponse struct {
	UserDetail
	RelatedAudit []AuditRecord `json:"related_audit"`
}

func (s *HTTPServer) handleV1Bootstrap(w http.ResponseWriter, r *http.Request, actor string) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, proto.NewError(proto.ErrInvalidArgument, "method not allowed"))
		return
	}
	writeAPIData(w, http.StatusOK, BootstrapPayload{
		Actor:         actor,
		Daemon:        s.service.DaemonInfo(),
		APIBase:       "/admin/api/v1",
		WebSocketPath: "/admin/ws",
	})
}

func (s *HTTPServer) handleV1Overview(w http.ResponseWriter, r *http.Request, _ string) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, proto.NewError(proto.ErrInvalidArgument, "method not allowed"))
		return
	}
	overview, err := s.service.Overview()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	writeAPIData(w, http.StatusOK, overview)
}

func (s *HTTPServer) handleV1Sessions(w http.ResponseWriter, r *http.Request, _ string) {
	switch r.Method {
	case http.MethodGet:
		filter := SessionListFilter{
			Search:        r.URL.Query().Get("search"),
			Username:      r.URL.Query().Get("username"),
			Role:          proto.Role(strings.TrimSpace(r.URL.Query().Get("role"))),
			ProjectState:  proto.ProjectState(strings.TrimSpace(r.URL.Query().Get("project_state"))),
			IncludeClosed: parseBoolQuery(r, "include_closed"),
		}
		writeAPIData(w, http.StatusOK, SessionListResponse{
			Items: s.service.ListSessionsFiltered(filter),
		})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, proto.NewError(proto.ErrInvalidArgument, "method not allowed"))
	}
}

func (s *HTTPServer) handleV1Session(w http.ResponseWriter, r *http.Request, actor string) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/api/v1/sessions/")
	if strings.HasSuffix(path, "/terminate") {
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, proto.NewError(proto.ErrInvalidArgument, "method not allowed"))
			return
		}
		sessionID := strings.TrimSuffix(path, "/terminate")
		if err := s.service.TerminateSession(actor, sessionID); err != nil {
			writeAPIError(w, statusCodeForError(err), err)
			return
		}
		writeAPIData(w, http.StatusOK, ActionResult{
			Status:  "ok",
			Message: "session terminated",
		})
		return
	}

	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, proto.NewError(proto.ErrInvalidArgument, "method not allowed"))
		return
	}
	item, ok := s.service.GetSession(path)
	if !ok {
		writeAPIError(w, http.StatusNotFound, proto.NewError(proto.ErrInvalidArgument, "session does not exist"))
		return
	}
	audit, err := s.service.ListAudit(AuditQuery{Target: path, Page: 1, PageSize: 20})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	writeAPIData(w, http.StatusOK, SessionDetailResponse{
		Session:      item,
		RelatedAudit: audit.Items,
	})
}

func (s *HTTPServer) handleV1Users(w http.ResponseWriter, r *http.Request, actor string) {
	switch r.Method {
	case http.MethodGet:
		writeAPIData(w, http.StatusOK, UserListResponse{Items: s.service.ListUsers()})
	case http.MethodPost:
		var body struct {
			Username string `json:"username"`
			Note     string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		record, err := s.service.CreateUser(actor, body.Username, body.Note)
		if err != nil {
			writeAPIError(w, statusCodeForError(err), err)
			return
		}
		writeAPIData(w, http.StatusOK, record)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, proto.NewError(proto.ErrInvalidArgument, "method not allowed"))
	}
}

func (s *HTTPServer) handleV1User(w http.ResponseWriter, r *http.Request, actor string) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/api/v1/users/")
	switch {
	case strings.HasSuffix(path, "/disable"):
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, proto.NewError(proto.ErrInvalidArgument, "method not allowed"))
			return
		}
		username := strings.TrimSuffix(path, "/disable")
		if _, err := s.service.DisableUser(actor, username); err != nil {
			writeAPIError(w, statusCodeForError(err), err)
			return
		}
		writeAPIData(w, http.StatusOK, ActionResult{Status: "ok", Message: "user disabled"})
	case strings.HasSuffix(path, "/tokens"):
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, proto.NewError(proto.ErrInvalidArgument, "method not allowed"))
			return
		}
		username := strings.TrimSuffix(path, "/tokens")
		var body struct {
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
			writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		token, err := s.service.IssueUserToken(actor, username, body.Description)
		if err != nil {
			writeAPIError(w, statusCodeForError(err), err)
			return
		}
		writeAPIData(w, http.StatusOK, token)
	case strings.HasSuffix(path, "/entrypoint"):
		username := strings.TrimSuffix(path, "/entrypoint")
		switch r.Method {
		case http.MethodGet:
			entrypoint, err := s.service.GetUserEntrypoint(username)
			if err != nil {
				writeAPIError(w, statusCodeForError(err), err)
				return
			}
			writeAPIData(w, http.StatusOK, map[string]any{"username": username, "entrypoint": entrypoint})
		case http.MethodPut:
			var body struct {
				Entrypoint []string `json:"entrypoint"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeAPIError(w, http.StatusBadRequest, err)
				return
			}
			record, err := s.service.SetUserEntrypoint(actor, username, body.Entrypoint)
			if err != nil {
				writeAPIError(w, statusCodeForError(err), err)
				return
			}
			writeAPIData(w, http.StatusOK, record)
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, proto.NewError(proto.ErrInvalidArgument, "method not allowed"))
		}
	default:
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, proto.NewError(proto.ErrInvalidArgument, "method not allowed"))
			return
		}
		detail, ok := s.service.GetUserDetail(path)
		if !ok {
			writeAPIError(w, http.StatusNotFound, proto.NewError(proto.ErrInvalidArgument, "user does not exist"))
			return
		}
		audit, err := s.service.ListAudit(AuditQuery{Target: path, Page: 1, PageSize: 20})
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err)
			return
		}
		writeAPIData(w, http.StatusOK, UserDetailResponse{
			UserDetail:   detail,
			RelatedAudit: audit.Items,
		})
	}
}

func (s *HTTPServer) handleV1Token(w http.ResponseWriter, r *http.Request, actor string) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/api/v1/tokens/")
	if !strings.HasSuffix(path, "/revoke") || r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, proto.NewError(proto.ErrInvalidArgument, "method not allowed"))
		return
	}
	tokenID := strings.TrimSuffix(path, "/revoke")
	record, err := s.service.RevokeUserToken(actor, tokenID)
	if err != nil {
		writeAPIError(w, statusCodeForError(err), err)
		return
	}
	writeAPIData(w, http.StatusOK, record)
}

func (s *HTTPServer) handleV1Audit(w http.ResponseWriter, r *http.Request, _ string) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, proto.NewError(proto.ErrInvalidArgument, "method not allowed"))
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	result, err := s.service.ListAudit(AuditQuery{
		Actor:    r.URL.Query().Get("actor"),
		Action:   r.URL.Query().Get("action"),
		Target:   r.URL.Query().Get("target"),
		Result:   r.URL.Query().Get("result"),
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	writeAPIData(w, http.StatusOK, result.Items, map[string]any{
		"page":      result.Page,
		"page_size": result.PageSize,
		"total":     result.Total,
	})
}

func writeAPIData(w http.ResponseWriter, status int, data any, meta ...any) {
	payload := apiEnvelope{OK: true, Data: data}
	if len(meta) > 0 {
		payload.Meta = meta[0]
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeAPIError(w http.ResponseWriter, status int, err error) {
	code, message := proto.ErrorFields(err, proto.ErrInvalidArgument)
	payload := apiEnvelope{
		OK: false,
		Error: &apiError{
			Code:    string(code),
			Message: message,
		},
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func statusCodeForError(err error) int {
	code, _ := proto.ErrorFields(err, proto.ErrInvalidArgument)
	switch code {
	case proto.ErrAuthenticationFailed:
		return http.StatusUnauthorized
	case proto.ErrPermissionDenied:
		return http.StatusForbidden
	case proto.ErrUnknownFile:
		return http.StatusNotFound
	case proto.ErrConflict:
		return http.StatusConflict
	case proto.ErrCursorExpired:
		return http.StatusGone
	default:
		return http.StatusBadRequest
	}
}

func parseBoolQuery(r *http.Request, key string) bool {
	raw := strings.TrimSpace(strings.ToLower(r.URL.Query().Get(key)))
	return raw == "1" || raw == "true" || raw == "yes"
}
