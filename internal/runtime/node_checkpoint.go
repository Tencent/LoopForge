package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

// NodeCheckpointRepositoryRoot rejects escaping paths and symlinked checkout
// roots. The caller must also fence workspace writers while using the result.
func NodeCheckpointRepositoryRoot(root, directory string) (string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("workspace root is not a real directory")
	}
	directory = filepath.FromSlash(strings.TrimSpace(directory))
	if directory == "" || directory == "." {
		return root, nil
	}
	if !filepath.IsLocal(directory) {
		return "", fmt.Errorf("invalid repository directory")
	}
	current := root
	for _, part := range strings.Split(filepath.Clean(directory), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("repository directory is not a real directory")
		}
	}
	return current, nil
}

// CreateNodeWorkspaceCheckpoint snapshots all run repositories. On error,
// partially-created refs are harmless and remain scoped to this workspace.
func CreateNodeWorkspaceCheckpoint(ctx context.Context, root string, repositories []domain.RepositoryContext, attempt int) (domain.NodeCheckpoint, error) {
	var checkpointID [16]byte
	if _, err := rand.Read(checkpointID[:]); err != nil {
		return domain.NodeCheckpoint{}, err
	}
	result := domain.NodeCheckpoint{ID: hex.EncodeToString(checkpointID[:]), Attempt: attempt, CreatedAt: time.Now().UTC()}
	if len(repositories) == 0 {
		repositories = []domain.RepositoryContext{{}}
	}
	for _, repository := range repositories {
		dir, err := NodeCheckpointRepositoryRoot(root, repository.WorkspaceDir)
		if err != nil {
			return result, fmt.Errorf("repository %s: %w", repository.RepositoryID, err)
		}
		info, err := os.Lstat(filepath.Join(dir, ".git"))
		if os.IsNotExist(err) && repository.RepositoryID == "" {
			continue
		} else if err != nil {
			return result, fmt.Errorf("repository %s: %w", repository.RepositoryID, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return result, fmt.Errorf("repository %s: external Git metadata is not supported", repository.RepositoryID)
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return result, err
		}
		cp, err := gitwork.CreateNodeCheckpoint(ctx, dir, hex.EncodeToString(id[:]))
		if err != nil {
			return result, fmt.Errorf("repository %s: %w", repository.RepositoryID, err)
		}
		result.Repositories = append(result.Repositories, domain.NodeRepositoryCheckpoint{
			RepositoryID: repository.RepositoryID, WorkspaceDir: repository.WorkspaceDir,
			Ref: cp.Ref, Commit: cp.Commit, Head: cp.Head, Branch: cp.Branch, FileCount: cp.FileCount,
		})
	}
	return result, nil
}

// DiffNodeWorkspaceCurrent captures an approximate point-in-time view while a
// node is still running. Concurrent writes can make Git reject the capture;
// callers should allow retry. Temporary refs are removed after comparison.
func DiffNodeWorkspaceCurrent(ctx context.Context, root string, start domain.NodeCheckpoint, limit int) (string, bool, []string, error) {
	repositories := make([]domain.RepositoryContext, 0, len(start.Repositories))
	for _, item := range start.Repositories {
		repositories = append(repositories, domain.RepositoryContext{RepositoryID: item.RepositoryID, WorkspaceDir: item.WorkspaceDir})
	}
	end, err := CreateNodeWorkspaceCheckpoint(ctx, root, repositories, start.Attempt)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, item := range end.Repositories {
			if dir, e := NodeCheckpointRepositoryRoot(root, item.WorkspaceDir); e == nil {
				_, _ = gitwork.C(cleanup, dir, "update-ref", "-d", item.Ref, item.Commit)
			}
		}
	}()
	if err != nil {
		return "", false, nil, err
	}
	files, err := NodeWorkspaceCheckpointChangedFiles(ctx, root, start, end)
	if err != nil {
		return "", false, nil, err
	}
	diff, truncated, err := DiffNodeWorkspaceCheckpoints(ctx, root, start, end, limit)
	return diff, truncated, files, err
}

