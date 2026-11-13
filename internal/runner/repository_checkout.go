package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

func checkoutRepositoryForPreparation(ctx context.Context, workspace string, repository *domain.RunnerRepository, refresh bool) error {
	if err := checkRepositoryGitLab(ctx, *repository); err != nil {
		return fmt.Errorf("%s: %w", repository.Name, err)
	}
	if repository.RequireExistingWorkBranch && strings.TrimSpace(repository.WorkBranch) == "" {
		return fmt.Errorf("继续已有分支时必须指定工作分支")
	}
	if repository.CreateNewWorkBranch && repository.RequireExistingWorkBranch {
		return fmt.Errorf("工作分支不能既继续已有分支又新建")
	}
	info, err := os.Lstat(filepath.Join(workspace, ".git"))
	if os.IsNotExist(err) {
		return cloneRepository(ctx, workspace, repository)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("repository .git is not a real directory")
	}
	if _, ok := gitwork.RevParse(ctx, workspace, "HEAD"); !ok {
		// A killed clone leaves .git before there are refs or checked-out files.
		// Never remove an index, refs or source files from a damaged checkout.
		if err := removeIncompleteClone(ctx, workspace); err != nil {
			return err
		}
		return cloneRepository(ctx, workspace, repository)
	}
	if repository.RequireExistingWorkBranch {
		return checkoutExistingRepositoryBranch(ctx, workspace, repository)
	}
	if repository.CreateNewWorkBranch {
		// The name was validated when this checkout was created; reusing it only
		// needs the local branch, because the remote copy is the one we pushed.
		return checkoutCreatedRepositoryBranch(ctx, workspace, repository)
	}
	if refresh {
		return refreshCleanRepository(ctx, workspace, repository)
	}
	return nil
}

func removeIncompleteClone(ctx context.Context, workspace string) error {
	entries, err := os.ReadDir(workspace)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != ".git" {
			return fmt.Errorf("repository checkout is incomplete but contains files; preserving workspace %s", workspace)
		}
	}
	for _, name := range []string{"index", "agentflow-baseline"} {
		if _, err := os.Lstat(filepath.Join(workspace, ".git", name)); !os.IsNotExist(err) {
			return fmt.Errorf("repository checkout is incomplete but has %s; preserving workspace %s", name, workspace)
		}
	}
	refs, err := gitwork.C(ctx, workspace, "for-each-ref", "--format=%(objectname)")
	if err != nil || strings.TrimSpace(refs) != "" {
		return fmt.Errorf("repository checkout is incomplete but refs cannot be safely discarded; preserving workspace %s", workspace)
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.RemoveAll(".git")
}

// A clean working tree can still contain local commits or a stash. Only a
// checkout still at its original baseline, with no other files or commits,
// may be replaced. Ignored files count too so dependencies and local settings
// are retained rather than silently discarded.
func repositoryCanRefresh(ctx context.Context, workspace string, repository *domain.RunnerRepository) (bool, error) {
	status, err := gitwork.C(ctx, workspace, "status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching")
	if err != nil || status != "" {
		return false, err
	}
	head, ok := gitwork.RevParse(ctx, workspace, "HEAD")
	if !ok {
		return false, fmt.Errorf("resolve repository HEAD before refreshing")
	}
	// Keep marker access inside the checkout even if symlinks change between
	// checking the file and reading it.
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return false, err
	}
	defer root.Close()
	baseline := ""
	const path = ".git/agentflow-baseline"
	if info, err := root.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("repository baseline is not a regular file")
		}
		raw, err := root.ReadFile(path)
		if err != nil {
			return false, err
		}
		baseline = strings.TrimSpace(string(raw))
	} else if !os.IsNotExist(err) {
		return false, err
	} else if ref := strings.TrimSpace(repository.Ref); ref != "" {
		baseline, _ = gitwork.RevParse(ctx, workspace, "refs/remotes/origin/"+ref)
	}
	if baseline == "" || head != baseline {
		return false, nil
	}
	refs, err := gitwork.C(ctx, workspace, "for-each-ref", "--format=%(objectname)", "refs/stash")
	if err != nil || refs != "" {
		return false, err
	}
	commits, err := gitwork.C(ctx, workspace, "rev-list", "--max-count=1", "--branches", "--not", baseline)
	return err == nil && commits == "", err
}

func refreshCleanRepository(ctx context.Context, workspace string, repository *domain.RunnerRepository) error {
	clean, err := repositoryCanRefresh(ctx, workspace, repository)
	if err != nil || !clean {
		return err
	}
	replacement, err := os.MkdirTemp(filepath.Dir(workspace), ".agentflow-checkout-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(replacement)
	if err := cloneRepository(ctx, replacement, repository); err != nil {
		return err // The original checkout is still intact.
	}
	if clean, err = repositoryCanRefresh(ctx, workspace, repository); err != nil || !clean {
		return err
	}
	return replaceCheckoutContents(workspace, replacement)
}

// Keep the workspace directory inode: it may already be a Docker bind mount.
// Retain a recoverable old checkout until every replacement entry is in place.
func replaceCheckoutContents(workspace, replacement string) error {
	backup, err := os.MkdirTemp(filepath.Dir(workspace), ".agentflow-checkout-backup-")
	if err != nil {
		return err
	}
	move := func(from, to string) error {
		entries, err := os.ReadDir(from)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := os.Rename(filepath.Join(from, entry.Name()), filepath.Join(to, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if err := move(workspace, backup); err != nil {
		if rollback := move(backup, workspace); rollback != nil {
			return fmt.Errorf("checkout refresh failed; original files retained at %s: %w", backup, errors.Join(err, rollback))
		}
		_ = os.Remove(backup)
		return err
	}
	if err := move(replacement, workspace); err != nil {
		rollback := move(workspace, replacement)
		rollback = errors.Join(rollback, move(backup, workspace))
		if rollback != nil {
			return fmt.Errorf("checkout refresh failed; original files retained at %s: %w", backup, errors.Join(err, rollback))
		}
		_ = os.Remove(backup)
		return err
	}
	return os.RemoveAll(backup)
}
