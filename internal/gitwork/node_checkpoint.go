package gitwork

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// NodeCheckpoint describes a private Git snapshot. It does not change HEAD,
// the checked-out branch, the worktree or the repository's index.
type NodeCheckpoint struct {
	Ref       string `json:"ref"`
	Commit    string `json:"commit"`
	Head      string `json:"head"`
	Branch    string `json:"branch,omitempty"`
	FileCount int    `json:"fileCount"`
}

var checkpointIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// CreateNodeCheckpoint snapshots tracked files and non-ignored untracked
// files into a private ref. id must be a generated, unique 128-bit hex ID;
// existing refs are never overwritten. The caller must exclude all concurrent
// worktree and index writers for the entire operation.
func CreateNodeCheckpoint(ctx context.Context, workspace, id string) (NodeCheckpoint, error) {
	var checkpoint NodeCheckpoint
	if !checkpointIDPattern.MatchString(id) {
		return checkpoint, fmt.Errorf("invalid checkpoint ID")
	}
	head, ok := RevParse(ctx, workspace, "HEAD")
	if !ok {
		return checkpoint, fmt.Errorf("resolve checkpoint HEAD")
	}
	checkpoint.Head = head
	branchCmd := command(ctx, workspace, "", nil, []string{"symbolic-ref", "--quiet", "--short", "HEAD"})
	branchOutput, err := branchCmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return NodeCheckpoint{}, fmt.Errorf("read checkpoint branch: %w", err)
		}
	}
	checkpoint.Branch = strings.TrimSpace(string(branchOutput))

	indexPath, err := C(ctx, workspace, "rev-parse", "--git-path", "index")
	if err != nil {
		return NodeCheckpoint{}, fmt.Errorf("locate Git index: %w", err)
	}
	indexPath = strings.TrimSpace(indexPath)
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(workspace, indexPath)
	}
	indexPath, err = filepath.Abs(indexPath)
	if err != nil {
		return NodeCheckpoint{}, fmt.Errorf("resolve Git index: %w", err)
	}
	tempDir, err := os.MkdirTemp(filepath.Dir(indexPath), "loopforge-node-checkpoint-")
	if err != nil {
		return NodeCheckpoint{}, fmt.Errorf("create temporary index: %w", err)
	}
	defer os.RemoveAll(tempDir)
	tempIndex := filepath.Join(tempDir, "index")
	index, err := os.Open(indexPath)
	if err == nil {
		copyErr := copyCheckpointIndex(tempIndex, index)
		closeErr := index.Close()
		if copyErr != nil {
			return NodeCheckpoint{}, copyErr
		}
		if closeErr != nil {
			return NodeCheckpoint{}, fmt.Errorf("close Git index: %w", closeErr)
		}
	} else if !os.IsNotExist(err) {
		return NodeCheckpoint{}, fmt.Errorf("open Git index: %w", err)
	} else if _, err = checkpointGit(ctx, workspace, tempIndex, "read-tree", "HEAD"); err != nil {
		return NodeCheckpoint{}, fmt.Errorf("initialize checkpoint index: %w", err)
	}
	// Index flags such as skip-worktree and assume-unchanged must not hide
	// actual files from the snapshot. Clear them only in the temporary index.
	tracked, err := checkpointGit(ctx, workspace, tempIndex, "ls-files", "-z")
	if err != nil {
		return NodeCheckpoint{}, fmt.Errorf("list checkpoint files: %w", err)
	}
	if tracked != "" {
		for _, flag := range []string{"--no-skip-worktree", "--no-assume-unchanged"} {
			cmd := command(ctx, workspace, "", nil, []string{"update-index", flag, "-z", "--stdin"})
			cmd.Env = WithEnvValue(cmd.Env, "GIT_INDEX_FILE", tempIndex)
			cmd.Stdin = strings.NewReader(tracked)
			if output, err := cmd.CombinedOutput(); err != nil {
				return NodeCheckpoint{}, fmt.Errorf("prepare checkpoint index: %w: %s", err, strings.TrimSpace(string(output)))
			}
		}
	}
	if _, err = checkpointGit(ctx, workspace, tempIndex, "add", "-A", "--", "."); err != nil {
		return NodeCheckpoint{}, fmt.Errorf("stage checkpoint: %w", err)
	}
	tree, err := checkpointGit(ctx, workspace, tempIndex, "write-tree")
	if err != nil {
		return NodeCheckpoint{}, fmt.Errorf("write checkpoint tree: %w", err)
	}
	files, err := checkpointGit(ctx, workspace, tempIndex, "ls-files", "-z")
	if err != nil {
		return NodeCheckpoint{}, fmt.Errorf("count checkpoint files: %w", err)
	}
	checkpoint.FileCount = strings.Count(files, "\x00")
	commit, err := checkpointGit(ctx, workspace, tempIndex, "-c", "user.name=LoopForge", "-c", "user.email=loopforge@local", "commit-tree", strings.TrimSpace(tree), "-p", head, "-m", "LoopForge node checkpoint")
	if err != nil {
		return NodeCheckpoint{}, fmt.Errorf("create checkpoint commit: %w", err)
	}
	checkpoint.Commit = strings.TrimSpace(commit)
	checkpoint.Ref = "refs/loopforge/checkpoints/" + id
	if _, err = checkpointGit(ctx, workspace, tempIndex, "update-ref", checkpoint.Ref, checkpoint.Commit, strings.Repeat("0", len(checkpoint.Commit))); err != nil {
		return NodeCheckpoint{}, fmt.Errorf("create checkpoint ref: %w", err)
	}
	return checkpoint, nil
}

