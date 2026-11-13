package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/runtime"
)

const (
	conversationPushNode    = "_push"
	conversationPushTimeout = 3 * time.Minute
)

// PushConversationWorkspace commits whatever changed in the run workspace and
// pushes the work branch of every bound repository that differs from its base,
// so a conversation can open an MR without an SOP delivery node. Unchanged
// repositories are left out of the returned heads. Callers
// hold the conversation workspace lease and supply the requesting user's Git
// identity, so the push is attributed to whoever asked for it.
func (e *Engine) PushConversationWorkspace(ctx context.Context, runID string, requesterID int64, gitToken, gitUserName, gitUserEmail, message, sharedTarget string) ([]domain.RunnerWorkBranchHead, error) {
	run, ok := e.repo.RunState(runID)
	if !ok {
		return nil, fmt.Errorf("运行不存在")
	}
	if WorkspaceRunID(run) != run.ID {
		return nil, fmt.Errorf("子 SOP 共享父任务工作区，请从父任务提交")
	}
	credentials := runtime.RunCredentials{GitToken: gitToken}
	var repositories []domain.RepositoryContext
	for _, rc := range run.RepositoryContexts() {
		if rc.ReadOnly || strings.TrimSpace(rc.CloneURL) == "" || strings.TrimSpace(rc.WorkBranch) == "" {
			continue
		}
		if rc.Provider == domain.RepositoryProviderGitLab {
			if credentials.GitLab.BaseURL == "" {
				credentials.GitLab = e.repo.GitLabCredential(requesterID)
			}
			if err := credentials.GitLab.Check(rc.CloneURL); err != nil {
				return nil, fmt.Errorf("%s: %w", rc.Name, err)
			}
		} else if rc.Provider != domain.RepositoryProviderSSH && strings.TrimSpace(gitToken) == "" {
			return nil, fmt.Errorf("%s: 未配置 Git Token，无法推送工作分支。%s", rc.Name, gitTokenFixHint)
		}
		repositories = append(repositories, rc)
	}
	if len(repositories) == 0 {
		return nil, fmt.Errorf("当前任务没有绑定可推送的代码仓库")
	}
	sshKeys := e.repo.GitSSHKeysForRemotes(requesterID, sshRepositoryURLs(repositories))
	for _, rc := range repositories {
		if rc.Provider == domain.RepositoryProviderSSH && sshKeys[rc.CloneURL] == "" {
			return nil, fmt.Errorf("%s: %w", rc.Name, gitwork.ErrSSHKeyRequired)
		}
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = "LoopForge: " + strings.TrimSpace(run.Input)
	}
	message = truncateCommitMessage(message)
	sharedTarget = strings.TrimSpace(sharedTarget)
	ctx, cancel := context.WithTimeout(ctx, conversationPushTimeout)
	defer cancel()
	credentials.GitSSHKeys = sshKeys
	ctx = withGitIdentity(withRunGitCredentials(ctx, credentials), gitUserName, gitUserEmail)
	if runnerID := e.defaultExecutionRunner(run); runnerID != "" {
		return e.pushConversationOnRunner(ctx, runnerID, run, repositories, requesterID, gitToken, gitUserName, gitUserEmail, message, sharedTarget)
	}
	root, reason := e.LocalRunWorkspace(run)
	if reason != "" {
		return nil, errors.New(reason)
	}
	heads := []domain.RunnerWorkBranchHead{}
	for _, rc := range repositories {
		ctx := withRepositoryGit(ctx, rc)
		dir := repositoryWorkspacePath(root, rc)
		if !gitwork.HasWork(ctx, dir, repositoryBaseBranch(rc)) {
			continue
		}
		if err := gitwork.RequireOnWorkBranch(ctx, dir, rc.WorkBranch); err != nil {
			return heads, fmt.Errorf("%s: %w", rc.Name, err)
		}
		if err := applyWorkspaceGitAuth(ctx, dir); err != nil {
			return heads, err
		}
		if err := applyWorkspaceGitIdentity(ctx, dir); err != nil {
			return heads, err
		}
		ensureGitIdentity(ctx, dir)
		var head string
		var err error
		if rc.RequireExistingWorkBranch {
			head, err = gitwork.PushExistingWorkBranch(ctx, dir, rc.WorkBranch, message)
		} else {
			head, err = gitwork.PushWorkBranch(ctx, dir, rc.WorkBranch, message)
		}
		if err != nil {
			return heads, explainGitAuthFailure(fmt.Errorf("%s: git push %s: %w", rc.Name, rc.WorkBranch, err), true)
		}
		if head == "" {
			head, _ = gitwork.RevParse(ctx, dir, "HEAD")
		}
		entry := domain.RunnerWorkBranchHead{RepositoryID: rc.RepositoryID, Name: rc.Name, WorkBranch: rc.WorkBranch, Head: head}
		if sharedTarget != "" {
			merged, conflicts, mergeErr := gitwork.MergeWorkIntoSharedBranch(ctx, dir, rc.WorkBranch, sharedTarget)
			if mergeErr != nil {
				return heads, explainGitAuthFailure(fmt.Errorf("%s: 直推 %s: %w", rc.Name, sharedTarget, mergeErr), true)
			}
			if len(conflicts) > 0 {
				return heads, fmt.Errorf("%s: 合并 %s 进 %s 有冲突，没有推送：%s", rc.Name, rc.WorkBranch, sharedTarget, strings.Join(conflicts, ", "))
			}
			entry.SharedBranch, entry.SharedHead = sharedTarget, merged
		}
		heads = append(heads, entry)
	}
	e.observeConversationPush(run.ID, heads, "")
	return heads, nil
}

