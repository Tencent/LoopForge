package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/procwatch"
	"agentflow/internal/workspaceprep"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// normalizeRepositories drops empty / duplicate entries while keeping order.
func normalizeRepositories(repositories []domain.RepositoryContext) []domain.RepositoryContext {
	seen := map[string]bool{}
	out := make([]domain.RepositoryContext, 0, len(repositories))
	for _, rc := range repositories {
		if strings.TrimSpace(rc.RepositoryID) == "" || seen[rc.RepositoryID] {
			continue
		}
		seen[rc.RepositoryID] = true
		out = append(out, rc)
	}
	return out
}

// repositoryWorkspacePath returns the checkout directory of rc inside the run
// workspace. New runs always assign WorkspaceDir; empty WorkspaceDir is the
// persisted legacy layout that checked out at the workspace root.
func repositoryWorkspacePath(workspace string, rc domain.RepositoryContext) string {
	if dir := strings.TrimSpace(rc.WorkspaceDir); dir != "" {
		return filepath.Join(workspace, filepath.FromSlash(dir))
	}
	return workspace
}

// repositoryWorkspaceDirs lists every git checkout directory of a run: one
// per bound repository, or the workspace root when no repository is bound.
func repositoryWorkspaceDirs(workspace string, repositories []domain.RepositoryContext) []string {
	if len(repositories) == 0 {
		return []string{workspace}
	}
	dirs := make([]string, 0, len(repositories))
	for _, rc := range repositories {
		dirs = append(dirs, repositoryWorkspacePath(workspace, rc))
	}
	return dirs
}

// prepareRepositories checks out every run repository into its WorkspaceDir
// (a first-level sub-directory of the run workspace).
func prepareRepositories(ctx context.Context, workspace string, repositories []domain.RepositoryContext) error {
	if len(repositories) == 0 {
		return nil
	}
	for _, rc := range repositories {
		if err := prepareRepository(ctx, repositoryWorkspacePath(workspace, rc), rc); err != nil {
			return fmt.Errorf("repository %s: %w", rc.Name, err)
		}
	}
	return nil
}

func repositoryBaseBranch(repository domain.RepositoryContext) string {
	if branch := strings.TrimSpace(repository.BaseBranch); branch != "" {
		return branch
	}
	if branch := strings.TrimSpace(repository.DefaultBranch); branch != "" {
		return branch
	}
	return "master"
}

func existingDevelopmentBranch(repository domain.RepositoryContext) bool {
	branch := strings.TrimSpace(repository.WorkBranch)
	if branch == "" || repository.AlignOnly {
		return false
	}
	for _, shared := range []string{repository.BaseBranch, repository.DefaultBranch, repository.IntegrateBranch, "master", "main"} {
		if branch == strings.TrimSpace(shared) {
			return false
		}
	}
	return true
}

