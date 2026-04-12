package client

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	endpointssh "symterm/internal/ssh"
)

func newHostKeyPrompter(stdin io.Reader, stdout io.Writer, stderr io.Writer) endpointssh.HostKeyPrompter {
	if !isTerminalStream(stdin) {
		return nil
	}
	writer := terminalPromptWriter(stderr, stdout)
	if writer == nil {
		return nil
	}
	return hostKeyPrompter{
		reader: bufio.NewReader(stdin),
		writer: writer,
	}
}

type hostKeyPrompter struct {
	reader *bufio.Reader
	writer io.Writer
}

func (p hostKeyPrompter) ConfirmUnknownHost(req endpointssh.HostKeyPrompt) (bool, error) {
	if _, err := fmt.Fprintf(p.writer, "Unknown SSH host key for %s\n", req.Host); err != nil {
		return false, err
	}
	if req.RemoteAddress != "" && req.RemoteAddress != req.Host {
		if _, err := fmt.Fprintf(p.writer, "Remote address: %s\n", req.RemoteAddress); err != nil {
			return false, err
		}
	}
	if _, err := fmt.Fprintf(p.writer, "Key type: %s\n", req.KeyType); err != nil {
		return false, err
	}
	if _, err := fmt.Fprintf(p.writer, "SHA256 fingerprint: %s\n", req.FingerprintSHA); err != nil {
		return false, err
	}
	if _, err := fmt.Fprintf(p.writer, "known_hosts file: %s\n", req.KnownHostsPath); err != nil {
		return false, err
	}
	if _, err := io.WriteString(p.writer, "Trust this host and save it? [y/N]: "); err != nil {
		return false, err
	}

	line, err := p.reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer := strings.TrimSpace(strings.ToLower(line))
	return answer == "y" || answer == "yes", nil
}

func terminalPromptWriter(candidates ...io.Writer) io.Writer {
	for _, candidate := range candidates {
		if isTerminalStream(candidate) {
			return candidate
		}
	}
	return nil
}

func isTerminalStream(stream any) bool {
	file, ok := stream.(*os.File)
	if !ok || file == nil {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
