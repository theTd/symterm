package transport

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"symterm/internal/proto"

	cryptossh "golang.org/x/crypto/ssh"
)

const (
	SSHChannelControl = "symterm-control"
	SSHChannelOwnerFS = "symterm-ownerfs"
	SSHChannelStdio   = "symterm-stdio"
)

type OwnerFSOpenPayload struct {
	ClientID string `json:"client_id"`
}

type StdioOpenPayload struct {
	ClientID  string `json:"client_id"`
	CommandID string `json:"command_id"`
}

func OpenSSHControlChannel(client *cryptossh.Client) (io.ReadWriteCloser, error) {
	channel, requests, err := client.OpenChannel(SSHChannelControl, nil)
	if err != nil {
		return nil, err
	}
	go cryptossh.DiscardRequests(requests)
	return channel, nil
}

func OpenSSHOwnerFSChannel(client *cryptossh.Client, clientID string) (io.ReadWriteCloser, error) {
	return openSSHJSONChannel(client, SSHChannelOwnerFS, OwnerFSOpenPayload{ClientID: clientID})
}

func OpenSSHStdioChannel(client *cryptossh.Client, clientID string, commandID string) (io.ReadWriteCloser, error) {
	return openSSHJSONChannel(client, SSHChannelStdio, StdioOpenPayload{
		ClientID:  clientID,
		CommandID: commandID,
	})
}

func DecodeOwnerFSOpenPayload(extraData []byte) (OwnerFSOpenPayload, error) {
	var payload OwnerFSOpenPayload
	if err := decodeSSHOpenPayload(extraData, &payload); err != nil {
		return OwnerFSOpenPayload{}, err
	}
	if strings.TrimSpace(payload.ClientID) == "" {
		return OwnerFSOpenPayload{}, proto.NewError(proto.ErrInvalidArgument, "ownerfs channel payload requires client_id")
	}
	return payload, nil
}

func DecodeStdioOpenPayload(extraData []byte) (StdioOpenPayload, error) {
	var payload StdioOpenPayload
	if err := decodeSSHOpenPayload(extraData, &payload); err != nil {
		return StdioOpenPayload{}, err
	}
	if strings.TrimSpace(payload.ClientID) == "" {
		return StdioOpenPayload{}, proto.NewError(proto.ErrInvalidArgument, "stdio channel payload requires client_id")
	}
	if strings.TrimSpace(payload.CommandID) == "" {
		return StdioOpenPayload{}, proto.NewError(proto.ErrInvalidArgument, "stdio channel payload requires command_id")
	}
	return payload, nil
}

func openSSHJSONChannel(client *cryptossh.Client, channelType string, payload any) (io.ReadWriteCloser, error) {
	extraData, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	channel, requests, err := client.OpenChannel(channelType, extraData)
	if err != nil {
		return nil, err
	}
	go cryptossh.DiscardRequests(requests)
	return channel, nil
}

func decodeSSHOpenPayload(extraData []byte, target any) error {
	if len(extraData) == 0 {
		return proto.NewError(proto.ErrInvalidArgument, "channel open payload is required")
	}
	decoder := json.NewDecoder(strings.NewReader(string(extraData)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

type sshStdioChannelOpener func(context.Context, string, string) (io.ReadWriteCloser, error)

type NoOpCloser struct{}

func (NoOpCloser) Close() error {
	return nil
}
