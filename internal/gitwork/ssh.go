package gitwork

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

const SSHKnownHostsEnv = "AGENTFLOW_GIT_SSH_KNOWN_HOSTS"

const SSHKeyHint = "请到「个人配置 → Git SSH 密钥」为该 SSH 主机添加私钥，并在代码托管平台为对应公钥授予仓库权限"

var ErrSSHKeyRequired = errors.New("未配置 Git SSH 密钥。" + SSHKeyHint)

func NormalizeSSHPrivateKey(value string) (string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\r\n", "\n") + "\n"
	if len(value) > 16<<10 {
		return "", errors.New("Git SSH 私钥不能超过 16 KiB")
	}
	if _, _, err := parseSSHIdentity(value); err != nil {
		return "", err
	}
	return value, nil
}

func parseSSHIdentity(value string) (any, ssh.Signer, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil, ErrSSHKeyRequired
	}
	key, err := ssh.ParseRawPrivateKey([]byte(value))
	var passphrase *ssh.PassphraseMissingError
	if errors.As(err, &passphrase) {
		return nil, nil, errors.New("Git SSH 密钥需要口令，无法用于无人值守；请使用单独生成的无口令部署密钥")
	}
	if err == nil {
		if signer, err := ssh.NewSignerFromKey(key); err == nil {
			return key, signer, nil
		}
	}
	return nil, nil, errors.New("Git SSH 私钥格式无效，请粘贴完整的 OpenSSH 或 PEM 私钥")
}

// builtinKnownHosts pins GitHub's published SSH host keys (api.github.com/meta,
// fingerprints SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU ed25519,
// p2QAMXNIC1TJYWeIOttrVc98/R1BUFWu3/LiyKgUfQM ecdsa, uNiVztksCsDhcc0u9e8BujQXVUpKZIDTMczCvj3tD2s rsa),
// for github.com:22 and the ssh.github.com:443 fallback. Other hosts still
// need AGENTFLOW_GIT_SSH_KNOWN_HOSTS; entries there (including @revoked) are
// read alongside these.
const builtinKnownHosts = `github.com,[ssh.github.com]:443 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl
github.com,[ssh.github.com]:443 ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEmKSENjQEezOmxkZMy7opKgwFB9nkt5YRrYMjNuG5N87uRgg6CLrbo5wAdT/y6v0mKV0U2w0WZ2YB/++Tpockg=
github.com,[ssh.github.com]:443 ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCj7ndNxQowgcQnjshcLrqPEiiphnt+VTTvDP6mHBL9j1aNUkY4Ue1gvwnGLVlOhGeYrnZaMgRK6+PKCUXaDbC7qtbW8gIkhL7aGCsOr/C56SJMy/BCZfxd1nWzAOxSDPgVsmerOBYfNqltV9/hWCqBywINIR+5dIg6JTJ72pcEpEjcYgXkE2YEFXV1JHnsKgbLWNlhScqb2UmyRkQyytRLtL+38TGxkxCflmO+5Z8CSSNY7GidjMIZ7Q4zMjA2n1nGrlTDkzwDCsw+wqFPGQA179cnfGWOWRVruj16z6XyvxvjJwbz0wQZ75XK5tKSb7FNyeIEs4TT4jk+S4dhPeAUC5y+bDYirYgM4GC7uEnztnZyaVWQ7B381AK4Qdrwt51ZqExKbQpTUNn+EjqoTwvqNj4kqx5QUCI0ThS/YkOxJCXmPUWZbhjpCg56i+2aB6CmK2JGhn57K5mj0MNdBXA4/WnwH6XoPWJzK5Nyu2zB3nAZp+S5hpQs+p1vN1/wsjk=
`

// sshBinding ties one repository remote to its host alias and private key.
type sshBinding struct {
	remote SSHRemote
	alias  string
	key    string
	// match is the host name ssh is invoked with; the repository alias for a
	// URL binding, the real host for a host binding.
	match string
}

