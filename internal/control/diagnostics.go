package control

import (
	"errors"

	"symterm/internal/diagnostic"
	"symterm/internal/proto"
)

func (s *Service) Diagnostics() diagnostic.Reporter {
	if s == nil || s.diagnostics == nil {
		return diagnostic.Default()
	}
	return s.diagnostics
}

func (s *Service) reportCleanup(activity string, err error) {
	if isIgnorableServiceError(err) {
		return
	}
	diagnostic.Cleanup(s.Diagnostics(), activity, err)
}

func (s *Service) reportError(activity string, err error) {
	if isIgnorableServiceError(err) {
		return
	}
	diagnostic.Error(s.Diagnostics(), activity, err)
}

func isIgnorableServiceError(err error) bool {
	if err == nil {
		return true
	}
	var protoErr *proto.Error
	return errors.As(err, &protoErr) && protoErr.Code == proto.ErrUnknownClient
}