func copyCheckpointIndex(path string, source io.Reader) error {
	dest, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create checkpoint index: %w", err)
	}
	_, copyErr := io.Copy(dest, source)
	closeErr := dest.Close()
	if copyErr != nil {
		return fmt.Errorf("copy Git index: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close checkpoint index: %w", closeErr)
	}
	return nil
}

func checkpointGit(ctx context.Context, workspace, index string, args ...string) (string, error) {
	cmd := command(ctx, workspace, "", nil, args)
	if index != "" {
		cmd.Env = WithEnvValue(cmd.Env, "GIT_INDEX_FILE", index)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// DiffNodeCheckpoints compares two immutable snapshots in the same checkout.
// Only internal checkpoint refs are accepted, rather than arbitrary Git
// revision expressions or client-provided paths.
func DiffNodeCheckpoints(ctx context.Context, workspace, from, to string, limit int) (string, bool, error) {
	if !validNodeCheckpointRef(from) || !validNodeCheckpointRef(to) || limit <= 0 {
		return "", false, fmt.Errorf("invalid checkpoint diff request")
	}
	for _, ref := range []string{from, to} {
		if _, ok := RevParse(ctx, workspace, ref); !ok {
			return "", false, fmt.Errorf("checkpoint %s not found", ref)
		}
	}
	cmd := command(ctx, workspace, "", nil, []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", from, to, "--"})
	output := &checkpointLimitWriter{limit: limit}
	var stderr bytes.Buffer
	cmd.Stdout = output
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", false, fmt.Errorf("diff checkpoints: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return output.data.String(), output.truncated, nil
}

func NodeCheckpointChangedFiles(ctx context.Context, workspace, from, to string) ([]string, error) {
	if !validNodeCheckpointRef(from) || !validNodeCheckpointRef(to) {
		return nil, fmt.Errorf("invalid checkpoint refs")
	}
	for _, ref := range []string{from, to} {
		if _, ok := RevParse(ctx, workspace, ref); !ok {
			return nil, fmt.Errorf("checkpoint %s not found", ref)
		}
	}
	cmd := command(ctx, workspace, "", nil, []string{"diff", "--no-ext-diff", "--no-renames", "--name-only", "-z", from, to, "--"})
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list checkpoint changes: %w", err)
	}
	var files []string
	for _, path := range strings.Split(string(out), "\x00") {
		if path != "" {
			files = append(files, path)
		}
	}
	return files, nil
}

func validNodeCheckpointRef(ref string) bool {
	const prefix = "refs/loopforge/checkpoints/"
	return strings.HasPrefix(ref, prefix) && checkpointIDPattern.MatchString(strings.TrimPrefix(ref, prefix))
}

// RestoreNodeCheckpoint restores the recorded tree and HEAD in an exclusively
// owned checkout. Ignored files are left alone. A branch changed since the
// snapshot is rejected; moving it back must be an explicit caller decision.
// The caller must first save a recovery checkpoint and fence every writer.
func RestoreNodeCheckpoint(ctx context.Context, workspace string, checkpoint NodeCheckpoint) error {
	if err := ValidateNodeCheckpointRestore(ctx, workspace, checkpoint); err != nil {
		return err
	}
	// git clean without -x leaves ignored paths untouched. Git refuses to
	// recurse into nested repositories unless explicitly forced twice.
	if _, err := C(ctx, workspace, "clean", "-fd", "--", "."); err != nil {
		return fmt.Errorf("remove new files: %w", err)
	}
	if _, err := C(ctx, workspace, "reset", "--hard", checkpoint.Head); err != nil {
		return fmt.Errorf("restore checkpoint HEAD: %w", err)
	}
	// Restore the snapshot tree as both index and worktree, then put the index
	// back at HEAD so the restored changes remain visible as unstaged changes.
	if _, err := C(ctx, workspace, "read-tree", "--reset", "-u", checkpoint.Commit); err != nil {
		return fmt.Errorf("restore checkpoint tree: %w", err)
	}
	if _, err := C(ctx, workspace, "reset", "--mixed", checkpoint.Head); err != nil {
		return fmt.Errorf("reset checkpoint index: %w", err)
	}
	return nil
}

// ValidateNodeCheckpointRestore checks recoverability without modifying the
// checkout. Multi-repository callers can preflight every target first.
func ValidateNodeCheckpointRestore(ctx context.Context, workspace string, checkpoint NodeCheckpoint) error {
	if !validNodeCheckpointRef(checkpoint.Ref) {
		return fmt.Errorf("invalid checkpoint ref")
	}
	commit, ok := RevParse(ctx, workspace, checkpoint.Ref)
	if !ok || commit != checkpoint.Commit {
		return fmt.Errorf("checkpoint ref is missing or changed")
	}
	parent, ok := RevParse(ctx, workspace, checkpoint.Commit+"^")
	if !ok || parent != checkpoint.Head {
		return fmt.Errorf("checkpoint HEAD does not match its parent")
	}
	branchCmd := command(ctx, workspace, "", nil, []string{"symbolic-ref", "--quiet", "--short", "HEAD"})
	out, err := branchCmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return fmt.Errorf("read current branch: %w", err)
		}
	}
	if strings.TrimSpace(string(out)) != checkpoint.Branch {
		return fmt.Errorf("current branch differs from checkpoint")
	}
	if head, ok := RevParse(ctx, workspace, "HEAD"); !ok || head != checkpoint.Head {
		return fmt.Errorf("current HEAD differs from checkpoint")
	}
	// A reset during an unfinished Git operation can destroy its metadata or
	// leave it inconsistent with the restored files. Refuse instead.
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer"} {
		path, err := C(ctx, workspace, "rev-parse", "--git-path", name)
		if err != nil {
			return fmt.Errorf("locate Git operation state: %w", err)
		}
		path = strings.TrimSpace(path)
		if !filepath.IsAbs(path) {
			path = filepath.Join(workspace, path)
		}
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("unfinished Git operation: %s", name)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	// Git deliberately leaves nested repositories behind during clean -fd,
	// and resetting a submodule does not restore its contents. Reject these
	// cases instead of reporting a successful but incomplete restore.
	entries, err := checkpointGit(ctx, workspace, "", "ls-files", "--stage", "-z")
	if err != nil {
		return fmt.Errorf("list index entries: %w", err)
	}
	for _, entry := range strings.Split(entries, "\x00") {
		if strings.HasPrefix(entry, "160000 ") {
			return fmt.Errorf("checkpoint restore does not support submodules")
		}
	}
	for _, revision := range []string{checkpoint.Head, checkpoint.Commit} {
		tree, err := checkpointGit(ctx, workspace, "", "ls-tree", "-r", "-z", revision)
		if err != nil {
			return fmt.Errorf("list checkpoint tree: %w", err)
		}
		for _, entry := range strings.Split(tree, "\x00") {
			if strings.HasPrefix(entry, "160000 ") {
				return fmt.Errorf("checkpoint restore does not support submodules")
			}
		}
	}
	untracked, err := checkpointGit(ctx, workspace, "", "ls-files", "--others", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return fmt.Errorf("list untracked paths: %w", err)
	}
	for _, path := range strings.Split(untracked, "\x00") {
		if path == "" || !strings.HasSuffix(path, "/") {
			continue
		}
		root := filepath.Join(workspace, filepath.FromSlash(path))
		if err := filepath.WalkDir(root, func(entryPath string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Name() == ".git" {
				return fmt.Errorf("nested repository %q cannot be safely restored", entryPath)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if err := checkpointRestoreIgnoredConflicts(ctx, workspace, checkpoint); err != nil {
		return err
	}
	return nil
}

// Both reset and read-tree may overwrite an ignored file when it blocks a
// tracked path. Refuse before mutating anything: ignored files are explicitly
// outside the checkpoint and cannot be recovered from it.
func checkpointRestoreIgnoredConflicts(ctx context.Context, workspace string, checkpoint NodeCheckpoint) error {
	ignored, err := checkpointGit(ctx, workspace, "", "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil {
		return fmt.Errorf("list ignored files: %w", err)
	}
	if ignored == "" {
		return nil
	}
	paths := map[string]bool{}
	for _, rev := range []string{checkpoint.Head, checkpoint.Commit} {
		out, err := checkpointGit(ctx, workspace, "", "ls-tree", "-r", "-z", "--name-only", rev)
		if err != nil {
			return fmt.Errorf("list checkpoint paths: %w", err)
		}
		for _, path := range strings.Split(out, "\x00") {
			if path != "" {
				paths[path] = true
			}
		}
	}
	for _, ignoredPath := range strings.Split(ignored, "\x00") {
		if ignoredPath == "" {
			continue
		}
		for path := range paths {
			if path == ignoredPath || strings.HasPrefix(path, ignoredPath+"/") || strings.HasPrefix(ignoredPath, path+"/") {
				return fmt.Errorf("ignored path %q conflicts with checkpoint path %q", ignoredPath, path)
			}
		}
	}
	return nil
}

// checkpointLimitWriter continues consuming Git's output after the display
// limit; stopping the pipe early would incorrectly turn truncation into an
// execution error and can leave the child process blocked.
type checkpointLimitWriter struct {
	data      bytes.Buffer
	limit     int
	truncated bool
}

func (w *checkpointLimitWriter) Write(p []byte) (int, error) {
	n := len(p)
	remaining := w.limit - w.data.Len()
	if len(p) > remaining {
		w.truncated = true
		p = p[:remaining]
	}
	_, _ = w.data.Write(p)
	return n, nil
}
