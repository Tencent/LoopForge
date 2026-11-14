package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

// executeWorkBranchSync pulls or pushes the work branch of every repository
// the control plane flagged SyncWorkBranch, in the run's shared checkout on
// this machine. These explicit actions remain supported for compatibility;
// ordinary workflow nodes keep their changes local until delivery.
func (d *Daemon) executeWorkBranchSync(ctx context.Context, task domain.RunnerTask) execResult {
	if err := d.client.StartTask(ctx, task.ID); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("start: %v", err)}
	}
	push := task.Action == domain.RunnerTaskActionPushWorkBranch
	heads, err := syncTaskWorkBranches(ctx, d.taskWorkspace(task), task.Invocation, push, task.WorkBranchMessage)
	var lines []domain.RunnerTaskEvent
	for i, head := range heads {
		verb := "pulled"
		if push {
			verb = "pushed"
		}
		data := fmt.Sprintf("work branch %s %s @ %s → %s", verb, head.Name, head.WorkBranch, head.Head)
		if head.SharedBranch != "" {
			shared := "已是最新"
			if head.SharedHead != "" {
				shared = head.SharedHead
			}
			data += fmt.Sprintf("; 直推 %s @ %s", head.SharedBranch, shared)
		}
		lines = append(lines, domain.RunnerTaskEvent{Seq: int64(i + 1), Type: "system", Data: data, Ts: time.Now().UTC()})
	}
	if len(lines) > 0 {
		_ = d.client.SendEvents(ctx, task.ID, lines)
	}
	if err != nil {
		return execResult{status: domain.RunnerTaskFailed, exitCode: 1, errMsg: err.Error()}
	}
	body, _ := json.Marshal(heads)
	return execResult{status: domain.RunnerTaskSuccess, exitCode: 0, result: string(body)}
}

func syncTaskWorkBranches(ctx context.Context, workspace string, inv *domain.RunnerInvocation, push bool, message string) ([]domain.RunnerWorkBranchHead, error) {
	heads := []domain.RunnerWorkBranchHead{}
	if inv == nil {
		return heads, nil
	}
	ctx = gitwork.WithSSHKeys(ctx, inv.GitSSHKeys)
	if strings.TrimSpace(message) == "" {
		message = "agentflow: " + inv.NodeID + " (" + inv.RunID + ")"
	}
	for _, repo := range inv.Repositories {
		if !repo.SyncWorkBranch || strings.TrimSpace(repo.WorkBranch) == "" {
			continue
		}
		dir := workspace
		if rel := safeRunnerRepoDir(repo.Dir); rel != "" {
			dir = filepath.Join(workspace, rel)
		}
		repoCtx := repositoryGitContext(ctx, repo)
		// A continued branch must still exist remotely even when nothing changed.
		if push && !repo.RequireExistingWorkBranch && !gitwork.HasWork(repoCtx, dir, repo.Ref) {
			continue
		}
		if push {
			if err := gitwork.RequireOnWorkBranch(repoCtx, dir, repo.WorkBranch); err != nil {
				return heads, fmt.Errorf("%s: %w", repo.Name, err)
			}
		}
		head, err := syncWorkBranch(repoCtx, dir, repo.WorkBranch, message, push, repo.RequireExistingWorkBranch)
		if err != nil {
			return heads, fmt.Errorf("%s: %w", repo.Name, err)
		}
		if head == "" && push {
			head, _ = gitwork.RevParse(repoCtx, dir, "HEAD")
		}
		if head == "" {
			continue
		}
		entry := domain.RunnerWorkBranchHead{RepositoryID: repo.RepositoryID, Name: repo.Name, WorkBranch: repo.WorkBranch, Head: head}
		if push && strings.TrimSpace(repo.SharedPushTarget) != "" {
			merged, err := pushSharedTarget(repoCtx, dir, repo)
			if err != nil {
				return heads, err
			}
			entry.SharedBranch, entry.SharedHead = repo.SharedPushTarget, merged
		}
		heads = append(heads, entry)
	}
	return heads, nil
}

// syncWorkBranch pulls or pushes one checkout's work branch. Explicitly
// chosen branches (requireExisting) must already exist remotely, so neither
// direction may create them.
func syncWorkBranch(ctx context.Context, dir, branch, message string, push, requireExisting bool) (string, error) {
	switch {
	case requireExisting && push:
		return gitwork.PushExistingWorkBranch(ctx, dir, branch, message)
	case requireExisting:
		return gitwork.PullExistingWorkBranch(ctx, dir, branch)
	case push:
		return gitwork.PushWorkBranch(ctx, dir, branch, message)
	default:
		return gitwork.PullWorkBranch(ctx, dir, branch)
	}
}

// pushSharedTarget merges a pushed work branch into the repository's shared
// push target and returns the merged head ("" when already up to date).
// Conflicts are reported as errors: nothing is pushed in that case.
func pushSharedTarget(ctx context.Context, dir string, repo domain.RunnerRepository) (string, error) {
	merged, conflicts, err := gitwork.MergeWorkIntoSharedBranch(ctx, dir, repo.WorkBranch, repo.SharedPushTarget)
	if err != nil {
		return "", fmt.Errorf("%s: 直推 %s: %w", repo.Name, repo.SharedPushTarget, err)
	}
	if len(conflicts) > 0 {
		return "", fmt.Errorf("%s: 合并 %s 进 %s 有冲突，没有推送：%s", repo.Name, repo.WorkBranch, repo.SharedPushTarget, strings.Join(conflicts, ", "))
	}
	return merged, nil
}
