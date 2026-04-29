package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"symterm/internal/control"
	"symterm/internal/diagnostic"
	"symterm/internal/proto"
)

func (s *Server) readBinaryPayload(request Request) (Request, error) {
	var lengthGetter struct {
		DataLength int64 `json:"data_length"`
	}
	if err := json.Unmarshal(request.Params, &lengthGetter); err != nil {
		return request, err
	}
	if lengthGetter.DataLength <= 0 {
		return request, nil
	}

	rawData := make([]byte, lengthGetter.DataLength)
	if _, err := io.ReadFull(s.reader, rawData); err != nil {
		return request, err
	}

	var paramsMap map[string]any
	if err := json.Unmarshal(request.Params, &paramsMap); err != nil {
		return request, err
	}
	paramsMap["data"] = rawData
	delete(paramsMap, "data_length")

	newParams, err := json.Marshal(paramsMap)
	if err != nil {
		return request, err
	}
	request.Params = newParams
	return request, nil
}

type Server struct {
	service        serverService
	reader         *bufio.Reader
	writer         *bufio.Writer
	closer         io.Closer
	connMeta       control.ConnMeta
	principal      *control.AuthenticatedPrincipal
	counters       *control.TrafficCounters
	writeMu        sync.Mutex
	streamWG       sync.WaitGroup
	dispatchRoutes map[string]dispatchRoute
	serveRoutes    map[string]serverRoute
	tracef         func(string, ...any)
}

type serverRoute struct {
	handle func(context.Context, Request) error
	async  bool
}

func NewServer(service serverService, reader io.Reader, writer io.Writer) *Server {
	return NewServerWithOptions(service, reader, writer, ServerOptions{})
}

func NewServerWithTrace(service serverService, reader io.Reader, writer io.Writer, tracef func(string, ...any)) *Server {
	return NewServerWithOptions(service, reader, writer, ServerOptions{Tracef: tracef})
}

type ServerOptions struct {
	ConnMeta  control.ConnMeta
	Principal *control.AuthenticatedPrincipal
	Tracef    func(string, ...any)
}

func NewServerWithOptions(service serverService, reader io.Reader, writer io.Writer, options ServerOptions) *Server {
	var closer io.Closer
	if candidate, ok := writer.(io.Closer); ok {
		closer = candidate
	} else if candidate, ok := reader.(io.Closer); ok {
		closer = candidate
	}
	counters := &control.TrafficCounters{}
	connMeta := options.ConnMeta
	if connMeta.TransportKind == "" && connMeta.RemoteAddr == "" && connMeta.LocalAddr == "" && connMeta.ConnectedAt.IsZero() {
		connMeta = detectConnMeta(reader, writer)
	}
	server := &Server{
		service:   service,
		reader:    bufio.NewReader(countingReader{reader: reader, counters: counters}),
		writer:    bufio.NewWriter(countingWriter{writer: writer, counters: counters}),
		closer:    closer,
		connMeta:  connMeta,
		principal: options.Principal,
		counters:  counters,
		tracef:    options.Tracef,
	}
	server.dispatchRoutes = server.newDispatchRoutes()
	server.serveRoutes = server.newServeRoutes()
	return server
}

