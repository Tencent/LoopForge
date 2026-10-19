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
		lines = append(lines, domain.RunnerTaskEvent{Seq: int64(i + 1), Type: "system", Data: fmt.Sprintf("work branch %s %s @ %s → %s", verb, head.Name, head.WorkBranch, head.Head), Ts: time.Now().UTC()})
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
		repoCtx := gitwork.WithToken(ctx, repo.GitToken)
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
		if head != "" {
			heads = append(heads, domain.RunnerWorkBranchHead{RepositoryID: repo.RepositoryID, Name: repo.Name, WorkBranch: repo.WorkBranch, Head: head})
		}
	}
	return heads, nil
}
