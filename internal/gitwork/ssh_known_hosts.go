package gitwork

import (
	"bytes"
	"context"
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
		entries.Write(matches[endpoint].lines)
	}
	return &domain.GitSSHKnownHosts{Entries: entries.String()}
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
		platformEntries = []byte(platform.Entries)
	}
	configured := matchingSSHKnownHosts(platformEntries, endpoints)
	// A platform entry takes precedence for its exact host and port, including
	// an entry that only revokes a key. Local pins cover the remaining hosts.
	if len(configured) < len(endpoints) {
		localEndpoints := maps.Clone(endpoints)
		for endpoint := range configured {
			delete(localEndpoints, endpoint)
		}
		content, err := readConfiguredSSHKnownHosts()
		if err != nil {
			return nil, fmt.Errorf("无法读取 %s 指定的主机密钥文件", SSHKnownHostsEnv)
		}
		maps.Copy(configured, matchingSSHKnownHosts(content, localEndpoints))
	}
	builtin := matchingSSHKnownHosts([]byte(builtinKnownHosts), endpoints)
	var pinned, bundled []byte
	for _, endpoint := range slices.Sorted(maps.Keys(endpoints)) {
		pins, defaults := configured[endpoint], builtin[endpoint]
		if !pins.hasKey && !defaults.hasKey {
			return nil, fmt.Errorf("SSH 主机 %s 未获信任：平台只内置了 github.com 的公钥，其他主机请管理员在 %s 中添加并核对该主机和端口的公钥", endpoint, SSHKnownHostsEnv)
		}
		pinned = append(pinned, pins.lines...)
		bundled = append(bundled, defaults.lines...)
	}
	return append(pinned, bundled...), nil
}

type sshHostPins struct {
	lines  []byte
	hasKey bool
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
			if marker != "" {
				pins.lines = fmt.Appendf(pins.lines, "@%s ", marker)
			}
			pins.lines = fmt.Appendf(pins.lines, "%s %s", host, ssh.MarshalAuthorizedKey(key))
			pins.hasKey = pins.hasKey || marker != "revoked"
			matches[host] = pins
		}
	}
	return matches
}