func (s *Server) Serve(ctx context.Context) error {
	serveCtx, cancel := context.WithCancel(ctx)
	s.trace("server serve start transport=%s remote=%q local=%q", s.connMeta.TransportKind, s.connMeta.RemoteAddr, s.connMeta.LocalAddr)
	var controlClientID string
	defer func() {
		if controlClientID != "" {
			s.service.DisconnectClient(controlClientID)
		}
	}()
	defer s.streamWG.Wait()
	defer cancel()
	defer s.trace("server serve end control_client_id=%q", controlClientID)

	for {
		select {
		case <-serveCtx.Done():
			s.trace("server serve context done error=%v", serveCtx.Err())
			return serveCtx.Err()
		default:
		}

		line, err := s.reader.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				s.trace("server read eof")
				return nil
			}
			s.trace("server read failed error=%v", err)
			return err
		}

		var request Request
		if err := json.Unmarshal(line, &request); err != nil {
			s.trace("server decode failed bytes=%d error=%v", len(line), err)
			if err := s.writeResponse(Response{
				Error: &ResponseError{
					Code:    string(proto.ErrInvalidArgument),
					Message: err.Error(),
				},
			}); err != nil {
				return err
			}
			continue
		}
		s.trace("server request recv id=%d method=%s client_id=%q bytes=%d", request.ID, request.Method, request.ClientID, len(line))

		if route, ok := s.serveRoutes[request.Method]; ok {
			startedAt := time.Now()
			if route.async {
				s.streamWG.Add(1)
				go func(route serverRoute, request Request) {
					defer s.streamWG.Done()
					s.trace("server async route begin id=%d method=%s client_id=%q", request.ID, request.Method, request.ClientID)
					defer func() {
						s.trace("server async route end id=%d method=%s duration_ms=%d", request.ID, request.Method, time.Since(startedAt).Milliseconds())
					}()
					diagnostic.Background(s.service.Diagnostics(), "serve route "+request.Method, route.handle(serveCtx, request))
				}(route, request)
				continue
			}
			s.trace("server route begin id=%d method=%s client_id=%q", request.ID, request.Method, request.ClientID)
			return route.handle(serveCtx, request)
		}
		if request.Method == "hello" {
			controlClientID = ""
		}

		if request.Method == "apply_chunk" {
			var err error
			request, err = s.readBinaryPayload(request)
			if err != nil {
				if writeErr := s.writeResponse(errorResponse(request.ID, err)); writeErr != nil {
					return writeErr
				}
				continue
			}
		}

		// Async dispatch routes: process in a goroutine to avoid blocking the serve loop.
		if route, ok := s.dispatchRoutes[request.Method]; ok && route.async {
			s.streamWG.Add(1)
			go func(req Request) {
				defer s.streamWG.Done()
				asyncStartedAt := time.Now()
				s.trace("server async dispatch begin id=%d method=%s client_id=%q", req.ID, req.Method, req.ClientID)
				response, helloClientID := s.dispatch(serveCtx, req)
				if helloClientID != "" {
					s.trace("server async dispatch unexpected hello_client_id id=%d method=%s", req.ID, req.Method)
				}
				if response.Error != nil {
					s.trace("server async dispatch end id=%d method=%s error_code=%s duration_ms=%d", req.ID, req.Method, response.Error.Code, time.Since(asyncStartedAt).Milliseconds())
				} else {
					s.trace("server async dispatch end id=%d method=%s duration_ms=%d", req.ID, req.Method, time.Since(asyncStartedAt).Milliseconds())
				}
				if controlClientID != "" {
					s.service.NoteSessionActivity(controlClientID)
					s.trace("server async noted session activity client_id=%s", controlClientID)
				}
				if err := s.writeResponse(response); err != nil {
					s.trace("server async write response failed id=%d method=%s error=%v", req.ID, req.Method, err)
				} else {
					s.trace("server async response sent id=%d method=%s", req.ID, req.Method)
				}
			}(request)
			continue
		}

		startedAt := time.Now()
		s.trace("server dispatch begin id=%d method=%s client_id=%q", request.ID, request.Method, request.ClientID)
		response, helloClientID := s.dispatch(serveCtx, request)
		if response.Error != nil {
			s.trace("server dispatch end id=%d method=%s error_code=%s duration_ms=%d", request.ID, request.Method, response.Error.Code, time.Since(startedAt).Milliseconds())
		} else {
			s.trace("server dispatch end id=%d method=%s hello_client_id=%q duration_ms=%d", request.ID, request.Method, helloClientID, time.Since(startedAt).Milliseconds())
		}
		if helloClientID != "" {
			controlClientID = helloClientID
			s.trace("server bind control begin client_id=%s", controlClientID)
			if err := s.service.BindControlConnection(controlClientID, s.connMetaFor(control.ChannelKindControl), s.counters, s.closer); err != nil {
				s.trace("server bind control failed client_id=%s error=%v", controlClientID, err)
				return err
			}
			s.trace("server bind control end client_id=%s", controlClientID)
		}
		if controlClientID != "" {
			s.service.NoteSessionActivity(controlClientID)
			s.trace("server noted session activity client_id=%s", controlClientID)
		}
		if err := s.writeResponse(response); err != nil {
			s.trace("server write response failed id=%d method=%s error=%v", request.ID, request.Method, err)
			return err
		}
		s.trace("server response sent id=%d method=%s", request.ID, request.Method)
	}
}

func (s *Server) newServeRoutes() map[string]serverRoute {
	return map[string]serverRoute{
		"attach_stdio_stream": {
			handle: s.streamAttachStdio,
		},
		"watch_project_stream": {
			handle: s.streamWatchProject,
			async:  true,
		},
		"watch_command_stream": {
			handle: s.streamWatchCommand,
			async:  true,
		},
		internalWatchInvalidateMethod + "_stream": {
			handle: s.streamWatchInvalidate,
			async:  true,
		},
	}
}

func (s *Server) connMetaFor(kind control.ChannelKind) control.ConnMeta {
	meta := s.connMeta
	meta.ChannelKind = kind
	return meta
}

func (s *Server) writeResponse(response Response) error {
	line, err := json.Marshal(response)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.writer.Write(append(line, '\n')); err != nil {
		return err
	}
	return s.writer.Flush()
}

func (s *Server) trace(format string, args ...any) {
	if s == nil || s.tracef == nil {
		return
	}
	s.tracef(format, args...)
}
