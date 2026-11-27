package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/forge"
	"agentflow/internal/gitwork"
)

const workspaceForgejoPRTool = "workbench_forgejo_pr_submit"

var (
	forgejoBranchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	forgejoCommitSHA  = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)
)

func workspaceForgejoPRDescriptor() agentToolDescriptor {
	return agentToolDescriptor{
		Name:        workspaceForgejoPRTool,
		Kind:        "workspace",
		Description: "为项目绑定的 Forgejo 仓库创建或更新同仓 PR。先在当前工作区测试、提交并 git push；传仓库 ID、源分支和已推送的完整 commit SHA。targetBranch 默认取工作区起点或绑定的默认分支。按当前审批模式使用发言人的 Forgejo 令牌，返回 mrUrl。该工具只操作 PR。",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"repositoryId":{"type":"string","minLength":1},"sourceBranch":{"type":"string","minLength":1,"maxLength":200},"targetBranch":{"type":"string","maxLength":200},"commit":{"type":"string","description":"已推送的完整 Git commit SHA"},"title":{"type":"string","minLength":1,"maxLength":200},"description":{"type":"string","maxLength":20000}},"required":["repositoryId","sourceBranch","commit","title"],"additionalProperties":false}`),
	}
}

func (h *agentToolGateway) workspaceHasForgejo(projectID string) bool {
	if projectID == "" || len(forge.Instances()) == 0 || h.deps == nil || h.repo == nil {
		return false
	}
	for _, repository := range h.repo.Repositories() {
		if repository.ProjectID == projectID && domain.IsForgeRepositoryProvider(repository.Provider) {
			return true
		}
	}
	return false
}

func (h *agentToolGateway) submitWorkspaceForgejoPR(ctx context.Context, s *AgentToolSession, run domain.Run, args map[string]any) (string, error) {
	if s.RuntimeMode != domain.AssistantModeWorkspace || s.UserID <= 0 || !h.speakerProjectWriter(s.ProjectID, s.User, s.UserID) {
		return "", errAgentToolForbidden
	}
	if !run.IsWorkspace() && !domain.TerminalRunStatus(run.Status) {
		return "", fmt.Errorf("LOOP 运行结束后才能从对话工作区发起 PR")
	}
	arg := func(key string) string {
		value, _ := args[key].(string)
		return strings.TrimSpace(value)
	}
	source, target, head := arg("sourceBranch"), arg("targetBranch"), arg("commit")
	title, body := arg("title"), arg("description")
	if title == "" || len([]rune(title)) > 200 || len([]rune(body)) > 20000 {
		return "", fmt.Errorf("请填写 1 至 200 字的 PR 标题，描述最长 20000 字")
	}
	if !forgejoCommitSHA.MatchString(head) || !validForgejoPRBranch(source) {
		return "", fmt.Errorf("请提供已推送的源分支和完整提交 SHA")
	}
	repository, ok := h.repo.Repository(arg("repositoryId"))
	if !ok || repository.ProjectID != s.ProjectID || !domain.IsForgeRepositoryProvider(repository.Provider) {
		return "", fmt.Errorf("请选择当前项目绑定的 Forgejo 仓库")
	}
	ref, err := forge.RunRepository(repositoryRunContext(repository))
	if err != nil {
		return "", err
	}
	defaultTarget := repository.DefaultBranch
	for _, frozen := range run.RepositoryContexts() {
		if frozen.RepositoryID != repository.ID {
			continue
		}
		if !sameWorkspaceSSHRemote(frozen.CloneURL, repository.CloneURL) || frozen.ReadOnly {
			return "", fmt.Errorf("运行的仓库已换绑或为只读检出")
		}
		defaultTarget = repositoryBaseBranchName(frozen)
		break
	}
	if target == "" {
		target = defaultTarget
	}
	if !validForgejoPRBranch(target) || target == source {
		return "", fmt.Errorf("PR 目标分支须有效且与源分支不同")
	}
	checkouts, err := h.assistant().workspaceCheckouts(ctx, run)
	if err != nil {
		return "", err
	}
	if !slices.ContainsFunc(checkouts, func(checkout gitwork.Checkout) bool {
		return slices.Contains(checkout.Heads, head) && slices.ContainsFunc(checkout.Remotes, func(remote string) bool {
			return sameWorkspaceSSHRemote(remote, repository.CloneURL)
		})
	}) {
		return "", fmt.Errorf("当前工作区的该仓库没有指向此提交的分支")
	}
	if h.repo.ForgeToken(s.UserID, ref.Instance) == "" {
		return "", forge.ErrTokenRequired
	}
	approval := assistantMCPCall{Server: ref.WebURL(), Tool: workspaceForgejoPRTool, Arguments: map[string]any{
		"sourceBranch": source, "targetBranch": target, "commit": head, "title": title, "description": body,
	}}
	if err := h.authorizeMCPWrite(ctx, s, workspaceForgejoPRTool, ref.WebURL(), approval); err != nil {
		return "", err
	}
	current, ok := h.repo.Repository(repository.ID)
	if !ok || current.ProjectID != repository.ProjectID || current.Provider != repository.Provider ||
		current.GongfengProjectID != repository.GongfengProjectID || !sameWorkspaceSSHRemote(current.CloneURL, repository.CloneURL) {
		return "", fmt.Errorf("仓库绑定有更新，请重新发起 PR 请求")
	}
	result, err := h.writeWorkspaceForgejoPR(ctx, s.UserID, ref, source, target, head, title, body)
	if err != nil {
		compareURL := ref.WebURL() + "/compare/" + url.PathEscape(target) + "..." + url.PathEscape(source)
		return "", fmt.Errorf("%w（源分支 %s，提交 %s，比较页 %s）", err, source, head, compareURL)
	}
	prURL := ref.PullURL(result.Pull.Number)
	out := map[string]any{"mrUrl": prURL, "mrIid": result.Pull.Number, "sourceBranch": source,
		"targetBranch": target, "commit": head, "existing": result.Existing, "updated": result.Updated}
	if result.UpdateError != "" {
		out["updateError"] = result.UpdateError
	}
	issueID, err := h.recordWorkspaceForgejoPR(s, run, repository, ref, result.Pull.Number, target, title, body)
	if issueID != "" {
		out["issueId"] = issueID
	}
	if err != nil {
		out["trackingError"] = "PR 已提交，登记需求交付失败"
	}
	raw, err := json.Marshal(out)
	return string(raw), err
}

func (h *agentToolGateway) writeWorkspaceForgejoPR(ctx context.Context, userID int64, ref domain.ForgeRepository, source, target, head, title, body string) (forge.Submission, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, err := forge.OpenRepository(ctx, ref, h.repo.ForgeToken(userID, ref.Instance))
	if err != nil {
		return forge.Submission{}, err
	}
	return client.SubmitPushedPull(ctx, source, target, head, title, body)
}

func (h *agentToolGateway) recordWorkspaceForgejoPR(s *AgentToolSession, run domain.Run, repository domain.Repository, ref domain.ForgeRepository, number int, target, title, body string) (string, error) {
	key := domain.MRDeliveryKey{Project: ref.Project(), IID: number}
	delivered := deliveredMR{URL: ref.PullURL(number), TargetBranch: target, ForgeUserID: s.UserID}
	if existing := h.repo.MRDeliveryStatesMap([]domain.MRDeliveryKey{key})[key]; existing.IssueID != "" {
		if issue, ok := h.repo.Issue(existing.IssueID); ok && issue.ProjectID == s.ProjectID {
			return issue.ID, h.recordConversationMRs(issue.ID, []deliveredMR{delivered}, []domain.Repository{repository})
		}
		return "", nil
	}
	issue, _, err := h.linkConversationRequirement(s, run, conversationRequirement{Title: title, Description: body}, true)
	if err != nil || issue.ID == "" || issue.ProjectID != repository.ProjectID {
		return "", err
	}
	return issue.ID, h.recordConversationMRs(issue.ID, []deliveredMR{delivered}, []domain.Repository{repository})
}

func sameWorkspaceSSHRemote(a, b string) bool {
	left, leftErr := gitwork.ParseSSHRemote(a)
	right, rightErr := gitwork.ParseSSHRemote(b)
	return leftErr == nil && rightErr == nil && left.URL == right.URL
}

func validForgejoPRBranch(branch string) bool {
	return forgejoBranchName.MatchString(branch) && !strings.Contains(branch, "..")
}
