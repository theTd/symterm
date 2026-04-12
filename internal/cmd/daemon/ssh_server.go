package daemoncmd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"symterm/internal/control"
	"symterm/internal/diagnostic"
	"symterm/internal/proto"
	"symterm/internal/transport"

	cryptossh "golang.org/x/crypto/ssh"
)

const sshUser = "symterm"

func RunSSHListener(
	ctx context.Context,
	service control.ClientService,
	authenticator control.Authenticator,
	listener net.Listener,
	hostSigner cryptossh.Signer,
	traces ...traceFunc,
) error {
	var trace traceFunc
	if len(traces) > 0 {
		trace = traces[0]
	}
	serverConfig := &cryptossh.ServerConfig{
		PasswordCallback: func(metadata cryptossh.ConnMetadata, supplied []byte) (*cryptossh.Permissions, error) {
			if metadata.User() != sshUser {
				return nil, errors.New("ssh user must be symterm")
			}
			principal, err := authenticator.Authenticate(ctx, string(supplied))
			if err != nil {
				return nil, err
			}
			if principal.AuthenticatedAt.IsZero() {
				principal.AuthenticatedAt = time.Now().UTC()
			}
			return encodePrincipalPermissions(principal), nil
		},
	}
	serverConfig.AddHostKey(hostSigner)

	go func() {
		<-ctx.Done()
		diagnostic.Cleanup(service.Diagnostics(), "close daemon ssh listener", listener.Close())
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				tracef(trace, "daemon ssh listener accept stop error=%v", err)
				return nil
			}
			tracef(trace, "daemon ssh listener accept failed error=%v", err)
			return err
		}
		tracef(trace, "daemon ssh listener accepted remote=%q local=%q", conn.RemoteAddr().String(), conn.LocalAddr().String())

		go func(rawConn net.Conn) {
			defer rawConn.Close()
			if err := serveSSHConn(ctx, service, rawConn, serverConfig, trace); err != nil {
				tracef(trace, "daemon ssh connection serve end remote=%q error=%v", rawConn.RemoteAddr().String(), err)
				diagnostic.Background(service.Diagnostics(), "serve daemon ssh connection", err)
			}
		}(conn)
	}
}