// bindSSHRemotes appends one Git URL rewrite per repository to environment in
// sorted URL order and returns the repositories that have an assigned key.
func bindSSHRemotes(environment map[string]string, keys map[string]string) ([]sshBinding, []SSHRemote, error) {
	urls := make([]string, 0, len(keys))
	for raw := range keys {
		urls = append(urls, raw)
	}
	sort.Strings(urls)
	var bindings []sshBinding
	var remotes []SSHRemote
	configCount, _ := strconv.Atoi(environment["GIT_CONFIG_COUNT"])
	for _, raw := range urls {
		remote, err := ParseSSHRemote(raw)
		if err != nil {
			return nil, nil, err
		}
		destination := raw
		if key := keys[raw]; key != "" {
			sum := sha256.Sum256([]byte(remote.URL))
			alias := fmt.Sprintf("lf-%x", sum[:16])
			destination = remote.aliasURL(alias)
			bindings = append(bindings, sshBinding{remote: remote, alias: alias, key: key})
			remotes = append(remotes, remote)
		}
		// The exact URL takes priority over the legacy host-wide HTTPS
		// rewrite, including when this repository has no assigned key.
		suffix := strconv.Itoa(configCount)
		environment["GIT_CONFIG_KEY_"+suffix] = "url." + destination + ".insteadOf"
		environment["GIT_CONFIG_VALUE_"+suffix] = raw
		configCount++
	}
	environment["GIT_CONFIG_COUNT"] = strconv.Itoa(configCount)
	return bindings, remotes, nil
}

// StartSSH adds one Git URL rewrite and one OpenSSH identity per repository.
// Aliases bind each repository to its selected host key.
// The filesystem contains a socket and public data only. clientRoot is the
// mount path corresponding to root for a client in a container.
func StartSSH(ctx context.Context, environment map[string]string, keys map[string]string, root, clientRoot string) (func(), error) {
	if len(keys) == 0 {
		return func() {}, nil
	}
	bindings, remotes, err := bindSSHRemotes(environment, keys)
	if err != nil {
		return nil, err
	}
	return startSSHSession(ctx, environment, bindings, remotes, root, clientRoot)
}

// StartSSHHosts gives every SSH remote on the given hosts the matching key,
// without knowing the repositories in advance: an agent that clones
// git@github.com:org/repo.git itself gets the requester's github.com key.
// keys maps "host:port" endpoints (as stored per user) to private keys.
func StartSSHHosts(ctx context.Context, environment map[string]string, keys map[string]string, root, clientRoot string) (func(), error) {
	endpoints := make([]string, 0, len(keys))
	for endpoint, key := range keys {
		if strings.TrimSpace(key) != "" {
			endpoints = append(endpoints, endpoint)
		}
	}
	if len(endpoints) == 0 {
		return func() {}, nil
	}
	sort.Strings(endpoints)
	var bindings []sshBinding
	var remotes []SSHRemote
	for _, endpoint := range endpoints {
		host, port, err := net.SplitHostPort(endpoint)
		if err != nil {
			return nil, fmt.Errorf("invalid Git SSH host %q", endpoint)
		}
		remote := SSHRemote{Host: strings.ToLower(host), Port: port}
		sum := sha256.Sum256([]byte(remote.Endpoint()))
		bindings = append(bindings, sshBinding{remote: remote, alias: fmt.Sprintf("lf-%x", sum[:16]), key: keys[endpoint], match: remote.Host})
		remotes = append(remotes, remote)
	}
	return startSSHSession(ctx, environment, bindings, remotes, root, clientRoot)
}

