package gitwork

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"agentflow/internal/domain"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type sshKnownHostsContext struct{}

// WithSSHKnownHosts makes the platform's pins available to Git commands and
// sandbox credential bindings for this runner request.
func WithSSHKnownHosts(ctx context.Context, knownHosts *domain.GitSSHKnownHosts) context.Context {
	if knownHosts == nil {
		return ctx
	}
	return context.WithValue(ctx, sshKnownHostsContext{}, knownHosts)
}

// SSHKnownHostsForKeys selects configured host keys for an authenticated
// runner dispatch. Each set maps clone URLs or host:port scopes to identities.
func SSHKnownHostsForKeys(keySets ...map[string]string) *domain.GitSSHKnownHosts {
	endpoints := make(map[string]struct{})
	for _, keys := range keySets {
		for target, key := range keys {
			if strings.TrimSpace(key) == "" {
				continue
			}
			if endpoint, err := SSHHostScope(target); err == nil {
				endpoints[knownhosts.Normalize(endpoint)] = struct{}{}
			}
		}
	}
	if len(endpoints) == 0 {
		return nil
	}
	content, err := readConfiguredSSHKnownHosts()
	if err != nil {
		return &domain.GitSSHKnownHosts{ReadFailed: true}
	}
	matches := matchingSSHKnownHosts(content, endpoints)
	if len(matches) == 0 {
		return nil
	}
	var entries strings.Builder
	for _, endpoint := range slices.Sorted(maps.Keys(matches)) {
		pins := matches[endpoint]
		entries.Write(pins.revocations)
		entries.Write(pins.keys)
	}
	pins := &domain.GitSSHKnownHosts{Entries: entries.String()}
	encoded, _ := json.Marshal(pins)
	const claimFieldPrefix = len(`,"gitSshKnownHosts":`)
	if len(encoded)+claimFieldPrefix > domain.RunnerSSHKnownHostsWireLimit {
		return &domain.GitSSHKnownHosts{TooLarge: true}
	}
	return pins
}

func readConfiguredSSHKnownHosts() ([]byte, error) {
	path := strings.TrimSpace(os.Getenv(SSHKnownHostsEnv))
	if path == "" {
		return nil, nil
	}
	return os.ReadFile(path)
}

func sshHostKeys(ctx context.Context, remotes []SSHRemote) ([]byte, error) {
	endpoints := make(map[string]struct{}, len(remotes))
	for _, remote := range remotes {
		endpoints[knownhosts.Normalize(remote.Endpoint())] = struct{}{}
	}
	var platformEntries []byte
	if platform, _ := ctx.Value(sshKnownHostsContext{}).(*domain.GitSSHKnownHosts); platform != nil {
		if platform.ReadFailed {
			return nil, fmt.Errorf("无法读取平台 %s 指定的主机密钥文件", SSHKnownHostsEnv)
		}
		if platform.TooLarge {
			return nil, fmt.Errorf("平台下发的 SSH 主机密钥超过大小上限，请检查 %s 中匹配的条目", SSHKnownHostsEnv)
		}
		platformEntries = []byte(platform.Entries)
	}
	localEntries, err := readConfiguredSSHKnownHosts()
	if err != nil {
		return nil, fmt.Errorf("无法读取 %s 指定的主机密钥文件", SSHKnownHostsEnv)
	}
	platform := matchingSSHKnownHosts(platformEntries, endpoints)
	local := matchingSSHKnownHosts(localEntries, endpoints)
	builtin := matchingSSHKnownHosts([]byte(builtinKnownHosts), endpoints)
	var pinned, bundled []byte
	for _, endpoint := range slices.Sorted(maps.Keys(endpoints)) {
		platformPins := platform[endpoint]
		localPins, defaults := local[endpoint], builtin[endpoint]
		// Use platform trust when supplied for this endpoint. Revocations from
		// both configurations apply regardless of the source of the trusted keys.
		pinned = append(pinned, localPins.revocations...)
		pinned = append(pinned, platformPins.revocations...)
		keys := platformPins.keys
		if len(keys) == 0 {
			keys = localPins.keys
		}
		if len(keys) == 0 && len(defaults.keys) == 0 {
			return nil, fmt.Errorf("SSH 主机 %s 未获信任：平台只内置了 github.com 的公钥，其他主机请管理员在 %s 中添加并核对该主机和端口的公钥", endpoint, SSHKnownHostsEnv)
		}
		pinned = append(pinned, keys...)
		bundled = append(bundled, defaults.revocations...)
		bundled = append(bundled, defaults.keys...)
	}
	return append(pinned, bundled...), nil
}

type sshHostPins struct {
	keys        []byte
	revocations []byte
}

func matchingSSHKnownHosts(content []byte, endpoints map[string]struct{}) map[string]sshHostPins {
	matches := make(map[string]sshHostPins)
	for line := range bytes.SplitSeq(content, []byte("\n")) {
		marker, hosts, key, _, _, err := ssh.ParseKnownHosts(line)
		if err != nil || (marker != "" && marker != "revoked" && marker != "cert-authority") {
			continue
		}
		for _, host := range hosts {
			host = strings.ToLower(host)
			if _, ok := endpoints[host]; !ok {
				continue
			}
			pins := matches[host]
			if marker == "revoked" {
				pins.revocations = fmt.Appendf(pins.revocations, "@revoked %s %s", host, ssh.MarshalAuthorizedKey(key))
			} else {
				if marker == "cert-authority" {
					pins.keys = append(pins.keys, "@cert-authority "...)
				}
				pins.keys = fmt.Appendf(pins.keys, "%s %s", host, ssh.MarshalAuthorizedKey(key))
			}
			matches[host] = pins
		}
	}
	return matches
}
