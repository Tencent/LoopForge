package engine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// BaselineAlignWorkflowID is the built-in SOP that resolves default→integrate
// conflicts before scheduled issue SOPs are dispatched.
const BaselineAlignWorkflowID = "scheduled-baseline-align"

// BaselineAlignAgentID is the platform-shared resolve role for that SOP.
const BaselineAlignAgentID = domain.BaselineAlignAgentID

// AlignConflictError means the shared integrate branch and the repository
// default branch diverged. Scheduled issue SOPs for that repository must wait.
type AlignConflictError struct {
	Integrate string
	Default   string
	Files     []string
	Blocks    []domain.ConflictBlock
}

func (e *AlignConflictError) Error() string {
	files := strings.Join(e.Files, ", ")
	if files == "" {
		files = "unknown"
	}
	return fmt.Sprintf("集成分支 %s 与默认分支 %s 存在冲突（%s）。已暂停该仓库的定时派发；对齐 Agent 正在自动解决并合入基线，完成后定时任务会继续", e.Integrate, e.Default, files)
}

func alignOnlyRun(r domain.Run) bool {
	if r.WorkflowID == BaselineAlignWorkflowID || r.WorkflowSnapshot.ID == BaselineAlignWorkflowID {
		return true
	}
	if r.Repository.AlignOnly {
		return true
	}
	for _, rc := range r.RepositoryContexts() {
		if rc.AlignOnly {
			return true
		}
	}
	return false
}

func alignRunWaitingApproval(r domain.Run) bool {
	if !alignOnlyRun(r) {
		return false
	}
	for _, node := range r.WorkflowSnapshot.Nodes {
		if node.Type != "approval" {
			continue
		}
		if nr := r.Nodes[node.ID]; nr != nil && nr.Status == "waiting_approval" {
			return true
		}
	}
	return false
}

func autoSucceedAlignApprovals(r *domain.Run, now time.Time) bool {
	if r == nil || !alignOnlyRun(*r) {
		return false
	}
	changed := false
	for _, node := range r.WorkflowSnapshot.Nodes {
		if node.Type != "approval" {
			continue
		}
		nr := r.Nodes[node.ID]
		if nr == nil || nr.Status != "waiting_approval" {
			continue
		}
		nr.Status = "succeeded"
		nr.FinishedAt = now
		changed = true
	}
	if changed {
		r.Status = "running"
	}
	return changed
}

// RenderBlocks renders the conflicted hunks as a labeled markdown fragment
// for the alignment agent prompt and the platform UI. Empty when no blocks
// were extracted (e.g. binary files only).
func (e *AlignConflictError) RenderBlocks() string {
	if len(e.Blocks) == 0 {
		return ""
	}
	var b strings.Builder
	for _, blk := range e.Blocks {
		fmt.Fprintf(&b, "\n### 冲突文件 %s\n```text\n<<<<<<< ours（集成分支 %s）\n%s\n=======\n%s\n>>>>>>> theirs（默认分支 %s）\n```\n",
			blk.File, e.Integrate, blk.Ours, blk.Theirs, e.Default)
	}
	return b.String()
}

func ShouldAlignIntegrate(r domain.RepositoryContext) bool {
	integ := domain.NormalizeBranchName(r.IntegrateBranch)
	def := domain.NormalizeBranchName(r.DefaultBranch)
	return integ != "" && def != "" && !domain.SameGitBranch(integ, def)
}

// AlignIntegrateBaseline merges the default branch into the shared integrate
// branch and pushes when the merge is clean. Conflicts are aborted and
// returned as AlignConflictError (with extracted conflict hunks); the remote
// integrate branch is left unchanged.
func AlignIntegrateBaseline(repository domain.RepositoryContext, gitToken string, locks IntegrationLocker) error {
	if !ShouldAlignIntegrate(repository) {
		return nil
	}
	if strings.TrimSpace(repository.CloneURL) == "" && strings.TrimSpace(repository.LocalPath) == "" {
		return nil
	}
	dir, err := os.MkdirTemp("", "agentflow-align-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	work := repository
	work.WorkBranch = ""
	work.IntegrateBranch = domain.NormalizeIntegrateBranch(repository.IntegrateBranch)
	work.BaseBranch = work.IntegrateBranch
	ctx := withGitToken(context.Background(), gitToken)
	if err := prepareRepository(ctx, dir, work); err != nil {
		return err
	}
	unlock, err := lockIntegration(locks, repository.RepositoryID)
	if err != nil {
		return err
	}
	defer unlock()
	ensureGitIdentity(ctx, dir)
	conflicts, err := mergeLatestRemoteBranch(ctx, dir, work.DefaultBranch)
	if err != nil {
		return fmt.Errorf("merge %s into %s: %w", work.DefaultBranch, work.IntegrateBranch, err)
	}
	if len(conflicts) > 0 {
		// The failed merge leaves conflict markers in the worktree — harvest
		// both sides per hunk before aborting, so the platform can show the
		// actual conflict content instead of only file names.
		blocks := extractConflictBlocks(dir, conflicts)
		_, _ = gitC(ctx, dir, "merge", "--abort")
		return &AlignConflictError{Integrate: work.IntegrateBranch, Default: work.DefaultBranch, Files: conflicts, Blocks: blocks}
	}
	if _, err := gitC(ctx, dir, "push", "origin", "HEAD:refs/heads/"+work.IntegrateBranch); err != nil {
		return fmt.Errorf("push aligned integration branch %s: %w", work.IntegrateBranch, err)
	}
	return nil
}

func extractConflictBlocks(dir string, files []string) []domain.ConflictBlock {
	return domain.ExtractConflictBlocks(dir, files)
}

func parseConflictMarkers(file string, data []byte, max int) []domain.ConflictBlock {
	return domain.ParseConflictMarkers(file, data, max)
}
