package admin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

type RPCRequest struct {
	ID     uint64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type RPCResponse struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type SocketServer struct {
	service *Service
}

func NewSocketServer(service *Service) *SocketServer {
	return &SocketServer{service: service}
}

func ListenAdminSocket(path string) (net.Listener, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("admin socket path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	_ = os.Remove(path)
	return net.Listen("unix", path)
}

func (s *SocketServer) Serve(ctx context.Context, listener net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go func() {
			defer conn.Close()
			_ = s.serveConn(conn)
		}()
	}
}

func (s *SocketServer) serveConn(conn net.Conn) error {
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		var request RPCRequest
		if err := json.Unmarshal(line, &request); err != nil {
			if writeErr := writeRPCResponse(writer, RPCResponse{Error: &RPCError{Code: string("invalid-argument"), Message: err.Error()}}); writeErr != nil {
				return writeErr
			}
			continue
		}
		response := s.handle(request)
		if err := writeRPCResponse(writer, response); err != nil {
			return err
		}
	}
}

func (s *SocketServer) handle(request RPCRequest) RPCResponse {
	writeResult := func(value any) RPCResponse {
		raw, err := json.Marshal(value)
		if err != nil {
			return RPCResponse{ID: request.ID, Error: &RPCError{Code: "internal", Message: err.Error()}}
		}
		return RPCResponse{ID: request.ID, Result: raw}
	}
	writeError := func(err error) RPCResponse {
		if err == nil {
			err = errors.New("admin request failed")
		}
		return RPCResponse{ID: request.ID, Error: &RPCError{Code: "invalid-argument", Message: err.Error()}}
	}
	switch request.Method {
	case "admin_get_daemon_info":
		return writeResult(s.service.DaemonInfo())
	case "admin_list_sessions":
		return writeResult(s.service.ListSessions())
	case "admin_get_session":
		var params struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return writeError(err)
		}
		session, ok := s.service.GetSession(params.SessionID)
		if !ok {
			return writeError(errors.New("session does not exist"))
		}
		return writeResult(session)
	case "admin_get_tmux_status":
		var params struct {
			ClientID  string `json:"client_id"`
			CommandID string `json:"command_id"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return writeError(err)
		}
		status, err := s.service.GetTmuxStatus(params.ClientID, params.CommandID)
		if err != nil {
			return writeError(err)
		}
		return writeResult(status)
	case "admin_terminate_session":
		var params struct {
			Actor     string `json:"actor"`
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return writeError(err)
		}
		if err := s.service.TerminateSession(params.Actor, params.SessionID); err != nil {
			return writeError(err)
		}
		return writeResult(struct{}{})
	case "admin_list_users":
		return writeResult(s.service.ListUsers())
	case "admin_get_user":
		var params struct {
			Username string `json:"username"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return writeError(err)
		}
		detail, ok := s.service.GetUserDetail(params.Username)
		if !ok {
			return writeError(errors.New("user does not exist"))
		}
		return writeResult(detail)
	case "admin_create_user":
		var params struct {
			Actor    string `json:"actor"`
			Username string `json:"username"`
			Note     string `json:"note"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return writeError(err)
		}
		user, err := s.service.CreateUser(params.Actor, params.Username, params.Note)
		if err != nil {
			return writeError(err)
		}
		return writeResult(user)
	case "admin_disable_user":
		var params struct {
			Actor    string `json:"actor"`
			Username string `json:"username"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return writeError(err)
		}
		user, err := s.service.DisableUser(params.Actor, params.Username)
		if err != nil {
			return writeError(err)
		}
		return writeResult(user)
	case "admin_issue_user_token":
		var params struct {
			Actor       string `json:"actor"`
			Username    string `json:"username"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return writeError(err)
		}
		token, err := s.service.IssueUserToken(params.Actor, params.Username, params.Description)
		if err != nil {
			return writeError(err)
		}
		return writeResult(token)
	case "admin_revoke_user_token":
		var params struct {
			Actor   string `json:"actor"`
			TokenID string `json:"token_id"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return writeError(err)
		}
		token, err := s.service.RevokeUserToken(params.Actor, params.TokenID)
		if err != nil {
			return writeError(err)
		}
		return writeResult(token)
	case "admin_get_user_entrypoint":
		var params struct {
			Username string `json:"username"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return writeError(err)
		}
		entrypoint, err := s.service.GetUserEntrypoint(params.Username)
		if err != nil {
			return writeError(err)
		}
		return writeResult(struct {
			Username   string   `json:"username"`
			Entrypoint []string `json:"entrypoint"`
		}{Username: params.Username, Entrypoint: entrypoint})
	case "admin_set_user_entrypoint":
		var params struct {
			Actor      string   `json:"actor"`
			Username   string   `json:"username"`
			Entrypoint []string `json:"entrypoint"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return writeError(err)
		}
		user, err := s.service.SetUserEntrypoint(params.Actor, params.Username, params.Entrypoint)
		if err != nil {
			return writeError(err)
		}
		return writeResult(user)
	default:
		return writeError(errors.New("unknown admin method"))
	}
}

func writeRPCResponse(writer *bufio.Writer, response RPCResponse) error {
	line, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if _, err := writer.Write(append(line, '\n')); err != nil {
		return err
	}
	return writer.Flush()
}
