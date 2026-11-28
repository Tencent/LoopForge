package workspacessh

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

const ServerCommand = "workspace-ssh-server"

type Keys struct {
	HostSeed  []byte
	ClientKey []byte
}

// HostSeed gives each Run a stable SSH identity across API and sandbox restarts.
func HostSeed(secret, runID string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("loopforge:workspace-ssh:host-key:v1\x00"))
	mac.Write([]byte(runID))
	return mac.Sum(nil)
}

func hostSigner(seed []byte) (ssh.Signer, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("invalid workspace SSH host key")
	}
	return ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(seed))
}

func HostPublicKey(seed []byte) (string, error) {
	signer, err := hostSigner(seed)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), nil
}

func ClientPublicKey(raw []byte) ([]byte, error) {
	if len(raw) > 4096 {
		return nil, fmt.Errorf("workspace SSH public key is too large")
	}
	key, _, options, rest, err := ssh.ParseAuthorizedKey(raw)
	if err != nil || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("workspace SSH requires one bare OpenSSH public key")
	}
	if _, certificate := key.(*ssh.Certificate); certificate {
		return nil, fmt.Errorf("workspace SSH requires a public key, without a certificate")
	}
	return key.Marshal(), nil
}
