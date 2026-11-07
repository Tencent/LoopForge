package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// ClaudeCodeVersion is installed from npm on first use. Published platform
// images do not redistribute Claude Code, so each installation downloads it
// from Anthropic's registry package itself.
const ClaudeCodeVersion = "2.1.300"

var claudeInstallMu sync.Mutex

// RuntimeCLIDir is the shared directory (AGENTFLOW_RUNTIME_CLI_DIR) holding
// on-demand CLI installations. It must resolve to the same absolute path for
// the platform and the host Docker daemon, because sandboxes bind-mount it.
func RuntimeCLIDir() string {
	return strings.TrimSpace(os.Getenv("AGENTFLOW_RUNTIME_CLI_DIR"))
}

var claudeCodePackageBinary = filepath.Join("node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe")

func claudeCodeBinary(root string) string {
	return filepath.Join(root, "claude-code", ClaudeCodeVersion, claudeCodePackageBinary)
}

// ClaudeCodeBinDir is the npm bin directory exposing the on-demand `claude`.
func ClaudeCodeBinDir() string {
	return filepath.Join(RuntimeCLIDir(), "claude-code", ClaudeCodeVersion, "node_modules", ".bin")
}

// ResolveClaudeCode returns a claude executable for the platform process: one
// already on PATH, otherwise the pinned on-demand installation.
func ResolveClaudeCode(ctx context.Context) (string, error) {
	if path, err := exec.LookPath("claude"); err == nil {
		return path, nil
	}
	return EnsureClaudeCode(ctx)
}

// EnsureClaudeCode installs the pinned Claude Code release into
// RuntimeCLIDir once and returns its absolute binary path.
func EnsureClaudeCode(ctx context.Context) (string, error) {
	root := RuntimeCLIDir()
	if root == "" {
		return "", errors.New("claude CLI 未安装")
	}
	binary := claudeCodeBinary(root)
	if fileIsExecutable(binary) {
		return binary, nil
	}
	claudeInstallMu.Lock()
	defer claudeInstallMu.Unlock()
	if fileIsExecutable(binary) {
		return binary, nil
	}
	parent := filepath.Join(root, "claude-code")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("prepare Claude Code directory: %w", err)
	}
	stage, err := os.MkdirTemp(parent, ".install-")
	if err != nil {
		return "", fmt.Errorf("prepare Claude Code install: %w", err)
	}
	defer os.RemoveAll(stage)
	// Sandboxes read the installation through a read-only bind mount.
	if err := os.Chmod(stage, 0o755); err != nil {
		return "", err
	}
	install := exec.CommandContext(ctx, "npm", "install", "--prefix", stage, "--no-audit", "--no-fund", "--omit=dev", "--save-exact", "@anthropic-ai/claude-code@"+ClaudeCodeVersion)
	install.Dir = stage
	if out, err := install.CombinedOutput(); err != nil {
		return "", fmt.Errorf("install Claude Code %s from npm: %w: %s", ClaudeCodeVersion, err, tailOutput(out))
	}
	staged := filepath.Join(stage, claudeCodePackageBinary)
	if out, err := exec.CommandContext(ctx, staged, "--version").CombinedOutput(); err != nil {
		return "", fmt.Errorf("installed Claude Code does not run on this platform: %w: %s", err, tailOutput(out))
	}
	target := filepath.Join(parent, ClaudeCodeVersion)
	if err := os.Rename(stage, target); err != nil && !fileIsExecutable(binary) {
		return "", fmt.Errorf("activate Claude Code install: %w", err)
	}
	return binary, nil
}

func fileIsExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}