func prepareRepository(ctx context.Context, workspace string, repository domain.RepositoryContext) error {
	if repository.RepositoryID == "" {
		return nil
	}
	ctx = withRepositoryGit(ctx, repository)
	if repository.RequireExistingWorkBranch && !existingDevelopmentBranch(repository) {
		return fmt.Errorf("continuing an existing branch requires a development branch distinct from the main and integration branches")
	}
	if repository.CreateNewWorkBranch {
		if repository.RequireExistingWorkBranch {
			return fmt.Errorf("a work branch cannot both continue an existing branch and be newly created")
		}
		if !existingDevelopmentBranch(repository) {
			return fmt.Errorf("creating a work branch requires a development branch name distinct from the main and integration branches")
		}
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return fmt.Errorf("create repository directory: %w", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err == nil {
		if _, valid := gitRevParse(ctx, workspace, "HEAD"); valid {
			return workspaceprep.Do(ctx, "branch", "正在准备分支："+repository.Name, func(ctx context.Context) error { return resumeRepository(ctx, workspace, repository) })
		}
		if err := gitwork.RemoveIncompleteClone(ctx, workspace); err != nil {
			return err
		}
	}
	localPath := strings.TrimSpace(repository.LocalPath)
	cloneURL := strings.TrimSpace(repository.CloneURL)
	localIsDir := false
	localIsGit := false
	if localPath != "" {
		info, err := os.Stat(localPath)
		if err != nil || !info.IsDir() {
			if cloneURL == "" {
				return fmt.Errorf("localPath is not a readable directory: %s", localPath)
			}
			// localPath is only a clone cache (--reference). A new host often
			// keeps the Studio path from another machine; fall back to cloneUrl.
			localPath = ""
		} else {
			localIsDir = true
			if _, err := os.Stat(filepath.Join(localPath, ".git")); err == nil {
				localIsGit = true
			}
		}
	}
	if localIsDir && !localIsGit && cloneURL == "" {
		if repository.RequireExistingWorkBranch {
			return fmt.Errorf("cannot continue branch %q: local source is not a Git repository", repository.WorkBranch)
		}
		base := strings.TrimSpace(repository.BaseBranch)
		defaultBranch := strings.TrimSpace(repository.DefaultBranch)
		legacyDefault := defaultBranch == "" && (base == "master" || base == "main")
		if base != "" && base != defaultBranch && !legacyDefault {
			return fmt.Errorf("cannot check out base branch %q: local source is not a Git repository", base)
		}
		cmd := exec.CommandContext(ctx, "cp", "-a", filepath.Clean(localPath)+"/.", workspace)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("copy local source: %w: %s", err, strings.TrimSpace(string(output)))
		}
		for _, args := range [][]string{{"init", "-b", "agentflow-base"}, {"config", "user.email", "loopforge@local"}, {"config", "user.name", "LoopForge"}, {"add", "-A"}, {"commit", "-m", "LoopForge baseline", "--quiet"}} {
			cmd = exec.CommandContext(ctx, "git", append([]string{"-C", workspace}, args...)...)
			if output, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("snapshot local source: %w: %s", err, strings.TrimSpace(string(output)))
			}
		}
		if err := applyWorkspaceGitIdentity(ctx, workspace); err != nil {
			return err
		}
		return recordRepositoryBaseline(workspace, "")
	}
	remote := cloneURL
	if remote == "" {
		remote = localPath
	}
	if remote == "" {
		return fmt.Errorf("repository has neither localPath nor cloneUrl")
	}
	if err := gitwork.ValidateRemoteURL(remote, true); err != nil {
		return fmt.Errorf("git clone: %w", err)
	}
	baseBranch := repositoryBaseBranch(repository)
	if err := workspaceprep.Do(ctx, "clone", "正在克隆仓库："+repository.Name, func(ctx context.Context) error {
		clone := func(branch string, useReference bool) error {
			args := []string{"clone", "--progress", "--no-hardlinks", "--single-branch", "--branch", branch}
			watch := cloneWatch(repository)
			if useReference && localIsGit && cloneURL != "" && localPath != cloneURL {
				args = append(args, "--reference", localPath, "--dissociate")
				// --dissociate can repack silently for minutes after the transfer.
				watch.Stall = 0
			}
			// Clone into the existing directory (`.`) so git cannot unlink and
			// recreate `workspace`. That inode is the Docker bind-mount source.
			args = append(args, "--", cloneRemoteForGit(ctx, remote), ".")
			if _, err := gitwork.Watch(ctx, workspace, watch, args...); err != nil {
				return fmt.Errorf("git clone: %w", err)
			}
			return nil
		}
		// Docker bind-mounts this directory. Replacing it (RemoveAll + MkdirAll)
		// changes the inode; runc then rejects `docker exec -w /workspace`.
		reset := resetCloneDestination
		if liveDockerSandbox(workspace) {
			reset = func(string) error { return nil }
		}
		if err := reset(workspace); err != nil {
			return err
		}
		if err := clone(baseBranch, true); err != nil {
			if procwatch.Stopped(err) || ctx.Err() != nil {
				return err
			}
			if gitwork.UsesSSH(ctx) && !localIsGit {
				return err
			}
			if clearErr := reset(workspace); clearErr != nil {
				return clearErr
			}
			retryErr := err
			// Only retry without --reference when a cache was actually used.
			// A network failure must not immediately repeat a full download.
			if localIsGit && cloneURL != "" && localPath != cloneURL {
				retryErr = clone(baseBranch, false)
			}
			if retryErr != nil {
				if procwatch.Stopped(retryErr) || ctx.Err() != nil {
					return retryErr
				}
				if gitwork.UsesSSH(ctx) {
					return retryErr // An SSH checkout never initializes a remote branch.
				}
				if seeded, seedErr := seedEmptyRepository(ctx, workspace, cloneRemoteForGit(ctx, remote), repository.DefaultBranch, baseBranch, reset); seeded {
					if seedErr != nil {
						return seedErr
					}
				} else if !shouldSeedIntegrateBranch(repository, baseBranch) {
					return err
				} else {
					if mk := reset(workspace); mk != nil {
						return mk
					}
					seed := strings.TrimSpace(repository.DefaultBranch)
					if seed == "" {
						seed = "master"
					}
					if seedErr := clone(seed, false); seedErr != nil {
						if procwatch.Stopped(seedErr) || ctx.Err() != nil {
							return seedErr
						}
						return err
					}
					if pub := publishIntegrateBranch(ctx, workspace, baseBranch); pub != nil {
						if clearErr := reset(workspace); clearErr != nil {
							return clearErr
						}
						if retry := clone(baseBranch, false); retry != nil {
							if procwatch.Stopped(retry) || ctx.Err() != nil {
								return retry
							}
							return fmt.Errorf("create integration branch %s: %w", baseBranch, pub)
						}
					}
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}

	return workspaceprep.Do(ctx, "branch", "正在准备分支："+repository.Name, func(ctx context.Context) error {

		if err := applyWorkspaceGitAuth(ctx, workspace); err != nil {
			return err
		}
		if err := applyWorkspaceGitIdentity(ctx, workspace); err != nil {
			return err
		}
		if origin := strings.TrimSpace(cloneURL); origin != "" {
			if _, err := gitC(ctx, workspace, "remote", "set-url", "--", "origin", origin); err != nil {
				return fmt.Errorf("set origin url: %w", err)
			}
		}
		if shouldSeedIntegrateBranch(repository, baseBranch) {
			if err := fetchRemoteWorkBranch(ctx, workspace, baseBranch); err != nil {
				return err
			}
		}
		for _, gitArgs := range [][]string{{"fetch", "--prune", "origin", baseBranch}, {"checkout", "--force", "-B", "agentflow-base", "origin/" + baseBranch}, {"reset", "--hard", "origin/" + baseBranch}} {
			if _, err := gitC(ctx, workspace, gitArgs...); err != nil {
				return fmt.Errorf("refresh base branch %s: %w", baseBranch, err)
			}
		}
		if repository.WorkBranch != "" {
			if err := fetchRepositoryWorkBranch(ctx, workspace, repository); err != nil {
				return err
			}
			checkout := func() error { return checkoutWorkBranch(ctx, workspace, repository.WorkBranch, baseBranch, true) }
			if repository.RequireExistingWorkBranch {
				checkout = func() error { return checkoutContinuedWorkBranch(ctx, workspace, repository.WorkBranch) }
			}
			if repository.CreateNewWorkBranch {
				// A freshly cloned workspace is where a named branch is created, so this
				// is the one place that must refuse an already existing remote branch.
				checkout = func() error { return checkoutNewWorkBranch(ctx, workspace, repository.WorkBranch) }
			}
			if err := checkout(); err != nil {
				return err
			}
		}
		return recordRepositoryBaseline(workspace, baseBranch)
	})
}

// seedEmptyRepository handles a freshly created Gongfeng repository with no
// commits: clone has no branch to check out, so neither an assistant
// workspace nor an SOP run could write the first file (the owner had to make an
// initial commit by hand). It publishes the base branch
// (and the default branch first, when the base is an integration branch) with
// one empty commit and leaves workspace on it. seeded is false when the
// remote is unreachable or has branches; the clone error then stands.
func seedEmptyRepository(ctx context.Context, workspace, remote, defaultBranch, baseBranch string, reset func(string) error) (seeded bool, err error) {
	heads, lsErr := runGit(ctx, workspace, "ls-remote", "--heads", "--", remote)
	if lsErr != nil || strings.TrimSpace(heads) != "" {
		return false, nil
	}
	if err := reset(workspace); err != nil {
		return true, err
	}
	first := strings.TrimSpace(defaultBranch)
	if first == "" {
		first = "master"
	}
	for _, args := range [][]string{
		{"init", "-q", "."},
		{"symbolic-ref", "HEAD", "refs/heads/" + first},
		{"remote", "add", "--", "origin", remote},
	} {
		if _, err := runGit(ctx, workspace, args...); err != nil {
			return true, fmt.Errorf("initialize empty repository: %w", err)
		}
	}
	ensureGitIdentity(ctx, workspace)
	if _, err := gitC(ctx, workspace, "commit", "-q", "--allow-empty", "-m", "Initialize repository"); err != nil {
		return true, fmt.Errorf("initialize empty repository: %w", err)
	}
	branches := []string{first}
	if baseBranch != first {
		if _, err := gitC(ctx, workspace, "branch", baseBranch); err != nil {
			return true, fmt.Errorf("initialize empty repository: %w", err)
		}
		branches = append(branches, baseBranch)
	}
	for _, branch := range branches {
		if _, err := gitC(ctx, workspace, "push", "-u", "origin", branch); err != nil {
			return true, fmt.Errorf("publish %s to the empty repository: %w", branch, err)
		}
	}
	return true, nil
}

func liveDockerSandbox(workspace string) bool {
	workspace = filepath.Clean(workspace)
	if workspace == "" || workspace == "." {
		return false
	}
	name := "agentflow-" + filepath.Base(workspace)
	return exec.Command("docker", "inspect", name).Run() == nil
}

// resetCloneDestination makes workspace an empty directory without replacing
// it, so a Docker bind-mount of the same path stays valid.
func resetCloneDestination(workspace string) error {
	info, err := os.Stat(workspace)
	if os.IsNotExist(err) {
		// Docker create is delayed until the first Exec, after clone. A missing
		// workspace here is the first checkout, not a live bind-mount.
		if err := os.MkdirAll(workspace, 0o700); err != nil {
			return fmt.Errorf("create workspace: %w", err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace is not a directory: %s", workspace)
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(workspace, entry.Name())); err != nil {
			return fmt.Errorf("clear workspace %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func resumeRepository(ctx context.Context, workspace string, repository domain.RepositoryContext) error {
	if err := applyWorkspaceGitAuth(ctx, workspace); err != nil {
		return err
	}
	if err := applyWorkspaceGitIdentity(ctx, workspace); err != nil {
		return err
	}
	if origin := strings.TrimSpace(repository.CloneURL); origin != "" {
		if err := gitwork.ValidateRemoteURL(origin, true); err != nil {
			return fmt.Errorf("set origin url: %w", err)
		}
		_, _ = gitC(ctx, workspace, "remote", "set-url", "--", "origin", origin)
	}
	baseBranch := repositoryBaseBranch(repository)
	if repository.CreateNewWorkBranch {
		if _, err := os.Stat(filepath.Join(workspace, ".git", "agentflow-baseline")); os.IsNotExist(err) {
			if _, exists := gitRevParse(ctx, workspace, "refs/heads/"+repository.WorkBranch); !exists {
				if err := checkoutNewWorkBranch(ctx, workspace, repository.WorkBranch); err != nil {
					return err
				}
				return recordRepositoryBaseline(workspace, baseBranch)
			}
		}
	}
	if repository.IntegrateBranch != "" && repository.WorkBranch != "" {
		branch, _ := gitC(ctx, workspace, "branch", "--show-current")
		if strings.TrimSpace(branch) == repository.WorkBranch {
			if _, err := gitC(ctx, workspace, "rev-parse", "--verify", "MERGE_HEAD"); err == nil {
				return recordRepositoryBaseline(workspace, baseBranch)
			}
		}
	}
	if _, err := gitC(ctx, workspace, "remote", "get-url", "origin"); err == nil {
		if _, err := gitC(ctx, workspace, "fetch", "--prune", "origin", baseBranch); err != nil {
			if _, fallbackErr := gitC(ctx, workspace, "fetch", "--prune", "origin"); fallbackErr != nil && gitwork.UsesSSH(ctx) {
				return fmt.Errorf("git fetch: %w", fallbackErr)
			}
		}
		if repository.WorkBranch != "" {
			if err := fetchRepositoryWorkBranch(ctx, workspace, repository); err != nil {
				return err
			}
		}
	} else if repository.RequireExistingWorkBranch {
		return fmt.Errorf("cannot continue branch %q: workspace has no origin remote", repository.WorkBranch)
	}
	if repository.WorkBranch != "" {
		checkout := func() error { return checkoutWorkBranch(ctx, workspace, repository.WorkBranch, baseBranch, false) }
		if repository.RequireExistingWorkBranch {
			checkout = func() error { return checkoutContinuedWorkBranch(ctx, workspace, repository.WorkBranch) }
		} else if repository.CreateNewWorkBranch {
			// A local branch in this cached workspace is the one this run created
			// (and may have pushed), so it is resumed. Without it the branch is
			// still to be created here, and a same-named remote branch belongs to
			// someone else: refuse it exactly as a fresh clone does.
			if _, created := gitRevParse(ctx, workspace, "refs/heads/"+strings.TrimSpace(repository.WorkBranch)); !created {
				checkout = func() error { return checkoutNewWorkBranch(ctx, workspace, repository.WorkBranch) }
			}
		}
		if err := checkout(); err != nil {
			return err
		}
	}
	return recordRepositoryBaseline(workspace, baseBranch)
}

// A continued branch is user-selected work. Never apply the legacy namesake
// reset, which can discard uncommitted work when the remote branch advances.
func checkoutContinuedWorkBranch(ctx context.Context, workspace, branch string) error {
	remoteRef := "refs/remotes/origin/" + branch
	if _, ok := gitRevParse(ctx, workspace, remoteRef); !ok {
		return fmt.Errorf("existing work branch %q is unavailable", branch)
	}
	current, _ := gitC(ctx, workspace, "branch", "--show-current")
	if strings.TrimSpace(current) != branch {
		args := []string{"checkout", branch}
		if _, ok := gitRevParse(ctx, workspace, "refs/heads/"+branch); !ok {
			// The main-branch clone has a narrow fetch refspec. Register this
			// existing head so Git recognizes it when setting up tracking.
			if _, err := gitC(ctx, workspace, "config", "--add", "remote.origin.fetch", "+refs/heads/"+branch+":"+remoteRef); err != nil {
				return fmt.Errorf("configure existing work branch tracking: %w", err)
			}
			args = []string{"checkout", "-b", branch, "--track", remoteRef}
		}
		if _, err := gitC(ctx, workspace, args...); err != nil {
			return fmt.Errorf("checkout existing work branch %s: %w", branch, err)
		}
	}
	if status, err := gitC(ctx, workspace, "status", "--porcelain"); err != nil || strings.TrimSpace(status) != "" {
		return err
	}
	// Only advance a clean checkout. Diverged or in-progress work remains
	// available for the normal synchronization or repair step.
	_, _ = gitC(ctx, workspace, "merge", "--ff-only", remoteRef)
	return nil
}

// checkoutNewWorkBranch creates the caller-named work branch. The caller asked
// for a new branch, so a same-named remote (or leftover local) branch is an
// error: silently checking it out would put the run on other people's work.
func checkoutNewWorkBranch(ctx context.Context, workspace, workBranch string) error {
	workBranch = strings.TrimSpace(workBranch)
	if workBranch == "" {
		return fmt.Errorf("creating a work branch requires its name")
	}
	if _, ok := gitRevParse(ctx, workspace, "refs/heads/"+workBranch); ok {
		return fmt.Errorf("本地已存在分支 %s，不能按新建分支处理", workBranch)
	}
	out, err := gitC(ctx, workspace, "ls-remote", "--heads", "origin", "refs/heads/"+workBranch)
	if err != nil {
		return fmt.Errorf("无法确认远端分支 %s 是否存在：%w", workBranch, err)
	}
	for _, line := range strings.Split(out, "\n") {
		_, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && ref == "refs/heads/"+workBranch {
			return fmt.Errorf("远端已存在分支 %s，不能按新建分支处理；继续该分支请用 branchMode=continue", workBranch)
		}
	}
	return createWorkBranchFromBase(ctx, workspace, workBranch)
}

func fetchRepositoryWorkBranch(ctx context.Context, workspace string, repository domain.RepositoryContext) error {
	err := fetchRemoteWorkBranch(ctx, workspace, repository.WorkBranch)
	if err != nil && repository.RequireExistingWorkBranch {
		return fmt.Errorf("继续开发的分支 %q 不存在或无法读取，请确认已有分支名称和权限：%w", repository.WorkBranch, err)
	}
	return nil
}

// fetchRemoteWorkBranch writes refs/remotes/origin/<workBranch>. A plain
// `git fetch origin <branch>` only updates FETCH_HEAD, so --single-branch
// clones never grow origin/<workBranch> and checkout falls back to master.
func fetchRemoteWorkBranch(ctx context.Context, workspace, workBranch string) error {
	workBranch = strings.TrimSpace(workBranch)
	if workBranch == "" {
		return nil
	}
	refspec := "+refs/heads/" + workBranch + ":refs/remotes/origin/" + workBranch
	_, err := gitC(ctx, workspace, "fetch", "origin", refspec)
	return err
}

func gitRevParse(ctx context.Context, workspace, ref string) (string, bool) {
	return gitwork.RevParse(ctx, workspace, ref)
}

func isWorkBranchNamesakeOfBase(ctx context.Context, workspace, localSHA, baseBranch string) bool {
	refs := []string{"agentflow-base", "origin/" + baseBranch}
	if baseBranch != "master" {
		refs = append(refs, "origin/master")
	}
	if baseBranch != "main" {
		refs = append(refs, "origin/main")
	}
	for _, ref := range refs {
		if sha, ok := gitRevParse(ctx, workspace, ref); ok && sha == localSHA {
			return true
		}
	}
	return false
}

func createWorkBranchFromBase(ctx context.Context, workspace, workBranch string) error {
	base := "agentflow-base"
	if _, err := gitC(ctx, workspace, "rev-parse", "--verify", base+"^{commit}"); err != nil {
		base = "HEAD"
	}
	if _, err := gitC(ctx, workspace, "checkout", "-B", workBranch, base); err != nil {
		return fmt.Errorf("create work branch %s: %w", workBranch, err)
	}
	return nil
}

func checkoutWorkBranch(ctx context.Context, workspace, workBranch, baseBranch string, fromFreshClone bool) error {
	if workBranch == "" {
		return nil
	}
	if baseBranch == "" {
		baseBranch = "master"
	}
	originRef := "origin/" + workBranch
	originSHA, originOK := gitRevParse(ctx, workspace, originRef)
	if fromFreshClone {
		if originOK {
			if _, err := gitC(ctx, workspace, "checkout", "--force", "-B", workBranch, originRef); err != nil {
				return fmt.Errorf("checkout origin/%s: %w", workBranch, err)
			}
			return nil
		}
		return createWorkBranchFromBase(ctx, workspace, workBranch)
	}
	localSHA, localOK := gitRevParse(ctx, workspace, workBranch)
	if localOK {
		if _, err := gitC(ctx, workspace, "checkout", workBranch); err != nil {
			return fmt.Errorf("checkout existing work branch %s: %w", workBranch, err)
		}
		if originOK && localSHA != originSHA {
			if isWorkBranchNamesakeOfBase(ctx, workspace, localSHA, baseBranch) {
				if _, err := gitC(ctx, workspace, "checkout", "--force", "-B", workBranch, originRef); err != nil {
					return fmt.Errorf("reset namesake work branch %s to origin: %w", workBranch, err)
				}
				return nil
			}
			_, _ = gitC(ctx, workspace, "merge", "--ff-only", originRef)
		}
		return nil
	}
	if originOK {
		if _, err := gitC(ctx, workspace, "checkout", "-B", workBranch, originRef); err != nil {
			return fmt.Errorf("checkout origin/%s: %w", workBranch, err)
		}
		return nil
	}
	return createWorkBranchFromBase(ctx, workspace, workBranch)
}

// cloneWatch bounds a platform-side clone. A clone that keeps printing
// progress is allowed to be slow; one that prints nothing for the stall window
// is treated as hung. The total limit is off by default so a large SOP
// checkout is not cut short.
func cloneWatch(repository domain.RepositoryContext) procwatch.Options {
	name := strings.TrimSpace(repository.Name)
	if name == "" {
		name = repository.RepositoryID
	}
	return procwatch.Options{
		Stage: "clone", Label: "克隆仓库 " + name,
		Stall: procwatch.DurationEnv(gitwork.CloneStallEnv, 5*time.Minute), StallEnv: gitwork.CloneStallEnv,
		Timeout: procwatch.DurationEnv(gitwork.CloneTimeoutEnv, 0), TimeoutEnv: gitwork.CloneTimeoutEnv,
	}
}

func runGit(ctx context.Context, workspace string, args ...string) (string, error) {
	return gitwork.Run(ctx, workspace, args...)
}

func gitC(ctx context.Context, workspace string, args ...string) (string, error) {
	return gitwork.C(ctx, workspace, args...)
}

func persistentSandboxID(run domain.Run) string {
	return run.ID
}

func workspaceHasRepoWork(workspace string) bool {
	baseline, err := os.ReadFile(gitwork.BaselinePath(workspace))
	if err != nil {
		return false
	}
	head, err := exec.Command("git", "-C", workspace, "rev-parse", "HEAD").Output()
	if err == nil && strings.TrimSpace(string(head)) != strings.TrimSpace(string(baseline)) {
		return true
	}
	status, err := exec.Command("git", "-C", workspace, "status", "--porcelain").Output()
	return err == nil && len(bytes.TrimSpace(status)) > 0
}

func defaultWorkBranch(issueNumber int, issueID string, now time.Time) string {
	branchOwner := "fix"
	if issueNumber > 0 {
		branchOwner = fmt.Sprintf("fix-issue-%d", issueNumber)
	} else if issueID != "" {
		branchOwner = "fix-" + safeGitRefSegment(issueID)
	}
	branch := "feature/" + branchOwner + "-" + now.In(time.Local).Format("20060102-1504")
	if issueNumber <= 0 && issueID == "" {
		// Nothing ties these runs together (chat workspaces, free
		// collaboration), so two started in the same minute must not share
		// a branch: they would push into each other's commits and MR.
		branch += "-" + workBranchNonce()
	}
	return branch
}

func workBranchNonce() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano()%(1<<24), 36)
	}
	return hex.EncodeToString(b[:])
}

func safeGitRefSegment(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else if b.Len() > 0 {
			b.WriteByte('-')
		}
		if b.Len() >= 64 {
			break
		}
	}
	result := strings.Trim(b.String(), "-.")
	if result == "" {
		return "run"
	}
	return result
}

func recordRepositoryBaseline(workspace, baseBranch string) error {
	return gitwork.RecordBaseline(workspace, baseBranch)
}
