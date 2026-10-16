package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"
	"strconv"
	"strings"
)

const projectAgentGitExcludeBlock = `# AgentFlow managed sandbox: repository Agent configuration
AGENTS.md
AGENTS.override.md
CLAUDE.md
CLAUDE.local.md
CODEBUDDY.md
.mcp.json
.agents/
.codex/
.claude/
.codebuddy/
`

const agentFlowGitExcludeBlock = `# AgentFlow managed runtime (hooks, bindings, invocation state)
.agentflow/
`

// UsesProjectAgentConfigIsolation reports whether a sandbox hides repository
// Agent CLI configuration from commands running inside it. Local execution
// deliberately keeps its historical, unisolated Git behavior.
func UsesProjectAgentConfigIsolation(driver string) bool {
	switch strings.TrimSpace(driver) {
	case "seatbelt", "bwrap", "docker":
		return true
	default:
		return false
	}
}

// RestoreProjectAgentGitIndex clears the temporary skip-worktree bits before
// AgentFlow refreshes or checks out the run branch. The workspace is a private
// clone, so these index bits can only have been set by AgentFlow.
func RestoreProjectAgentGitIndex(ctx context.Context, workspace, driver string) error {
	return updateProjectAgentGitIndex(ctx, workspace, driver, "--no-skip-worktree")
}

// ProtectProjectAgentGitIndex prevents sandbox masks and read denials from
// looking like repository changes to Git. Tracked paths use skip-worktree;
// untracked paths use the private clone's .git/info/exclude. Neither mechanism
// changes the source repository or its committed .gitignore.
func ProtectProjectAgentGitIndex(ctx context.Context, workspace, driver string) error {
	if !UsesProjectAgentConfigIsolation(driver) {
		return nil
	}
	if err := ensureProjectAgentGitExcludes(workspace); err != nil {
		return err
	}
	return updateProjectAgentGitIndex(ctx, workspace, driver, "--skip-worktree")
}

func ensureProjectAgentGitExcludes(workspace string) error {
	return appendGitExcludeBlock(workspace, projectAgentGitExcludeBlock, "project Agent Git excludes")
}

// EnsureAgentFlowGitExcludes marks .agentflow/ ignored in the private clone so
// agent git stash -u does not remove runtime hook files mid-session.
func EnsureAgentFlowGitExcludes(workspace string) error {
	return appendGitExcludeBlock(workspace, agentFlowGitExcludeBlock, "AgentFlow runtime excludes")
}

