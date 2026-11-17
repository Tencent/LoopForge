package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type conversationLoginSpec struct {
	label, dir, file, configEnv string
	binding                     CredentialBinding
}

func runnerLoginSpec(provider string) (conversationLoginSpec, bool) {
	switch provider {
	case "codex":
		return conversationLoginSpec{"Codex", ".codex", "auth.json", "CODEX_HOME", BindCodex}, true
	case "claude-code":
		return conversationLoginSpec{"Claude Code", ".claude", ".credentials.json", "CLAUDE_CONFIG_DIR", BindClaudeCode}, true
	case AgentCLIAntigravity:
		return conversationLoginSpec{"Antigravity", ".gemini/antigravity-cli", "antigravity-oauth-token", "", BindAgentCLI}, true
	default:
		return conversationLoginSpec{}, false
	}
}

// Copy only the native CLI's login file into its isolated conversation home.
// This runs on the executing machine. The file never goes through the control
// connection or enters the synchronized asset set.
func prepareConversationRunnerLogin(root string, login conversationLoginSpec, enabled bool, docker ...bool) (func() error, map[string]string, error) {
	return prepareRunnerLogin(AgentCLIHome{Base: root, Rel: "home"}, login, enabled, docker...)
}

// PrepareRunnerSandboxLogin stages the selected CLI's login in the node's
// private home. The CLI can resume its session on a later attempt after the
// login file has been removed. Checkpoints exclude this control directory.
func PrepareRunnerSandboxLogin(sandbox Sandbox, provider, runID, nodeID string) (map[string]string, func() error, error) {
	login, supported := runnerLoginSpec(provider)
	if !supported {
		return nil, nil, nil
	}
	scope := sha256.Sum256([]byte(runID + "\x00" + nodeID))
	home := WorkspaceAgentCLIHome(sandbox.Workspace(), fmt.Sprintf("%s-login-%x", provider, scope[:12]))
	cleanup, env, err := prepareRunnerLogin(home, login, true, sandboxDriver(sandbox) == "docker")
	if err != nil {
		if cleanup != nil {
			err = errors.Join(err, cleanup())
		}
		return nil, nil, err
	}
	if env == nil {
		env = map[string]string{}
	}
	homePath := home.Path()
	if sandboxDriver(sandbox) == "docker" {
		homePath = filepath.ToSlash(filepath.Join("/workspace", home.Rel))
	}
	env["HOME"] = homePath
	if login.configEnv != "" {
		env[login.configEnv] = filepath.ToSlash(filepath.Join(homePath, login.dir))
	}
	return env, cleanup, nil
}

// Agent CLIs keep MCP and session configuration in the agent's existing HOME.
// Stage account authentication there rather than replacing that HOME.
func PrepareRunnerAgentCLILogin(sandbox Sandbox, command CommandSpec) (map[string]string, func() error, error) {
	login, supported := runnerLoginSpec(command.Provider)
	if !supported || !IsAgentCLI(command.Provider) {
		return nil, nil, fmt.Errorf("该 CLI 不支持使用执行机器的本机登录")
	}
	home := WorkspaceAgentCLIHome(sandbox.Workspace(), PrivateHomeName(command))
	cleanup, env, err := prepareRunnerLogin(home, login, true)
	if err != nil {
		if cleanup != nil {
			err = errors.Join(err, cleanup())
		}
		return nil, nil, err
	}
	_, homeEnv := privateHomePaths(sandbox, sandbox.Workspace(), command)
	env["HOME"] = homeEnv
	tmp, err := EnsureControlDirectory(sandbox.Workspace(), "tmp", PrivateHomeName(command))
	if err != nil {
		if cleanup != nil {
			err = errors.Join(err, cleanup())
		}
		return nil, nil, err
	}
	env["TMPDIR"] = tmp
	if sandboxDriver(sandbox) == "docker" {
		env["TMPDIR"] = filepath.ToSlash(filepath.Join("/workspace", ".agentflow", "tmp", PrivateHomeName(command)))
	}
	return env, cleanup, nil
}

