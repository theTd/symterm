package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"symterm/internal/config"

	cryptossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestSSHHostKeyCallbackPromptsAndPersistsUnknownHost(t *testing.T) {
	t.Parallel()

	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")
	signer := mustNewSSHSigner(t)
	prompter := &stubHostKeyPrompter{accepted: true}

	callback, err := sshHostKeyCallback(config.ClientAuthConfig{
		SSHKnownHostsPath: knownHostsPath,
	}, prompter)
	if err != nil {
		t.Fatalf("sshHostKeyCallback() error = %v", err)
	}

	remoteAddr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2222}
	if err := callback("127.0.0.1:2222", remoteAddr, signer.PublicKey()); err != nil {
		t.Fatalf("callback() error = %v", err)
	}
	if prompter.callCount != 1 {
		t.Fatalf("prompter call count = %d, want 1", prompter.callCount)
	}
	data, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatalf("ReadFile(known_hosts) error = %v", err)
	}
	if !strings.Contains(string(data), knownhosts.Normalize("127.0.0.1:2222")) {
		t.Fatalf("known_hosts = %q, want host entry", string(data))
	}
	if err := callback("127.0.0.1:2222", remoteAddr, signer.PublicKey()); err != nil {
		t.Fatalf("callback(second call) error = %v", err)
	}
	if prompter.callCount != 1 {
		t.Fatalf("prompter call count after second call = %d, want 1", prompter.callCount)
	}
}

func TestSSHHostKeyCallbackRejectsUnknownHostWithoutPrompt(t *testing.T) {
	setTestHome(t, t.TempDir())
	signer := mustNewSSHSigner(t)

	callback, err := sshHostKeyCallback(config.ClientAuthConfig{}, nil)
	if err != nil {
		t.Fatalf("sshHostKeyCallback() error = %v", err)
	}
	err = callback("127.0.0.1:2222", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2222}, signer.PublicKey())
	if err == nil {
		t.Fatal("callback() error = nil, want prompt guidance failure")
	}
	if !strings.Contains(err.Error(), "no interactive prompt is available") {
		t.Fatalf("callback() error = %v, want interactive prompt guidance", err)
	}
}

func TestSSHHostKeyCallbackRejectsUnknownHostWhenUserDeclines(t *testing.T) {
	t.Parallel()

	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")
	signer := mustNewSSHSigner(t)

	callback, err := sshHostKeyCallback(config.ClientAuthConfig{
		SSHKnownHostsPath: knownHostsPath,
	}, &stubHostKeyPrompter{})
	if err != nil {
		t.Fatalf("sshHostKeyCallback() error = %v", err)
	}
	err = callback("127.0.0.1:2222", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2222}, signer.PublicKey())
	if err == nil {
		t.Fatal("callback() error = nil, want rejection")
	}
	if !strings.Contains(err.Error(), "was not trusted by the user") {
		t.Fatalf("callback() error = %v, want rejection guidance", err)
	}
	if _, statErr := os.Stat(knownHostsPath); !os.IsNotExist(statErr) {
		t.Fatalf("Stat(known_hosts) error = %v, want not exists", statErr)
	}
}

func TestSSHHostKeyCallbackAllowsExplicitInsecureOverride(t *testing.T) {
	t.Parallel()

	callback, err := sshHostKeyCallback(config.ClientAuthConfig{
		SSHDisableHostKeyCheck: true,
	}, nil)
	if err != nil {
		t.Fatalf("sshHostKeyCallback() error = %v", err)
	}

	signer := mustNewSSHSigner(t)
	if err := callback("127.0.0.1:2222", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2222}, signer.PublicKey()); err != nil {
		t.Fatalf("callback() error = %v, want insecure override to accept host key", err)
	}
}

func TestSSHHostKeyCallbackUsesKnownHostsAndRejectsMismatch(t *testing.T) {
	t.Parallel()

	signer := mustNewSSHSigner(t)
	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")
	entry := knownhosts.Line([]string{knownhosts.Normalize("127.0.0.1:2222")}, signer.PublicKey())
	if err := os.WriteFile(knownHostsPath, []byte(entry+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(known_hosts) error = %v", err)
	}
	prompter := &stubHostKeyPrompter{accepted: true}

	callback, err := sshHostKeyCallback(config.ClientAuthConfig{
		SSHKnownHostsPath: knownHostsPath,
	}, prompter)
	if err != nil {
		t.Fatalf("sshHostKeyCallback() error = %v", err)
	}

	remoteAddr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2222}
	if err := callback("127.0.0.1:2222", remoteAddr, signer.PublicKey()); err != nil {
		t.Fatalf("callback(match) error = %v", err)
	}

	otherSigner := mustNewSSHSigner(t)
	if err := callback("127.0.0.1:2222", remoteAddr, otherSigner.PublicKey()); err == nil {
		t.Fatal("callback(mismatch) error = nil, want host key rejection")
	}
	if prompter.callCount != 0 {
		t.Fatalf("prompter call count = %d, want 0 for known host and mismatch paths", prompter.callCount)
	}
}

type stubHostKeyPrompter struct {
	accepted  bool
	callCount int
	last      HostKeyPrompt
}

func (s *stubHostKeyPrompter) ConfirmUnknownHost(prompt HostKeyPrompt) (bool, error) {
	s.callCount++
	s.last = prompt
	return s.accepted, nil
}

func mustNewSSHSigner(t *testing.T) cryptossh.Signer {
	t.Helper()

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	signer, err := cryptossh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("NewSignerFromKey() error = %v", err)
	}
	return signer
}

func setTestHome(t *testing.T, home string) {
	t.Helper()

	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", filepath.VolumeName(home))
	t.Setenv("HOMEPATH", strings.TrimPrefix(home, filepath.VolumeName(home)))
}