// RestoreNodeWorkspaceCheckpointWithRecovery attempts to undo a partial
// multi-repository restore if an I/O error occurs after preflight.
func RestoreNodeWorkspaceCheckpointWithRecovery(ctx context.Context, root string, target, recovery domain.NodeCheckpoint) error {
	if len(target.Repositories) != len(recovery.Repositories) {
		return fmt.Errorf("checkpoint repositories differ")
	}
	for i, before := range target.Repositories {
		after := recovery.Repositories[i]
		if before.RepositoryID != after.RepositoryID || before.WorkspaceDir != after.WorkspaceDir {
			return fmt.Errorf("checkpoint repositories differ")
		}
	}
	if err := ValidateNodeWorkspaceCheckpoint(ctx, root, target); err != nil {
		return err
	}
	if err := RestoreNodeWorkspaceCheckpoint(ctx, root, target); err != nil {
		// Cancellation may mean the caller lost the workspace lease. Never
		// continue writing with an unfenced context in that case; the durable
		// recovery checkpoint remains available for an operator.
		if ctx.Err() != nil {
			return fmt.Errorf("restore interrupted; recovery checkpoint retained: %w", err)
		}
		if rollbackErr := RestoreNodeWorkspaceCheckpoint(ctx, root, recovery); rollbackErr != nil {
			return fmt.Errorf("restore failed: %v; recovery failed: %w", err, rollbackErr)
		}
		return fmt.Errorf("restore failed; recovered previous state: %w", err)
	}
	return nil
}

func nodeGitCheckpoint(cp domain.NodeRepositoryCheckpoint) gitwork.NodeCheckpoint {
	return gitwork.NodeCheckpoint{Ref: cp.Ref, Commit: cp.Commit, Head: cp.Head, Branch: cp.Branch, FileCount: cp.FileCount}
}

// RestoreNodeWorkspaceCheckpoint preflights all repositories before mutating
// any. Callers must capture a recovery snapshot and prevent concurrent writes.
func RestoreNodeWorkspaceCheckpoint(ctx context.Context, root string, checkpoint domain.NodeCheckpoint) error {
	if err := ValidateNodeWorkspaceCheckpoint(ctx, root, checkpoint); err != nil {
		return err
	}
	for _, repository := range checkpoint.Repositories {
		dir, err := NodeCheckpointRepositoryRoot(root, repository.WorkspaceDir)
		if err != nil {
			return err
		}
		if err := gitwork.RestoreNodeCheckpoint(ctx, dir, nodeGitCheckpoint(repository)); err != nil {
			return fmt.Errorf("repository %s: %w", repository.RepositoryID, err)
		}
	}
	return nil
}

func ValidateNodeWorkspaceCheckpoint(ctx context.Context, root string, checkpoint domain.NodeCheckpoint) error {
	for _, repository := range checkpoint.Repositories {
		dir, err := NodeCheckpointRepositoryRoot(root, repository.WorkspaceDir)
		if err != nil {
			return err
		}
		if err := gitwork.ValidateNodeCheckpointRestore(ctx, dir, nodeGitCheckpoint(repository)); err != nil {
			return fmt.Errorf("repository %s: %w", repository.RepositoryID, err)
		}
	}
	return nil
}

func DiffNodeWorkspaceCheckpoints(ctx context.Context, root string, start, end domain.NodeCheckpoint, limit int) (string, bool, error) {
	if len(start.Repositories) != len(end.Repositories) || limit <= 0 {
		return "", false, fmt.Errorf("checkpoint repositories differ")
	}
	var result strings.Builder
	for i, before := range start.Repositories {
		after := end.Repositories[i]
		if before.RepositoryID != after.RepositoryID || before.WorkspaceDir != after.WorkspaceDir {
			return "", false, fmt.Errorf("checkpoint repositories differ")
		}
		dir, err := NodeCheckpointRepositoryRoot(root, before.WorkspaceDir)
		if err != nil {
			return "", false, err
		}
		remaining := limit - result.Len()
		if remaining <= 0 {
			return result.String(), true, nil
		}
		diff, truncated, err := gitwork.DiffNodeCheckpoints(ctx, dir, before.Ref, after.Ref, remaining)
		if err != nil {
			return "", false, fmt.Errorf("repository %s: %w", before.RepositoryID, err)
		}
		result.WriteString(diff)
		if truncated {
			return result.String(), true, nil
		}
	}
	return result.String(), false, nil
}

func NodeWorkspaceCheckpointChangedFiles(ctx context.Context, root string, start, end domain.NodeCheckpoint) ([]string, error) {
	if len(start.Repositories) != len(end.Repositories) {
		return nil, fmt.Errorf("checkpoint repositories differ")
	}
	var files []string
	for i, before := range start.Repositories {
		after := end.Repositories[i]
		if before.RepositoryID != after.RepositoryID || before.WorkspaceDir != after.WorkspaceDir {
			return nil, fmt.Errorf("checkpoint repositories differ")
		}
		dir, err := NodeCheckpointRepositoryRoot(root, before.WorkspaceDir)
		if err != nil {
			return nil, err
		}
		changed, err := gitwork.NodeCheckpointChangedFiles(ctx, dir, before.Ref, after.Ref)
		if err != nil {
			return nil, err
		}
		for _, path := range changed {
			files = append(files, filepath.ToSlash(filepath.Join(before.WorkspaceDir, path)))
		}
	}
	return files, nil
}
