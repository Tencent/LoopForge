package runtime

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	case AgentCLICursor:
		return conversationLoginSpec{"Cursor CLI", ".cursor", "auth.json", "CURSOR_CONFIG_DIR", BindAgentCLI}, true
	default:
		return conversationLoginSpec{}, false
	}
}

// Copy only the native CLI's login file into its isolated conversation home.
// This runs on the executing machine. The file never goes through the control
// connection or enters the synchronized asset set.
func prepareConversationRunnerLogin(root string, login conversationLoginSpec, enabled bool) (func() error, map[string]string, error) {
	home := AgentCLIHome{Base: root, Rel: filepath.Join("home", login.dir)}
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
	// Cursor's file store also uses ~/.cursor with a custom CLI config
	// directory. Prefer the configured directory when it contains a login.
	if os.IsNotExist(err) && login.binding == BindAgentCLI && strings.TrimSpace(os.Getenv(login.configEnv)) != "" {
		if base, homeErr := os.UserHomeDir(); homeErr == nil && base != "" {
			data, err = readRunnerLoginFile(filepath.Join(base, login.dir), login.file)
		}
	}
	if err != nil {
		if login.binding == BindAgentCLI {
			return nil, nil, CursorRunnerLoginError()
		}
		return nil, nil, fmt.Errorf("执行机器上的 %s 本机登录态不可用，请检查 Runner 账户的 CLI 登录", login.label)
	}
	cleanup := func() error {
		dir, err := openAgentCLIHome(home)
		if err != nil {
			return err
		}
		defer dir.Close()
		return remove(dir)
	}
	if err := target.WriteFile(login.file, data, 0600); err != nil {
		return cleanup, nil, fmt.Errorf("无法准备 %s 的本机登录态", login.label)
	}
	return cleanup, nil, nil
}

// CursorRunnerLoginError points to Cursor's supported file store for
// sandboxes that cannot access the system keychain.
func CursorRunnerLoginError() error {
	return fmt.Errorf("执行机器上的 Cursor CLI 本机登录态不可用，请在运行 Runner 的系统账号下执行 AGENT_CLI_CREDENTIAL_STORE=file agent login")
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
