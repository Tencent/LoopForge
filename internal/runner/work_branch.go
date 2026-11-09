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
	if push {
		if err := requireTaskGitLabOrigins(ctx, workspace, inv); err != nil {
			return heads, err
		}
	}
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
		repoCtx := gitContextForRunnerRepository(ctx, &repo)
		// A continued branch must still exist remotely even when nothing changed.
		if push && !repo.RequireExistingWorkBranch && !gitwork.HasWork(repoCtx, dir, repo.Ref) {
			continue
		}
		if push {
			if err := gitwork.RequireOnWorkBranch(repoCtx, dir, repo.WorkBranch); err != nil {
				return heads, fmt.Errorf("%s: %w", repo.Name, err)
			}
			if err := configureRepositoryGitIdentity(repoCtx, dir, inv.GitUserName, inv.GitUserEmail); err != nil {
				return heads, fmt.Errorf("%s: %w", repo.Name, err)
			}
		}
		var head string
		var err error
		if repo.RequireExistingWorkBranch {
			if push {
				head, err = gitwork.PushExistingWorkBranch(repoCtx, dir, repo.WorkBranch, message)
			} else {
				head, err = gitwork.PullExistingWorkBranch(repoCtx, dir, repo.WorkBranch)
			}
		} else if push {
			head, err = gitwork.PushWorkBranch(repoCtx, dir, repo.WorkBranch, message)
		} else {
			head, err = gitwork.PullWorkBranch(repoCtx, dir, repo.WorkBranch)
		}
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
			merged, conflicts, mergeErr := gitwork.MergeWorkIntoSharedBranch(repoCtx, dir, repo.WorkBranch, repo.SharedPushTarget)
			if mergeErr != nil {
				return heads, fmt.Errorf("%s: 直推 %s: %w", repo.Name, repo.SharedPushTarget, mergeErr)
			}
			if len(conflicts) > 0 {
				return heads, fmt.Errorf("%s: 合并 %s 进 %s 有冲突，没有推送：%s", repo.Name, repo.WorkBranch, repo.SharedPushTarget, strings.Join(conflicts, ", "))
			}
			entry.SharedBranch, entry.SharedHead = repo.SharedPushTarget, merged
		}
		heads = append(heads, entry)
	}
	return heads, nil
}