func serveSSHConn(ctx context.Context, service control.ClientService, rawConn net.Conn, serverConfig *cryptossh.ServerConfig, trace traceFunc) error {
	serverConn, chans, reqs, err := cryptossh.NewServerConn(rawConn, serverConfig)
	if err != nil {
		return err
	}
	defer serverConn.Close()
	connCtx, cancel := context.WithCancel(ctx)
	go cryptossh.DiscardRequests(reqs)
	go func() {
		_ = serverConn.Wait()
		cancel()
	}()

	principal, err := principalFromPermissions(serverConn.Permissions)
	if err != nil {
		return err
	}
	baseMeta := control.ConnMeta{
		TransportKind: control.TransportKindSSH,
		RemoteAddr:    rawConn.RemoteAddr().String(),
		LocalAddr:     rawConn.LocalAddr().String(),
		ConnectedAt:   time.Now().UTC(),
	}
	controlOpened := false
	var handlers sync.WaitGroup
	defer func() {
		cancel()
		handlers.Wait()
	}()

	for {
		select {
		case <-connCtx.Done():
			return nil
		case newChannel, ok := <-chans:
			if !ok {
				return nil
			}
			switch newChannel.ChannelType() {
			case transport.SSHChannelControl:
				if controlOpened {
					_ = newChannel.Reject(cryptossh.Prohibited, "control channel already exists")
					continue
				}
				controlOpened = true
				channel, requests, err := newChannel.Accept()
				if err != nil {
					return err
				}
				go cryptossh.DiscardRequests(requests)
				handlers.Add(1)
				go func() {
					defer handlers.Done()
					meta := baseMeta
					meta.ChannelKind = control.ChannelKindControl
					server := transport.NewServerWithOptions(service, channel, channel, transport.ServerOptions{
						ConnMeta:  meta,
						Principal: &principal,
						Tracef:    trace,
					})
					diagnostic.Background(service.Diagnostics(), "serve control ssh channel", server.Serve(connCtx))
				}()
			case transport.SSHChannelOwnerFS:
				payload, err := transport.DecodeOwnerFSOpenPayload(newChannel.ExtraData())
				if err != nil {
					_ = newChannel.Reject(cryptossh.ConnectionFailed, err.Error())
					continue
				}
				channel, requests, err := newChannel.Accept()
				if err != nil {
					return err
				}
				go cryptossh.DiscardRequests(requests)
				handlers.Add(1)
				go func(clientID string) {
					defer handlers.Done()
					meta := baseMeta
					meta.ChannelKind = control.ChannelKindOwnerFS
					counters := &control.TrafficCounters{}
					channelID, attachErr := service.AttachSessionChannel(clientID, meta, counters, channel)
					if attachErr != nil {
						diagnostic.Background(service.Diagnostics(), "attach ownerfs ssh channel", attachErr)
						_ = channel.Close()
						return
					}
					defer service.DetachSessionChannel(clientID, channelID)
					client := transport.NewOwnerFileRPCClient(channel, channel, channel)
					if err := service.RegisterOwnerFileClient(clientID, client); err != nil {
						diagnostic.Background(service.Diagnostics(), "register ownerfs ssh channel", err)
						_ = channel.Close()
						return
					}
					select {
					case <-connCtx.Done():
					case <-client.Done():
					}
				}(payload.ClientID)
			case transport.SSHChannelStdio:
				payload, err := transport.DecodeStdioOpenPayload(newChannel.ExtraData())
				if err != nil {
					_ = newChannel.Reject(cryptossh.ConnectionFailed, err.Error())
					continue
				}
				channel, requests, err := newChannel.Accept()
				if err != nil {
					return err
				}
				go cryptossh.DiscardRequests(requests)
				handlers.Add(1)
				go func(payload transport.StdioOpenPayload) {
					defer handlers.Done()
					meta := baseMeta
					meta.ChannelKind = control.ChannelKindStdio
					diagnostic.Background(service.Diagnostics(), "serve stdio ssh channel", transport.ServeStdioChannel(
						connCtx,
						service,
						payload.ClientID,
						payload.CommandID,
						channel,
						channel,
						channel,
						meta,
						trace,
					))
				}(payload)
			default:
				_ = newChannel.Reject(cryptossh.UnknownChannelType, "unsupported channel type")
			}
		}
	}
}

func LoadOrCreateSSHHostSigner(path string) (cryptossh.Signer, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return cryptossh.ParsePrivateKey(data)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}
	data = pem.EncodeToMemory(block)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, err
	}
	return cryptossh.ParsePrivateKey(data)
}

func encodePrincipalPermissions(principal control.AuthenticatedPrincipal) *cryptossh.Permissions {
	return &cryptossh.Permissions{
		Extensions: map[string]string{
			"username":         principal.Username,
			"user_disabled":    strconv.FormatBool(principal.UserDisabled),
			"token_id":         principal.TokenID,
			"token_source":     string(principal.TokenSource),
			"authenticated_at": principal.AuthenticatedAt.UTC().Format(time.RFC3339Nano),
		},
	}
}

func principalFromPermissions(permissions *cryptossh.Permissions) (control.AuthenticatedPrincipal, error) {
	if permissions == nil {
		return control.AuthenticatedPrincipal{}, proto.NewError(proto.ErrAuthenticationFailed, "ssh principal is missing")
	}
	extensions := permissions.Extensions
	principal := control.AuthenticatedPrincipal{
		Username:     strings.TrimSpace(extensions["username"]),
		UserDisabled: strings.EqualFold(strings.TrimSpace(extensions["user_disabled"]), "true"),
		TokenID:      strings.TrimSpace(extensions["token_id"]),
		TokenSource:  control.TokenSource(strings.TrimSpace(extensions["token_source"])),
	}
	if principal.Username == "" {
		return control.AuthenticatedPrincipal{}, proto.NewError(proto.ErrAuthenticationFailed, "ssh principal username is missing")
	}
	if value := strings.TrimSpace(extensions["authenticated_at"]); value != "" {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return control.AuthenticatedPrincipal{}, fmt.Errorf("parse authenticated_at: %w", err)
		}
		principal.AuthenticatedAt = parsed
	}
	return principal, nil
}
