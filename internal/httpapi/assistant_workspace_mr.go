package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"agentflow/internal/domain"
)

// The agent delivers a conversation workspace with its own git: it pushes and
// opens the PR/MR itself using the requester's Git credentials. Platform
// bookkeeping (requirement link, MR merge tracking, requirement status) only
// needs the resulting links, which workbench_delivery_register records.
const workbenchDeliveryRegister = "workbench_delivery_register"

func deliveryRegisterDescriptor() agentToolDescriptor {
	return agentToolDescriptor{
		Name:        workbenchDeliveryRegister,
		Description: "登记本对话交付的 PR/MR。你在工作区里用 git push 并开好 PR/MR 后调用一次，平台把它关联到需求、标记需求为待合入，并在合入后自动完成需求。urls 填 PR/MR 链接（GitHub https://github.com/<owner>/<repo>/pull/<n> 或工蜂 merge_requests 链接）；title/description 用于本对话还没有关联需求时新建需求。只读排查、没有开 PR/MR 时不要调用。",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"urls":{"type":"array","minItems":1,"maxItems":20,"items":{"type":"string","maxLength":500}},"title":{"type":"string","minLength":1,"maxLength":200,"description":"需求标题，一句话写清交付了什么"},"description":{"type":"string","maxLength":20000}},"required":["urls","title"],"additionalProperties":false}`),
		Kind:        "workspace",
	}
}

type deliveredMR struct {
	Repository   string
	URL          string
	TargetBranch string
}

func projectRepositories(all []domain.Repository, projectID string) []domain.Repository {
	out := []domain.Repository{}
	for _, r := range all {
		if r.ProjectID == projectID {
			out = append(out, r)
		}
	}
	return out
}

func (h *agentToolGateway) registerConversationDelivery(ctx context.Context, s *AgentToolSession, args map[string]any) (string, error) {
	if s.RunID != "" {
		return "", fmt.Errorf("任务会话由 SOP 交付节点登记交付")
	}
	runID := s.workspaceRun()
	run, ok := h.repo.RunState(runID)
	if !ok || run.ExecutionContext.ProjectID != s.ProjectID || !run.IsWorkspace() {
		return "", fmt.Errorf("本对话还没有工作区：先用 workbench_workspace_open 打开工作区并开好 PR/MR")
	}
	title, _ := args["title"].(string)
	title = strings.TrimSpace(title)
	if title == "" || len([]rune(title)) > 200 {
		return "", fmt.Errorf("title is required and must be at most 200 characters")
	}
	description, _ := args["description"].(string)
	if len([]rune(description)) > 20000 {
		return "", fmt.Errorf("description must be at most 20000 characters")
	}
	raw, _ := args["urls"].([]any)
	mrs := []deliveredMR{}
	invalid := []string{}
	for _, item := range raw {
		text, _ := item.(string)
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if _, _, _, ok := domain.ParseGongfengMR(text); !ok {
			invalid = append(invalid, text)
			continue
		}
		mrs = append(mrs, deliveredMR{URL: text})
	}
	if len(invalid) > 0 {
		return "", fmt.Errorf("这些链接不是 GitHub PR 或工蜂 MR：%s", strings.Join(invalid, "、"))
	}
	if len(mrs) == 0 {
		return "", fmt.Errorf("urls 至少填一个 PR/MR 链接")
	}
	issue, created, err := h.linkConversationRequirement(s, run, conversationRequirement{Title: title, Description: strings.TrimSpace(description)}, true)
	if err != nil {
		return "", err
	}
	if err := h.recordConversationMRs(issue.ID, mrs, projectRepositories(h.repo.Repositories(), s.ProjectID)); err != nil {
		log.Printf("record conversation workspace %s delivery: %v", run.ID, err)
		return "", fmt.Errorf("登记交付失败：%v", err)
	}
	out := map[string]any{"registered": len(mrs)}
	if issue.ID != "" {
		out["issueId"], out["issueTitle"] = issue.ID, issue.Title
		note := fmt.Sprintf("已登记到需求 %s「%s」，PR/MR 合入后需求自动完成。", issue.ID, truncateRunes(issue.Title, 60))
		if created {
			note = fmt.Sprintf("已新建项目需求 %s「%s」并登记这些 PR/MR，合入后自动完成；告诉用户可以在需求列表查看。", issue.ID, truncateRunes(issue.Title, 60))
		}
		out["note"] = note
	}
	body, _ := json.Marshal(out)
	return redactAgentToolResult(string(body), s.User), nil
}