func appendGitExcludeBlock(workspace, block, label string) error {
	gitDir := filepath.Join(workspace, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	excludePath := filepath.Join(gitDir, "info", "exclude")
	existing, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if bytes.Contains(existing, []byte(block)) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(excludePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		if _, err := file.WriteString("\n"); err != nil {
			return err
		}
	}
	if _, err := file.WriteString(block); err != nil {
		return fmt.Errorf("write %s: %w", label, err)
	}
	return nil
}

func updateProjectAgentGitIndex(ctx context.Context, workspace, driver, mode string) error {
	if !UsesProjectAgentConfigIsolation(driver) {
		return nil
	}
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	paths, err := trackedProjectAgentConfigPathsForMode(ctx, workspace, mode)
	if err != nil || len(paths) == 0 {
		return err
	}
	var input bytes.Buffer
	for _, path := range paths {
		input.WriteString(path)
		input.WriteByte(0)
	}
	run := func() ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", "-C", workspace, "update-index", mode, "-z", "--stdin")
		cmd.Stdin = bytes.NewReader(input.Bytes())
		return cmd.CombinedOutput()
	}
	output, err := run()
	if err == nil {
		return nil
	}
	if !isGitIndexLockConflict(output) {
		return fmt.Errorf("git update-index %s: %w: %s", mode, err, strings.TrimSpace(string(output)))
	}
	lockPath, lockErr := gitIndexLockPath(ctx, workspace)
	if lockErr != nil {
		return fmt.Errorf("git update-index %s: %w: %s (resolve index lock: %v)", mode, err, strings.TrimSpace(string(output)), lockErr)
	}
	inUse, confirmed, inspectErr := gitIndexLockInUse(ctx, lockPath)
	if inspectErr != nil || !confirmed {
		return fmt.Errorf("git update-index %s: %w: %s (cannot safely inspect index lock: %v)", mode, err, strings.TrimSpace(string(output)), inspectErr)
	}
	if inUse {
		return fmt.Errorf("git update-index %s: %w: %s (index lock is held by a live process)", mode, err, strings.TrimSpace(string(output)))
	}
	if removeErr := os.Remove(lockPath); removeErr != nil && !os.IsNotExist(removeErr) {
		return fmt.Errorf("git update-index %s: %w: %s (remove stale index lock: %v)", mode, err, strings.TrimSpace(string(output)), removeErr)
	}
	output, err = run()
	if err != nil {
		return fmt.Errorf("git update-index %s after stale index lock cleanup: %w: %s", mode, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func isGitIndexLockConflict(output []byte) bool {
	message := strings.ToLower(string(output))
	return strings.Contains(message, "index.lock") && strings.Contains(message, "file exists")
}

func gitIndexLockPath(ctx context.Context, workspace string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", workspace, "rev-parse", "--git-path", "index.lock")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(output))
	if path == "" {
		return "", fmt.Errorf("git returned an empty index lock path")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	return filepath.Clean(path), nil
}

// gitIndexLockInUse reports whether a live process still has the lock file
// open. The second result is false when the host cannot prove either state;
// callers must keep the lock in that case.
func gitIndexLockInUse(ctx context.Context, lockPath string) (inUse, confirmed bool, err error) {
	if stdruntime.GOOS == "linux" {
		return gitIndexLockInUseProc(lockPath)
	}
	lsof, lookupErr := exec.LookPath("lsof")
	if lookupErr != nil {
		return false, false, lookupErr
	}
	cmd := exec.CommandContext(ctx, lsof, "-t", "--", lockPath)
	output, runErr := cmd.CombinedOutput()
	if len(bytes.TrimSpace(output)) > 0 {
		return true, true, nil
	}
	if runErr == nil {
		return false, true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1 {
		return false, true, nil
	}
	return false, false, runErr
}

func gitIndexLockInUseProc(lockPath string) (inUse, confirmed bool, err error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, false, err
	}
	want, err := filepath.Abs(lockPath)
	if err != nil {
		return false, false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		fds, readErr := os.ReadDir(filepath.Join("/proc", entry.Name(), "fd"))
		if readErr != nil {
			continue
		}
		for _, fd := range fds {
			target, linkErr := os.Readlink(filepath.Join("/proc", entry.Name(), "fd", fd.Name()))
			if linkErr != nil {
				continue
			}
			if target == want {
				return true, true, nil
			}
		}
	}
	return false, true, nil
}

func trackedProjectAgentConfigPathsForMode(ctx context.Context, workspace, mode string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", workspace, "ls-files", "-v", "-z")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list tracked project Agent configuration: %w", err)
	}
	entries := bytes.Split(output, []byte{0})
	paths := make([]string, 0)
	for _, entry := range entries {
		if len(entry) == 0 {
			continue
		}
		if len(entry) < 3 || entry[1] != ' ' {
			continue
		}
		path := string(entry[2:])
		isProtected := entry[0] == 'S'
		needsUpdate := mode == "--skip-worktree" && !isProtected || mode == "--no-skip-worktree" && isProtected
		if needsUpdate && isProjectAgentConfigPath(path) {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func isProjectAgentConfigPath(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return false
	}
	parts := strings.Split(clean, "/")
	for _, part := range parts[:len(parts)-1] {
		if projectAgentDirNames[part] {
			return true
		}
	}
	return projectAgentDirNames[parts[len(parts)-1]] || projectAgentFileNames[parts[len(parts)-1]]
}
