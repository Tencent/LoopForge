package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	default:
		return conversationLoginSpec{}, false
	}
}

// Copy only the native CLI's login file into its isolated conversation home.
// This runs on the executing machine. The file never goes through the control
// connection or enters the synchronized asset set.
func prepareConversationRunnerLogin(root string, login conversationLoginSpec, enabled bool) (func() error, map[string]string, error) {
	return prepareRunnerLogin(AgentCLIHome{Base: root, Rel: "home"}, login, enabled)
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
	cleanup, env, err := prepareRunnerLogin(home, login, true)
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
	env[login.configEnv] = filepath.ToSlash(filepath.Join(homePath, login.dir))
	return env, cleanup, nil
}

func prepareRunnerLogin(home AgentCLIHome, login conversationLoginSpec, enabled bool) (func() error, map[string]string, error) {
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
	data, err := readRunnerLoginFile(configDir, login.file)
	if err != nil {
		return nil, nil, fmt.Errorf("执行机器上的 %s 本机登录态不可用，请检查 Runner 账户的 CLI 登录", login.label)
	}
	cleanup := func() error {
		dir, err := openAgentCLIHome(home)
		if err != nil {
			return err
		}
		defer dir.Close()
		// Codex and Claude Code rotate refresh tokens: a refresh inside the
		// sandbox invalidates the host's copy, so keep the refreshed login
		// before the copy is removed.
		if err := writeBackRefreshedLogin(dir, login, configDir, data); err != nil {
			log.Printf("runner login: keep refreshed %s login: %v", login.label, err)
		}
		return remove(dir)
	}
	if err := target.WriteFile(login.file, data, 0600); err != nil {
		return cleanup, nil, fmt.Errorf("无法准备 %s 的本机登录态", login.label)
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

// writeBackRefreshedLogin copies a login the CLI refreshed in its isolated home
// back to the Runner account's file. It is a compare-and-swap on content: the
// host file must still be the one copied in (otherwise the host CLI or another
// session refreshed it first and stays authoritative), and the refreshed file
// must be the same kind of login (same top-level fields and, for Codex, the
// same account) so a sandboxed agent cannot swap in another account.
func writeBackRefreshedLogin(dir *os.Root, login conversationLoginSpec, configDir string, original []byte) error {
	refreshed, err := readRootLoginFile(dir, login.file)
	if err != nil || bytes.Equal(refreshed, original) {
		return nil
	}
	if !sameRunnerLogin(original, refreshed) {
		return fmt.Errorf("refreshed login does not match the original account; not written back")
	}
	if configDir == "" {
		return nil
	}
	path := filepath.Join(configDir, login.file)
	current, err := readRunnerLoginFile(configDir, login.file)
	if err != nil || !bytes.Equal(current, original) {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(configDir, "."+login.file+".refresh-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
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
	if current, err := readRunnerLoginFile(configDir, login.file); err != nil || !bytes.Equal(current, original) {
		return nil
	}
	return os.Rename(tmp.Name(), path)
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

func sameRunnerLogin(original, refreshed []byte) bool {
	var before, after map[string]json.RawMessage
	if json.Unmarshal(original, &before) != nil || json.Unmarshal(refreshed, &after) != nil {
		return false
	}
	keys := func(m map[string]json.RawMessage) []string {
		out := make([]string, 0, len(m))
		for key := range m {
			out = append(out, key)
		}
		slices.Sort(out)
		return out
	}
	if !slices.Equal(keys(before), keys(after)) {
		return false
	}
	account := func(m map[string]json.RawMessage) string {
		var tokens struct {
			AccountID string `json:"account_id"`
		}
		_ = json.Unmarshal(m["tokens"], &tokens)
		return tokens.AccountID
	}
	return account(before) == account(after)
}