func (e *Engine) pushConversationOnRunner(ctx context.Context, runnerID string, run domain.Run, repositories []domain.RepositoryContext, requesterID int64, gitToken, gitUserName, gitUserEmail, message, sharedTarget string) ([]domain.RunnerWorkBranchHead, error) {
	if run.ExecutionContext.RunnerWorkspaceLayout != domain.RunnerWorkspaceLayoutShared {
		return nil, fmt.Errorf("该任务的执行机器工作区不是共享布局，暂不支持直接提交 MR")
	}
	runner, ok := e.repo.Runner(runnerID)
	if !ok {
		return nil, fmt.Errorf("执行机器 %s 不存在", runnerID)
	}
	if err := e.requireWorkBranchCapabilities(runnerID, repositories); err != nil {
		return nil, err
	}
	if sharedTarget != "" && !runner.SupportsSharedPush() {
		return nil, fmt.Errorf("执行机器 %s 的 runner 版本是 %s，还不支持直推共享分支（需要 %s 及以上）；请先升级 runner，或改用 MR 提交", runnerID, strings.TrimSpace(runner.RunnerVersion), domain.RunnerVersionSharedPush)
	}
	invocation := &domain.RunnerInvocation{RunID: run.ID, NodeID: conversationPushNode}
	bindRunnerRepositories(invocation, run, gitToken, gitUserName, gitUserEmail)
	invocation.Repository = nil
	wanted := map[string]bool{}
	for _, rc := range repositories {
		wanted[rc.RepositoryID+"\x00"+rc.WorkspaceDir] = true
	}
	kept := invocation.Repositories[:0]
	for _, repo := range invocation.Repositories {
		if wanted[repo.RepositoryID+"\x00"+repo.Dir] {
			repo.SyncWorkBranch = true
			repo.SharedPushTarget = sharedTarget
			kept = append(kept, repo)
		}
	}
	invocation.Repositories = kept
	if invocation.GitLabUserID > 0 {
		invocation.GitLabUserID = requesterID
	}
	if len(invocation.GitSSHRemotes()) > 0 {
		invocation.GitSSHUserID = requesterID
	} else {
		invocation.GitSSHUserID = 0
	}
	task := domain.RunnerTask{
		ID:                fmt.Sprintf("rt-%d-%s-push", time.Now().UnixNano(), safeRunnerPath(run.ID)),
		RunnerID:          runnerID,
		Provider:          "terminal",
		Action:            domain.RunnerTaskActionPushWorkBranch,
		WorkBranchMessage: message,
		Workspace:         runnerWorkspaceRel(run, conversationPushNode),
		Status:            domain.RunnerTaskQueued,
		MaxAttempts:       1,
		RunID:             run.ID,
		NodeID:            conversationPushNode,
		Invocation:        invocation,
	}
	if err := e.repo.PutRunnerTask(task); err != nil {
		return nil, err
	}
	e.notifyRunnerDispatch("terminal")
	final, err := e.awaitRunnerTask(ctx, task.ID, nil)
	if err != nil {
		return nil, err
	}
	if final.Status != domain.RunnerTaskSuccess {
		msg := strings.TrimSpace(final.Error)
		if msg == "" {
			msg = "执行机器推送工作分支失败（" + final.Status + "）"
		}
		return nil, explainGitAuthFailure(fmt.Errorf("git push: %s", msg), true)
	}
	heads := []domain.RunnerWorkBranchHead{}
	if strings.TrimSpace(final.Result) != "" {
		if err := json.Unmarshal([]byte(final.Result), &heads); err != nil {
			return nil, fmt.Errorf("decode push result: %w", err)
		}
	}
	e.observeConversationPush(run.ID, heads, runnerID)
	return heads, nil
}

func (e *Engine) observeConversationPush(runID string, heads []domain.RunnerWorkBranchHead, runnerID string) {
	for _, head := range heads {
		e.observe(runID, "", domain.TelemetryEvent{Category: "sandbox", Name: "work_branch.pushed", Phase: "finish", Message: fmt.Sprintf("%s @ %s → %s", head.Name, head.WorkBranch, head.Head), Attributes: map[string]any{"repositoryId": head.RepositoryID, "workBranch": head.WorkBranch, "commit": head.Head, "runnerId": runnerID}})
		if head.SharedBranch != "" {
			e.observe(runID, "", domain.TelemetryEvent{Category: "sandbox", Name: "work_branch.shared_pushed", Phase: "finish", Message: fmt.Sprintf("%s @ %s → %s %s", head.Name, head.WorkBranch, head.SharedBranch, head.SharedHead), Attributes: map[string]any{"repositoryId": head.RepositoryID, "workBranch": head.WorkBranch, "sharedBranch": head.SharedBranch, "commit": head.SharedHead, "runnerId": runnerID}})
		}
	}
}

func truncateCommitMessage(message string) string {
	runes := []rune(message)
	if len(runes) > 2000 {
		return string(runes[:2000])
	}
	return message
}
