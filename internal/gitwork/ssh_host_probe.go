package gitwork

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const sshHostKeyProbeTimeout = 5 * time.Second

var errSSHHostKeyObserved = errors.New("SSH host key observed")

// ProbeSSHHostPublicKey returns the raw key presented by one SSH endpoint.
// Its caller must verify the fingerprint through a trusted source before
// registering the key. The handshake stops before user authentication.
func ProbeSSHHostPublicKey(ctx context.Context, address string, dial func(context.Context, string, string) (net.Conn, error)) (string, error) {
	endpoint, err := SSHHostScope(address)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, sshHostKeyProbeTimeout)
	defer cancel()

	conn, err := dial(ctx, "tcp", endpoint)
	if err != nil {
		return "", fmt.Errorf("无法连接 SSH 主机 %s: %w", endpoint, err)
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return "", err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	var key ssh.PublicKey
	_, _, _, err = ssh.NewClientConn(conn, endpoint, &ssh.ClientConfig{
		User: "git",
		HostKeyAlgorithms: []string{
			ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384,
			ssh.KeyAlgoECDSA521, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256,
		},
		HostKeyCallback: func(_ string, _ net.Addr, observed ssh.PublicKey) error {
			key = observed
			return errSSHHostKeyObserved
		},
	})
	if !errors.Is(err, errSSHHostKeyObserved) {
		return "", fmt.Errorf("无法读取 SSH 主机 %s 的公钥: %w", endpoint, err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), nil
}
