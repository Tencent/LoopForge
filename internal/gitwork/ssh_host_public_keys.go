package gitwork

import (
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

const (
	MaxSSHHostPublicKeyBytes = 16 << 10
	maxSSHHostPublicKeys     = 8
)

// NormalizeSSHHostPublicKeys accepts OpenSSH public keys, one per line. Strip
// comments so that only the key type and blob reach known_hosts.
func NormalizeSSHHostPublicKeys(input string) (string, []string, error) {
	if len(input) > MaxSSHHostPublicKeyBytes {
		return "", nil, fmt.Errorf("SSH 主机公钥不能超过 16 KiB")
	}
	var publicKeys strings.Builder
	var fingerprints []string
	seen := make(map[string]bool)
	for line := range strings.SplitSeq(input, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, _, options, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil || len(options) != 0 || strings.Fields(line)[0] != key.Type() {
			return "", nil, fmt.Errorf("请输入 OpenSSH 格式的主机公钥，每行一把，例如以 ssh-ed25519 开头")
		}
		if _, certificate := key.(*ssh.Certificate); certificate {
			return "", nil, fmt.Errorf("请输入服务器主机公钥")
		}
		fingerprint := ssh.FingerprintSHA256(key)
		if seen[fingerprint] {
			continue
		}
		seen[fingerprint] = true
		publicKeys.Write(ssh.MarshalAuthorizedKey(key))
		fingerprints = append(fingerprints, fingerprint)
		if len(fingerprints) > maxSSHHostPublicKeys {
			return "", nil, fmt.Errorf("每个 SSH 主机最多可登记 %d 把公钥", maxSSHHostPublicKeys)
		}
	}
	if len(fingerprints) == 0 {
		return "", nil, fmt.Errorf("请输入 SSH 主机公钥")
	}
	return strings.TrimSpace(publicKeys.String()), fingerprints, nil
}