func startSSHSession(ctx context.Context, environment map[string]string, bindings []sshBinding, remotes []SSHRemote, root, clientRoot string) (func(), error) {
	environment["GIT_SSH_VARIANT"] = "ssh"
	if len(bindings) == 0 {
		// Keep terminals usable for editing without using a host identity.
		environment["GIT_SSH_COMMAND"] = `sh -c 'echo "未配置 Git SSH 密钥，请在个人配置中添加" >&2; exit 1' --`
		return func() {}, nil
	}
	pinned, err := sshHostKeys(ctx, remotes)
	if err != nil {
		return nil, err
	}
	keyring := agent.NewKeyring()
	publicKeys := make(map[string][]byte)
	var config strings.Builder
	for _, item := range bindings {
		r := item.remote
		keyFile, loaded := publicKeys[item.key]
		if !loaded {
			key, signer, err := parseSSHIdentity(item.key)
			if err != nil {
				return nil, err
			}
			if err := keyring.Add(agent.AddedKey{PrivateKey: key}); err != nil {
				return nil, errors.New("无法将 Git SSH 密钥加载到临时代理")
			}
			keyFile = ssh.MarshalAuthorizedKey(signer.PublicKey())
			publicKeys[item.key] = keyFile
		}
		// Only the numeric port is expanded by Match exec. The host key is
		// checked against the real endpoint, including a custom port.
		fmt.Fprintf(&config, "Match originalhost %s exec \"test %%p = %s\"\n  HostName %s\n  HostKeyAlias %s\n  ProxyCommand none\n  IdentityAgent ./agent\n  IdentityFile ./%s.pub\n  UserKnownHostsFile ./known_hosts\n",
			item.matchHost(), r.Port, r.Host, knownhosts.Normalize(r.Endpoint()), item.alias)
	}
	config.WriteString(`Host *
  ProxyCommand /bin/false
  IdentityAgent none
  UserKnownHostsFile /dev/null
  GlobalKnownHostsFile /dev/null
  StrictHostKeyChecking yes
  CheckHostIP no
  UpdateHostKeys no
  BatchMode yes
  PreferredAuthentications publickey
  IdentitiesOnly yes
  ForwardAgent no
  ClearAllForwardings yes
  ControlMaster no
  ConnectTimeout 15
`)
	dir, err := os.MkdirTemp(root, "lf-ssh-")
	if err != nil {
		return nil, fmt.Errorf("create Git SSH session: %w", err)
	}
	files := map[string][]byte{"config": []byte(config.String()), "known_hosts": pinned}
	for _, item := range bindings {
		files[item.alias+".pub"] = publicKeys[item.key]
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("write Git SSH session configuration: %w", err)
		}
	}
	listener, err := listenSSHAgent(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("start Git SSH agent: %w", err)
	}
	stop := serveSSHAgent(listener, keyring)
	clientDir := dir
	if clientRoot != "" {
		clientDir = filepath.Join(clientRoot, filepath.Base(dir))
	}
	environment["GIT_SSH_COMMAND"] = "cd " + shellQuote(clientDir) + " && exec ssh -F ./config"
	return sync.OnceFunc(func() {
		stop()
		_ = os.RemoveAll(dir)
	}), nil
}

func (b sshBinding) matchHost() string {
	if b.match != "" {
		return b.match
	}
	return b.alias
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'" }

func listenSSHAgent(dir string) (net.Listener, error) {
	path := filepath.Join(dir, "agent")
	if len(path) < 100 {
		return net.Listen("unix", path)
	}
	// Unix socket addresses have a small fixed length. Bind through a short
	// alias; the socket itself remains in the workspace's private directory.
	alias, err := os.MkdirTemp("/tmp", "lf-ssh-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(alias)
	if err := os.Symlink(dir, filepath.Join(alias, "d")); err != nil {
		return nil, err
	}
	return net.Listen("unix", filepath.Join(alias, "d", "agent"))
}

func serveSSHAgent(listener net.Listener, keyring agent.Agent) func() {
	var mu sync.Mutex
	clients := map[net.Conn]bool{}
	closed := false
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			if closed {
				mu.Unlock()
				_ = conn.Close()
				return
			}
			clients[conn] = true
			mu.Unlock()
			go func() {
				_ = agent.ServeAgent(keyring, conn)
				_ = conn.Close()
				mu.Lock()
				delete(clients, conn)
				mu.Unlock()
			}()
		}
	}()
	return func() {
		_ = listener.Close()
		mu.Lock()
		defer mu.Unlock()
		closed = true
		for conn := range clients {
			_ = conn.Close()
		}
		_ = keyring.RemoveAll()
	}
}

type sshKeysContext struct{}
type sshRemoteContext struct{}

func WithSSHKeys(ctx context.Context, keys map[string]string) context.Context {
	return context.WithValue(ctx, sshKeysContext{}, keys)
}

func WithSSHRemote(ctx context.Context, remote string) context.Context {
	return context.WithValue(ctx, sshRemoteContext{}, remote)
}

func UsesSSH(ctx context.Context) bool {
	remote, _ := ctx.Value(sshRemoteContext{}).(string)
	return remote != ""
}

func sshCommandEnvironment(ctx context.Context, args []string) (map[string]string, func(), error) {
	remote, _ := ctx.Value(sshRemoteContext{}).(string)
	if remote == "" {
		return nil, func() {}, nil
	}
	if len(args) >= 2 && args[0] == "-C" {
		args = args[2:]
	}
	if len(args) > 0 {
		switch args[0] {
		case "clone", "fetch", "pull", "push", "ls-remote":
			keys, _ := ctx.Value(sshKeysContext{}).(map[string]string)
			if keys[remote] == "" {
				return nil, nil, fmt.Errorf("%s: %w", remote, ErrSSHKeyRequired)
			}
			env := make(map[string]string)
			cleanup, err := StartSSH(ctx, env, map[string]string{remote: keys[remote]}, "", "")
			return env, cleanup, err
		}
	}
	return nil, func() {}, nil
}
