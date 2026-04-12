package config

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

type EndpointKind string

const (
	EndpointSSH EndpointKind = "ssh"
)

type Endpoint struct {
	Kind   EndpointKind
	Target string
}

func ParseEndpoint(raw string) (Endpoint, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return Endpoint{}, errors.New("endpoint is required")
	}

	if strings.Contains(value, "://") {
		scheme, _, _ := strings.Cut(value, "://")
		if scheme != "symterm" {
			return Endpoint{}, fmt.Errorf("unsupported endpoint scheme %q; migrate to symterm://<host>:<port>", scheme)
		}
	}
	target, ok := strings.CutPrefix(value, "symterm://")
	if !ok {
		return Endpoint{}, errors.New("unsupported endpoint; expected symterm://<host>:<port>")
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return Endpoint{}, errors.New("symterm endpoint target is required")
	}
	if strings.ContainsAny(target, " \t\r\n") {
		return Endpoint{}, errors.New("symterm endpoint must not contain whitespace")
	}
	if strings.Contains(target, "@") {
		return Endpoint{}, errors.New("symterm endpoint must not include user@host; migrate to symterm://<host>:<port> and authenticate with SYMTERM_TOKEN")
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return Endpoint{}, fmt.Errorf("symterm endpoint must be symterm://<host>:<port>: %w", err)
	}
	if strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
		return Endpoint{}, errors.New("symterm endpoint host and port are required")
	}
	return Endpoint{
		Kind:   EndpointSSH,
		Target: target,
	}, nil
}
