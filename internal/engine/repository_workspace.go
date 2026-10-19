package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	if repository.RequireExistingWorkBranch && !existingDevelopmentBranch(repository) {
		return fmt.Errorf("continuing an existing branch requires a development branch distinct from the main and integration branches")
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return fmt.Errorf("create repository directory: %w", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err == nil {
		return resumeRepository(ctx, workspace, repository)
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
		for _, args := range [][]string{{"init", "-b", "agentflow-base"}, {"config", "user.email", "agentflow@local"}, {"config", "user.name", "AgentFlow"}, {"add", "-A"}, {"commit", "-m", "AgentFlow baseline", "--quiet"}} {
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
	baseBranch := repositoryBaseBranch(repository)
	clone := func(branch string, useReference bool) error {
		args := []string{"clone", "--no-hardlinks", "--single-branch", "--branch", branch}
		if useReference && localIsGit && cloneURL != "" && localPath != cloneURL {
			args = append(args, "--reference", localPath, "--dissociate")
		}
		// Clone into the existing directory (`.`) so git cannot unlink and
		// recreate `workspace`. That inode is the Docker bind-mount source.
		args = append(args, gitwork.RewriteRemoteForToken(remote, gitTokenFrom(ctx)), ".")
		if _, err := runGit(ctx, workspace, args...); err != nil {
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
		if clearErr := reset(workspace); clearErr != nil {
			return clearErr
		}
		if retryErr := clone(baseBranch, false); retryErr != nil {
			if !shouldSeedIntegrateBranch(repository, baseBranch) {
				return err
			}
			if mk := reset(workspace); mk != nil {
				return mk
			}
			seed := strings.TrimSpace(repository.DefaultBranch)
			if seed == "" {
				seed = "master"
			}
			if seedErr := clone(seed, false); seedErr != nil {
				return err
			}
			if pub := publishIntegrateBranch(ctx, workspace, baseBranch); pub != nil {
				if clearErr := reset(workspace); clearErr != nil {
					return clearErr
				}
				if retry := clone(baseBranch, false); retry != nil {
					return fmt.Errorf("create integration branch %s: %w", baseBranch, pub)
				}
			}
		}
	}
	if err := applyWorkspaceGitAuth(ctx, workspace); err != nil {
		return err
	}
	if err := applyWorkspaceGitIdentity(ctx, workspace); err != nil {
		return err
	}
	if origin := strings.TrimSpace(cloneURL); origin != "" {
		if _, err := gitC(ctx, workspace, "remote", "set-url", "origin", origin); err != nil {
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
		if err := checkout(); err != nil {
			return err
		}
	}
	return recordRepositoryBaseline(workspace, baseBranch)
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
		_, _ = gitC(ctx, workspace, "remote", "set-url", "origin", origin)
	}
	baseBranch := repositoryBaseBranch(repository)
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
			_, _ = gitC(ctx, workspace, "fetch", "--prune", "origin")
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
	return "feature/" + branchOwner + "-" + now.In(time.Local).Format("20060102-1504")
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
