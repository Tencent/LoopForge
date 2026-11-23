package gitwork

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"os"
	"slices"
	"strings"

	"agentflow/internal/domain"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type sshKnownHostsContext struct{}
type sshKnownHostsLookupContext struct{}

// SSHKnownHostsLookup reads the platform-managed entries for the requested
// canonical host:port scopes. It is installed only on platform operations.
type SSHKnownHostsLookup func(context.Context, []string) ([]byte, error)

func WithSSHKnownHostsLookup(ctx context.Context, lookup SSHKnownHostsLookup) context.Context {
	if lookup == nil || ctx.Value(sshKnownHostsLookupContext{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, sshKnownHostsLookupContext{}, lookup)
}

// SnapshotSSHKnownHostsLookup loads managed pins before the workspace editor
// can hold a database transaction while it opens a terminal.
func SnapshotSSHKnownHostsLookup(ctx context.Context, keySets ...map[string]string) context.Context {
	lookup, _ := ctx.Value(sshKnownHostsLookupContext{}).(SSHKnownHostsLookup)
	scopes := sshKnownHostScopes(keySets)
	if lookup == nil || len(scopes) == 0 {
		return ctx
	}
	entries, err := lookup(ctx, scopes)
	snapshot := SSHKnownHostsLookup(func(context.Context, []string) ([]byte, error) { return entries, err })
	return context.WithValue(ctx, sshKnownHostsLookupContext{}, snapshot)
}

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
func SSHKnownHostsForKeys(ctx context.Context, keySets ...map[string]string) *domain.GitSSHKnownHosts {
	scopes := sshKnownHostScopes(keySets)
	if len(scopes) == 0 {
		return nil
	}
	endpoints := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		endpoints[knownhosts.Normalize(scope)] = struct{}{}
	}
	content, err := readConfiguredSSHKnownHosts(ctx, scopes)
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

func sshKnownHostScopes(keySets []map[string]string) []string {
	scopes := make(map[string]struct{})
	for _, keys := range keySets {
		for target, key := range keys {
			if strings.TrimSpace(key) == "" {
				continue
			}
			if endpoint, err := SSHHostScope(target); err == nil {
				scopes[endpoint] = struct{}{}
			}
		}
	}
	return slices.Sorted(maps.Keys(scopes))
}

func readConfiguredSSHKnownHosts(ctx context.Context, scopes []string) ([]byte, error) {
	var content []byte
	if path := strings.TrimSpace(os.Getenv(SSHKnownHostsEnv)); path != "" {
		var err error
		content, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("无法读取 %s 指定的主机密钥文件", SSHKnownHostsEnv)
		}
	}
	if lookup, _ := ctx.Value(sshKnownHostsLookupContext{}).(SSHKnownHostsLookup); lookup != nil {
		managed, err := lookup(ctx, scopes)
		if err != nil {
			// The registry contributes positive keys. The deployment file
			// and its revocations remain authoritative during an outage.
			log.Printf("read platform Git SSH host trust registry: %v", err)
		} else if len(managed) > 0 {
			content = append(append(content, '\n'), managed...)
		}
	}
	return content, nil
}

func sshHostKeys(ctx context.Context, remotes []SSHRemote) ([]byte, error) {
	scopes := make([]string, 0, len(remotes))
	endpoints := make(map[string]struct{}, len(remotes))
	for _, remote := range remotes {
		scope := remote.Endpoint()
		scopes = append(scopes, scope)
		endpoints[knownhosts.Normalize(scope)] = struct{}{}
	}
	var platformEntries []byte
	if platform, _ := ctx.Value(sshKnownHostsContext{}).(*domain.GitSSHKnownHosts); platform != nil {
		if platform.ReadFailed {
			return nil, fmt.Errorf("无法读取平台 %s 指定的主机密钥文件", SSHKnownHostsEnv)
		}
		if platform.TooLarge {
			return nil, fmt.Errorf("平台下发的 SSH 主机密钥超过大小上限，请检查平台设置和 %s", SSHKnownHostsEnv)
		}
		platformEntries = []byte(platform.Entries)
	}
	localEntries, err := readConfiguredSSHKnownHosts(ctx, scopes)
	if err != nil {
		return nil, err
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
			return nil, fmt.Errorf("SSH 主机 %s 未获信任。请平台管理员在「平台管理 → Git 主机信任」核验并登记公钥，部署也可通过 %s 配置", endpoint, SSHKnownHostsEnv)
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
