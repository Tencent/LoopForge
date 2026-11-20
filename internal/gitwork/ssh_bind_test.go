package gitwork

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"

	"golang.org/x/crypto/ssh"
)

func TestBindSSHRemotesRewritesSortedURLsAfterExistingConfig(t *testing.T) {
	keyed := "ssh://git@git.example.org:2222/group/b.git"
	plain := "git@gitlab.com:group/a.git"
	env := map[string]string{"GIT_CONFIG_COUNT": "1"}
	bindings, remotes, err := bindSSHRemotes(env, map[string]string{keyed: "KEY", plain: ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || len(remotes) != 1 {
		t.Fatalf("bindings=%d remotes=%d, want 1/1", len(bindings), len(remotes))
	}
	remote, err := ParseSSHRemote(keyed)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(remote.URL))
	alias := fmt.Sprintf("lf-%x", sum[:16])
	if got := bindings[0]; got.alias != alias || got.key != "KEY" || got.remote != remote || remotes[0] != remote {
		t.Fatalf("binding = %+v, want alias %s for %+v", got, alias, remote)
	}
	want := map[string]string{
		"GIT_CONFIG_COUNT":   "3",
		"GIT_CONFIG_KEY_1":   "url." + plain + ".insteadOf",
		"GIT_CONFIG_VALUE_1": plain,
		"GIT_CONFIG_KEY_2":   "url." + remote.aliasURL(alias) + ".insteadOf",
		"GIT_CONFIG_VALUE_2": keyed,
	}
	if len(env) != len(want) {
		t.Fatalf("env = %v, want %v", env, want)
	}
	for k, v := range want {
		if env[k] != v {
			t.Fatalf("env[%s] = %q, want %q", k, env[k], v)
		}
	}
}

func TestBindSSHRemotesRejectsInvalidRemote(t *testing.T) {
	env := map[string]string{}
	if _, _, err := bindSSHRemotes(env, map[string]string{"https://example.com/a.git": "KEY"}); err == nil {
		t.Fatal("expected invalid SSH remote error")
	}
}

func TestStartSSHWithoutAssignedKeyOnlyRewritesURLs(t *testing.T) {
	raw := "git@gitlab.com:group/a.git"
	env := map[string]string{}
	stop, err := StartSSH(t.Context(), env, map[string]string{raw: ""}, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if env["GIT_CONFIG_COUNT"] != "1" || env["GIT_CONFIG_VALUE_0"] != raw || env["GIT_SSH_VARIANT"] != "ssh" {
		t.Fatalf("env = %v", env)
	}
	if env["GIT_SSH_COMMAND"] == "" {
		t.Fatal("expected failing GIT_SSH_COMMAND without assigned key")
	}
}

// A workspace CLI clones whatever URL it likes, so personal keys are bound
// by host:port instead of by registered remote.
func TestStartSSHHostsBindsByEndpoint(t *testing.T) {
	env := map[string]string{}
	stop, err := StartSSHHosts(t.Context(), env, map[string]string{"git.example.org:22": "", "other.example.org:2222": " "}, t.TempDir(), "")
	if err != nil || len(env) != 0 {
		t.Fatalf("keys without content must bind nothing: env=%v err=%v", env, err)
	}
	stop()
	if _, err := StartSSHHosts(t.Context(), map[string]string{}, map[string]string{"git.example.org": "KEY"}, t.TempDir(), ""); err == nil {
		t.Fatal("an endpoint without a port was accepted")
	}
	if got := (sshBinding{alias: "lf-x", match: "git.example.org"}).matchHost(); got != "git.example.org" {
		t.Fatalf("host-bound match = %q", got)
	}
	if got := (sshBinding{alias: "lf-x"}).matchHost(); got != "lf-x" {
		t.Fatalf("remote-bound match = %q", got)
	}
}

func TestBuiltinKnownHostsTrustGitHubOnly(t *testing.T) {
	t.Setenv(SSHKnownHostsEnv, "")
	pinned, err := sshHostKeys(t.Context(), []SSHRemote{{Host: "github.com", Port: "22"}, {Host: "ssh.github.com", Port: "443"}})
	if err != nil {
		t.Fatalf("github.com should be trusted without configuration: %v", err)
	}
	if got := string(pinned); !strings.Contains(got, "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl") || !strings.Contains(got, "[ssh.github.com]:443 ssh-rsa") {
		t.Fatalf("pinned keys = %s", got)
	}
	if _, err := sshHostKeys(t.Context(), []SSHRemote{{Host: "git.example.org", Port: "22"}}); err == nil || !strings.Contains(err.Error(), SSHKnownHostsEnv) {
		t.Fatalf("other hosts still need configured keys: %v", err)
	}
	file := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(file, []byte("git.example.org ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SSHKnownHostsEnv, file)
	if _, err := sshHostKeys(t.Context(), []SSHRemote{{Host: "git.example.org", Port: "22"}, {Host: "github.com", Port: "22"}}); err != nil {
		t.Fatalf("configured and built-in keys combine: %v", err)
	}
}

func TestPlatformSSHKnownHostsAreScopedAndTakePrecedence(t *testing.T) {
	_, _, publicKey, _, _, err := ssh.ParseKnownHosts([]byte(builtinKnownHosts))
	if err != nil {
		t.Fatal(err)
	}
	key := string(ssh.MarshalAuthorizedKey(publicKey))
	const host = "[gitlab.example.org]:2222"
	const remote = "ssh://git@gitlab.example.org:2222/team/app.git"
	const legacyHost = "legacy.example.org"
	platformFile := filepath.Join(t.TempDir(), "platform_known_hosts")
	content := host + ",gitlab.example.org,unrelated.example.org " + key +
		"*.example.org " + key + "unassigned.example.org " + key
	if err := os.WriteFile(platformFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SSHKnownHostsEnv, platformFile)
	platform := SSHKnownHostsForKeys(t.Context(), map[string]string{remote: "identity", "git@unassigned.example.org:group/repo": ""})
	if platform == nil || platform.Entries != host+" "+key || platform.ReadFailed {
		t.Fatalf("scoped platform pins = %+v", platform)
	}

	localFile := filepath.Join(t.TempDir(), "runner_known_hosts")
	local := "@cert-authority " + host + " " + key + legacyHost + " " + key
	if err := os.WriteFile(localFile, []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SSHKnownHostsEnv, localFile)
	ctx := WithSSHKnownHosts(t.Context(), platform)
	pinned, err := sshHostKeys(ctx, []SSHRemote{{Host: "gitlab.example.org", Port: "2222"}, {Host: legacyHost, Port: "22"}})
	if err != nil || string(pinned) != platform.Entries+legacyHost+" "+key {
		t.Fatalf("platform and runner pins = %q, err = %v", pinned, err)
	}
	if _, err := sshHostKeys(ctx, []SSHRemote{{Host: "gitlab.example.org", Port: "22"}}); err == nil {
		t.Fatal("known hosts pin was accepted on a different port")
	}

	repo, err := ParseSSHRemote(remote)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(platformFile, []byte("@revoked "+platform.Entries), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SSHKnownHostsEnv, platformFile)
	revoked := SSHKnownHostsForKeys(t.Context(), map[string]string{repo.Endpoint(): "identity"})
	if revoked == nil || revoked.Entries != "@revoked "+platform.Entries {
		t.Fatalf("revocation was lost on dispatch: %+v", revoked)
	}
	t.Setenv(SSHKnownHostsEnv, "")
	if _, err := sshHostKeys(WithSSHKnownHosts(t.Context(), revoked), []SSHRemote{repo}); err == nil || !strings.Contains(err.Error(), "未获信任") {
		t.Fatalf("a revocation was treated as a trusted host key: %v", err)
	}
}

func TestRunnerSSHRevocationSurvivesPlatformKnownHosts(t *testing.T) {
	_, _, builtinKey, _, remaining, err := ssh.ParseKnownHosts([]byte(builtinKnownHosts))
	if err != nil {
		t.Fatal(err)
	}
	_, _, platformKey, _, _, err := ssh.ParseKnownHosts(remaining)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte("github.com "+string(ssh.MarshalAuthorizedKey(platformKey))), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SSHKnownHostsEnv, path)
	platform := SSHKnownHostsForKeys(t.Context(), map[string]string{"github.com:22": "identity"})
	if platform == nil {
		t.Fatal("platform host pin is missing")
	}
	revoked := "@revoked github.com " + string(ssh.MarshalAuthorizedKey(builtinKey))
	if err := os.WriteFile(path, []byte(revoked), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithSSHKnownHosts(t.Context(), platform)
	pinned, err := sshHostKeys(ctx, []SSHRemote{{Host: "github.com", Port: "22"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pinned), revoked) {
		t.Fatal("the platform host pin displaced the runner-local revocation")
	}
}

func TestUnreadablePlatformSSHKnownHostsFailsClosed(t *testing.T) {
	t.Setenv(SSHKnownHostsEnv, filepath.Join(t.TempDir(), "missing"))
	platform := SSHKnownHostsForKeys(t.Context(), map[string]string{"github.com:22": "identity"})
	if platform == nil || !platform.ReadFailed {
		t.Fatalf("platform read failure = %+v", platform)
	}
	t.Setenv(SSHKnownHostsEnv, "")
	ctx := WithSSHKnownHosts(t.Context(), platform)
	if _, err := sshHostKeys(ctx, []SSHRemote{{Host: "github.com", Port: "22"}}); err == nil || !strings.Contains(err.Error(), "无法读取平台") {
		t.Fatalf("platform read failure was hidden: %v", err)
	}
}

func TestSSHHostTrustRegistryReadFailureUsesKnownHostsFallbacks(t *testing.T) {
	_, _, key, _, _, err := ssh.ParseKnownHosts([]byte(builtinKnownHosts))
	if err != nil {
		t.Fatal(err)
	}
	const host = "git.example.org"
	publicKey := string(ssh.MarshalAuthorizedKey(key))
	configured := "@revoked github.com " + publicKey + host + " " + publicKey
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte(configured), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SSHKnownHostsEnv, path)
	keys := map[string]string{"github.com:22": "identity", host + ":22": "identity"}
	ctx := WithSSHKnownHostsLookup(t.Context(), func(context.Context, []string) ([]byte, error) {
		return []byte("registry.example.org " + publicKey), errors.New("registry unavailable")
	})
	platform := SSHKnownHostsForKeys(ctx, keys)
	if platform == nil || platform.ReadFailed || !strings.Contains(platform.Entries, "@revoked github.com "+publicKey) ||
		!strings.Contains(platform.Entries, host+" "+publicKey) {
		t.Fatalf("fallback pins sent to Runner: %+v", platform)
	}

	remotes := []SSHRemote{{Host: "github.com", Port: "22"}, {Host: host, Port: "22"}}
	pins, err := sshHostKeys(SnapshotSSHKnownHostsLookup(ctx, keys), remotes)
	if err != nil || !strings.Contains(string(pins), "github.com ssh-rsa ") || !strings.Contains(string(pins), "@revoked github.com "+publicKey) ||
		!strings.Contains(string(pins), host+" "+publicKey) {
		t.Fatalf("local fallback pins: %s, %v", pins, err)
	}
	if _, err := sshHostKeys(ctx, []SSHRemote{{Host: "registry.example.org", Port: "22"}}); err == nil {
		t.Fatal("accepted a pin returned by a failed registry read")
	}
	t.Setenv(SSHKnownHostsEnv, "")
	runnerPins, err := sshHostKeys(WithSSHKnownHosts(t.Context(), platform), remotes)
	if err != nil || string(runnerPins) != string(pins) {
		t.Fatalf("Runner fallback pins: %s, %v", runnerPins, err)
	}
	if platform := SSHKnownHostsForKeys(ctx, keys); platform != nil {
		t.Fatalf("registry-only read failure was sent to the Runner: %+v", platform)
	}
}

func TestOversizedPlatformSSHKnownHostsFailsClosed(t *testing.T) {
	_, _, publicKey, _, _, err := ssh.ParseKnownHosts([]byte(builtinKnownHosts))
	if err != nil {
		t.Fatal(err)
	}
	const host = "gitlab.example.org"
	line := host + " " + string(ssh.MarshalAuthorizedKey(publicKey))
	// A line's newline takes two bytes in JSON. The unencoded file fits in the
	// allowance, but the matching entries do not fit after escaping.
	lines := domain.RunnerSSHKnownHostsWireLimit/(len(line)+1) + 1
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte(strings.Repeat(line, lines)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SSHKnownHostsEnv, path)
	platform := SSHKnownHostsForKeys(t.Context(), map[string]string{host + ":22": "identity"})
	if platform == nil {
		t.Fatal("oversized platform config was omitted")
	}
	if !platform.TooLarge || platform.ReadFailed || platform.Entries != "" {
		t.Fatalf("oversized platform config has %d entry bytes, tooLarge = %v, readFailed = %v", len(platform.Entries), platform.TooLarge, platform.ReadFailed)
	}
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithSSHKnownHosts(t.Context(), platform)
	if _, err := sshHostKeys(ctx, []SSHRemote{{Host: host, Port: "22"}}); err == nil || !strings.Contains(err.Error(), "超过大小上限") {
		t.Fatalf("oversized platform pins were silently bypassed: %v", err)
	}
}