func prepareRunnerLogin(home AgentCLIHome, login conversationLoginSpec, enabled bool, docker ...bool) (func() error, map[string]string, error) {
	home.Rel = filepath.Join(home.Rel, login.dir)
	target, err := openAgentCLIHome(home)
	if err != nil {
		return nil, nil, err
	}
	defer target.Close()
	remove := func(dir *os.Root) error {
		if err := dir.Remove(login.file); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	// A runner restart can interrupt cleanup. Remove any previous copy even
	// when this invocation now uses a personal key.
	if err := remove(target); err != nil {
		return nil, nil, err
	}
	if !enabled {
		return nil, nil, nil
	}
	if login.binding == BindClaudeCode {
		if token := strings.TrimSpace(os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")); token != "" {
			return nil, map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": token}, nil
		}
	}
	configDir := strings.TrimSpace(os.Getenv(login.configEnv))
	if configDir == "" {
		base, _ := os.UserHomeDir()
		if base != "" {
			configDir = filepath.Join(base, login.dir)
		}
	}
	var data []byte
	var keepAntigravity func([]byte) error
	if login.binding == BindAgentCLI {
		data, keepAntigravity, err = readAntigravityRunnerLogin()
	} else {
		data, err = readRunnerLoginFile(configDir, login.file)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("执行机器上的 %s 本机登录态不可用，请检查 Runner 账户的 CLI 登录", login.label)
	}
	if login.binding == BindCodex || login.binding == BindClaudeCode {
		source := fileRunnerLoginSource(login, configDir)
		if _, parseErr := parseRunnerOAuth(source.provider, data); parseErr == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			lease, leaseErr := newRunnerLoginLease(ctx, nativeRunnerLogins, source, len(docker) > 0 && docker[0])
			if leaseErr != nil {
				return nil, nil, fmt.Errorf("执行机器上的 %s 登录暂时无法续期：%w", login.label, leaseErr)
			}
			credential, credentialErr := lease.credential()
			if credentialErr == nil {
				credentialErr = target.WriteFile(login.file, credential, 0600)
			}
			if credentialErr != nil {
				lease.close()
				return nil, nil, credentialErr
			}
			return func() error {
				lease.close()
				dir, err := openAgentCLIHome(home)
				if err != nil {
					return err
				}
				defer dir.Close()
				// Only the trusted refresh manager can update the host login.
				return remove(dir)
			}, lease.environment(), nil
		}
		var incomplete struct {
			Codex struct {
				Refresh string `json:"refresh_token"`
			} `json:"tokens"`
			Claude struct {
				Refresh string `json:"refreshToken"`
			} `json:"claudeAiOauth"`
		}
		_ = json.Unmarshal(data, &incomplete)
		if incomplete.Codex.Refresh != "" || incomplete.Claude.Refresh != "" {
			return nil, nil, fmt.Errorf("执行机器上的 %s OAuth 凭证不完整，请重新登录 CLI", login.label)
		}
	}
	cleanup := func() error {
		dir, err := openAgentCLIHome(home)
		if err != nil {
			return err
		}
		defer dir.Close()
		// Only Antigravity's verified, stable-refresh-token flow accepts sandbox
		// updates. Other edited files are never promoted to host credentials.
		var keepErr error
		if keepAntigravity != nil {
			if refreshed, err := readRootLoginFile(dir, login.file); err == nil {
				keepErr = keepAntigravity(refreshed)
			}
		} else {
			keepErr = writeBackRefreshedLogin(dir, login, configDir, data)
		}
		if keepErr != nil {
			log.Printf("runner login: keep refreshed %s login: %v", login.label, keepErr)
		}
		return remove(dir)
	}
	if err := target.WriteFile(login.file, data, 0600); err != nil {
		return cleanup, nil, fmt.Errorf("无法准备 %s 的本机登录态", login.label)
	}
	if login.binding == BindAgentCLI {
		// The official CLI selects file storage in remote sessions. This also
		// prevents a sandbox from trying to open the host's OS credential store.
		env := map[string]string{"SSH_CONNECTION": "127.0.0.1 0 127.0.0.1 0"}
		for _, key := range agentCLIHostEnv {
			env[key] = ""
		}
		// A prior API-key turn may have selected the direct Gemini API.
		if err := target.WriteFile("settings.json", []byte("{}"), 0600); err != nil {
			return cleanup, nil, fmt.Errorf("无法准备 Antigravity 的账号登录配置")
		}
		return cleanup, env, nil
	}
	return cleanup, nil, nil
}

func readRunnerLoginFile(dir, name string) ([]byte, error) {
	if dir == "" {
		return nil, os.ErrNotExist
	}
	const maxSize = 1 << 20
	path := filepath.Join(dir, name)
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxSize {
		return nil, fmt.Errorf("invalid login file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSize || !json.Valid(data) {
		return nil, fmt.Errorf("invalid login file")
	}
	return data, nil
}

// Unmanaged/static login copies cannot replace a host credential. OAuth pairs
// are refreshed by the trusted manager; sandbox JSON is never an issuer response.
func writeBackRefreshedLogin(dir *os.Root, login conversationLoginSpec, configDir string, original []byte) error {
	refreshed, err := readRootLoginFile(dir, login.file)
	if err != nil || bytes.Equal(refreshed, original) {
		return nil
	}
	return fmt.Errorf("sandbox login changes cannot replace Runner credentials")
}

func replaceRunnerLoginFile(configDir, name string, original, refreshed []byte) error {
	if configDir == "" {
		return nil
	}
	path := filepath.Join(configDir, name)
	current, err := readRunnerLoginFile(configDir, name)
	if err != nil || !bytes.Equal(current, original) {
		return nil
	}
	tmp, err := os.CreateTemp(configDir, "."+name+".refresh-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(refreshed); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Re-check right before the swap to keep the window with a concurrent
	// host refresh as small as possible.
	if current, err := readRunnerLoginFile(configDir, name); err != nil || !bytes.Equal(current, original) {
		return nil
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(configDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// readRootLoginFile reads the login file below the isolated home through
// os.Root, so a symlink planted by the sandbox cannot point elsewhere.
func readRootLoginFile(dir *os.Root, name string) ([]byte, error) {
	const maxSize = 1 << 20
	info, err := dir.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSize {
		return nil, fmt.Errorf("invalid login file")
	}
	file, err := dir.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSize || !json.Valid(data) {
		return nil, fmt.Errorf("invalid login file")
	}
	return data, nil
}
