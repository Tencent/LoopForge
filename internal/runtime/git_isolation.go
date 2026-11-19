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
	"sync"
	"syscall"
	"time"

	"agentflow/internal/domain"
)

const legacyProjectAgentGitExcludeBlock = `# AgentFlow managed sandbox: repository Agent configuration
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

// RestoreLegacyWorkspaceAgentConfig clears the Git settings installed by the
// old instruction masks in private checkouts, including legacy root checkouts.
func RestoreLegacyWorkspaceAgentConfig(ctx context.Context, workspace string, repositories []domain.RepositoryContext) error {
	if err := RestoreLegacyProjectAgentConfig(ctx, workspace); err != nil {
		return err
	}
	for _, repo := range repositories {
		dir := strings.TrimSpace(repo.WorkspaceDir)
		if dir == "" || dir == "." {
			continue
		}
		if !filepath.IsLocal(dir) || filepath.Base(dir) != dir {
			continue
		}
		target := filepath.Join(workspace, dir)
		if info, err := os.Lstat(target); err == nil && info.IsDir() {
			if err := RestoreLegacyProjectAgentConfig(ctx, target); err != nil {
				return err
			}
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// RestoreLegacyProjectAgentConfig only updates clones tagged by the old masks.
// Keep their marker until the index is restored so an interrupted upgrade can
// retry without touching unmarked repositories or unrelated Git excludes.
func RestoreLegacyProjectAgentConfig(ctx context.Context, workspace string) error {
	git, err := os.Lstat(filepath.Join(workspace, ".git"))
	if os.IsNotExist(err) || err == nil && !git.IsDir() {
		return nil
	}
	if err != nil {
		return err
	}
	gitExcludeMu.Lock()
	defer gitExcludeMu.Unlock()

	target := filepath.Join(workspace, ".git", "info", "exclude")
	root, rel, err := openWorkspacePath(workspace, target)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Lstat(rel)
	if os.IsNotExist(err) || err == nil && !info.Mode().IsRegular() {
		return nil
	}
	if err != nil {
		return err
	}
	content, err := root.ReadFile(rel)
	if err != nil {
		return err
	}
	block := []byte(legacyProjectAgentGitExcludeBlock)
	if !bytes.Contains(content, block) {
		return nil
	}
	if err := clearLegacyProjectAgentGitIndex(ctx, workspace); err != nil {
		return err
	}
	return writeManagedFile(workspace, target, bytes.ReplaceAll(content, block, nil), info.Mode().Perm())
}

var gitExcludeMu sync.Mutex

// EnsureAgentFlowGitExcludes marks .agentflow/ ignored in the private clone so
// agent git stash -u does not remove runtime hook files mid-session.
func EnsureAgentFlowGitExcludes(workspace string) error {
	// Parallel branches prepare the same checkout; check-then-append must not
	// interleave or the block is appended twice.
	gitExcludeMu.Lock()
	defer gitExcludeMu.Unlock()
	root, excludePath, err := openWorkspacePath(workspace, filepath.Join(workspace, ".git", "info", "exclude"))
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := root.Stat(".git"); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	existing, err := root.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if bytes.Contains(existing, []byte(agentFlowGitExcludeBlock)) {
		return nil
	}
	if err := root.MkdirAll(filepath.Dir(excludePath), 0o700); err != nil {
		return err
	}
	file, err := root.OpenFile(excludePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		if _, err := file.WriteString("\n"); err != nil {
			return err
		}
	}
	if _, err := file.WriteString(agentFlowGitExcludeBlock); err != nil {
		return fmt.Errorf("write AgentFlow runtime excludes: %w", err)
	}
	return nil
}

func clearLegacyProjectAgentGitIndex(ctx context.Context, workspace string) error {
	const mode = "--no-skip-worktree"
	paths, err := trackedSkippedProjectAgentConfigPaths(ctx, workspace)
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
	if ctx.Err() != nil {
		return ctx.Err()
	}
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
	// A live holder is usually a short git command (a reader refreshing the
	// index, an agent's commit). Wait for it briefly instead of failing the
	// whole preparation on the first collision.
	for deadline := time.Now().Add(gitIndexLockWait); inspectErr == nil && confirmed && inUse && time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(gitIndexLockPoll):
		}
		if _, statErr := os.Stat(lockPath); os.IsNotExist(statErr) {
			if output, err = run(); err == nil {
				return nil
			}
			if !isGitIndexLockConflict(output) {
				return fmt.Errorf("git update-index %s: %w: %s", mode, err, strings.TrimSpace(string(output)))
			}
		}
		inUse, confirmed, inspectErr = gitIndexLockInUse(ctx, lockPath)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
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

// How long a preparation waits for another live git process to release
// .git/index.lock before giving up; variables so tests can shorten them.
var (
	gitIndexLockWait = 15 * time.Second
	gitIndexLockPoll = 200 * time.Millisecond
)

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
	return gitIndexLockHeldUnder("/proc", lockPath, uint32(os.Geteuid()))
}

// gitIndexLockHeldUnder matches open descriptors by file identity, not path:
// a sandbox may hold the same lock through a bind-mounted or hard-linked path.
//
// Git creates the lock with O_EXCL, so its holder runs as the lock's owner.
// When that owner is another user (e.g. root in a rootful container), this
// process may be unable to read the holder's descriptors; such an unreadable
// process leaves the result unconfirmed. Our own Git processes are always
// readable, and same-user processes hidden from us (non-dumpable helpers such
// as browser sandboxes) do not hold index locks, so they never block cleanup.
//
// A root-owned lock matches every unreadable root process on the host, so a
// non-root runner could never reclaim it. Git writes the lock while it holds
// it; once the lock has not been modified for gitIndexLockForeignStaleAfter,
// unreadable processes no longer keep it. A readable holder always does.
func gitIndexLockHeldUnder(procRoot, lockPath string, self uint32) (inUse, confirmed bool, err error) {
	want, err := filepath.Abs(lockPath)
	if err != nil {
		return false, false, err
	}
	lock, err := os.Lstat(want)
	if errors.Is(err, os.ErrNotExist) {
		return false, true, nil
	}
	if err != nil {
		return false, false, err
	}
	stat, ok := lock.Sys().(*syscall.Stat_t)
	if !lock.Mode().IsRegular() || !ok {
		return false, false, fmt.Errorf("index lock %s is not a regular file", want)
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return false, false, err
	}
	uncertain := false
	foreignOwner := stat.Uid != self
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		held, readErr := fdDirHoldsFile(filepath.Join(procRoot, entry.Name(), "fd"), want, lock)
		if readErr != nil {
			if foreignOwner && !errors.Is(readErr, os.ErrNotExist) && procMayOwnFile(filepath.Join(procRoot, entry.Name()), stat.Uid) {
				uncertain = true
			}
			continue
		}
		if held {
			return true, true, nil
		}
	}
	if uncertain && time.Since(lock.ModTime()) >= gitIndexLockForeignStaleAfter {
		uncertain = false
	}
	return false, !uncertain, nil
}

var gitIndexLockForeignStaleAfter = 30 * time.Minute

// fdDirHoldsFile reports whether any descriptor in one process's fd directory
// points at the lock file, either by path (want) or by inode (lock). The error
// is only the failure to read the fd directory itself.
func fdDirHoldsFile(dir, want string, lock os.FileInfo) (bool, error) {
	fds, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, fd := range fds {
		link := filepath.Join(dir, fd.Name())
		target, linkErr := os.Readlink(link)
		// Sockets, pipes and anonymous inodes are never the lock file.
		if linkErr != nil || !strings.HasPrefix(target, "/") {
			continue
		}
		if target == want {
			return true, nil
		}
		if open, statErr := os.Stat(link); statErr == nil && os.SameFile(lock, open) {
			return true, nil
		}
	}
	return false, nil
}

// procMayOwnFile reports whether the process may run as uid. Unknown state
// counts as a match, so an unreadable process can only preserve the lock.
func procMayOwnFile(procDir string, uid uint32) bool {
	status, err := os.ReadFile(filepath.Join(procDir, "status"))
	if errors.Is(err, os.ErrNotExist) {
		return false // the process has exited
	}
	if err != nil {
		return true
	}
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "Uid:" {
			continue
		}
		for _, field := range fields[1:] {
			if id, parseErr := strconv.ParseUint(field, 10, 32); parseErr != nil || uint32(id) == uid {
				return true
			}
		}
		return false
	}
	return true
}

func trackedSkippedProjectAgentConfigPaths(ctx context.Context, workspace string) ([]string, error) {
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
		if (entry[0] == 'S' || entry[0] == 's') && isLegacyProjectAgentConfigPath(path) {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

var legacyProjectAgentDirNames = map[string]bool{
	".agents": true, ".codex": true, ".claude": true, ".codebuddy": true,
}

var legacyProjectAgentFileNames = map[string]bool{
	"AGENTS.md": true, "AGENTS.override.md": true,
	"CLAUDE.md": true, "CLAUDE.local.md": true,
	"CODEBUDDY.md": true, ".mcp.json": true,
}

func isLegacyProjectAgentConfigPath(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return false
	}
	parts := strings.Split(clean, "/")
	for _, part := range parts[:len(parts)-1] {
		if legacyProjectAgentDirNames[part] {
			return true
		}
	}
	return legacyProjectAgentDirNames[parts[len(parts)-1]] || legacyProjectAgentFileNames[parts[len(parts)-1]]
}
