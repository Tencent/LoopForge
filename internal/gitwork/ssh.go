package gitwork

import (
	"bytes"
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

func sshHostKeys(remotes []SSHRemote) ([]byte, error) {
	path := strings.TrimSpace(os.Getenv(SSHKnownHostsEnv))
	if path == "" {
		return nil, fmt.Errorf("Git SSH 尚未启用，请管理员配置 %s", SSHKnownHostsEnv)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("无法读取 %s 指定的主机密钥文件", SSHKnownHostsEnv)
	}
	allowed := make(map[string]bool, len(remotes))
	for _, remote := range remotes {
		allowed[knownhosts.Normalize(net.JoinHostPort(remote.Host, remote.Port))] = false
	}
	var pinned bytes.Buffer
	for _, line := range bytes.Split(content, []byte("\n")) {
		marker, hosts, key, _, _, err := ssh.ParseKnownHosts(line)
		if err != nil || (marker != "" && marker != "revoked" && marker != "cert-authority") {
			continue
		}
		for _, host := range hosts {
			host = strings.ToLower(host)
			if _, ok := allowed[host]; !ok {
				continue
			}
			if marker != "" {
				fmt.Fprintf(&pinned, "@%s ", marker)
			}
			fmt.Fprintf(&pinned, "%s %s", host, ssh.MarshalAuthorizedKey(key))
			if marker != "revoked" {
				allowed[host] = true
			}
		}
	}
	for _, remote := range remotes {
		if endpoint := knownhosts.Normalize(net.JoinHostPort(remote.Host, remote.Port)); !allowed[endpoint] {
			return nil, fmt.Errorf("SSH 主机 %s 未获信任，请管理员在 %s 中添加并核对该主机和端口的公钥", endpoint, SSHKnownHostsEnv)
		}
	}
	return pinned.Bytes(), nil
}

// sshBinding ties one repository remote to its host alias and private key.
type sshBinding struct {
	remote SSHRemote
	alias  string
	key    string
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
func StartSSH(environment map[string]string, keys map[string]string, root, clientRoot string) (func(), error) {
	if len(keys) == 0 {
		return func() {}, nil
	}
	bindings, remotes, err := bindSSHRemotes(environment, keys)
	if err != nil {
		return nil, err
	}
	environment["GIT_SSH_VARIANT"] = "ssh"
	if len(bindings) == 0 {
		// Keep terminals usable for editing without using a host identity.
		environment["GIT_SSH_COMMAND"] = `sh -c 'echo "未配置 Git SSH 密钥，请在个人配置中添加" >&2; exit 1' --`
		return func() {}, nil
	}
	pinned, err := sshHostKeys(remotes)
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
			item.alias, r.Port, r.Host, knownhosts.Normalize(r.Endpoint()), item.alias)
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
			cleanup, err := StartSSH(env, map[string]string{remote: keys[remote]}, "", "")
			return env, cleanup, err
		}
	}
	return nil, func() {}, nil
}
