package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// 助手审批模式（对齐 Codex 的审批策略选择）：
// confirm  每次确认：全部草稿逐个展示，用户点击保存后才执行（默认）。
// assisted 帮我批准：保存类等低风险写入自动执行，删除/发布/派发/启动等
//          危险操作以及存在依赖冲突的草稿仍逐个展示供确认。
// auto     自动执行：全部草稿由平台直接执行，不弹出审批卡片。
const (
	AssistantApprovalConfirm  = "confirm"
	AssistantApprovalAssisted = "assisted"
	AssistantApprovalAuto     = "auto"
)

func normalizeAssistantApprovalMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case AssistantApprovalAssisted:
		return AssistantApprovalAssisted
	case AssistantApprovalAuto:
		return AssistantApprovalAuto
	default:
		return AssistantApprovalConfirm
	}
}

var assistantRiskyToolPattern = regexp.MustCompile(`(_delete|_publish|_dispatch|_start|_cancel|_reject|_run|_restore|_pass|_retry|_pause|_resume|_reopen)$`)

// assistantRiskyToolName 判断平台写入工具是否属于危险操作（删除/发布/
// 派发/启动类）。assisted 模式下这类工具在网关被拦截转为草稿。
func assistantRiskyToolName(tool string) bool {
	tool = strings.TrimSpace(tool)
	if tool == "sync_start" {
		return true
	}
	return assistantRiskyToolPattern.MatchString(tool)
}

// assistantDraftRisky 判断草稿是否属于危险操作。assisted 模式下危险操作
// 仍保留草稿由用户确认，其余自动执行。
func assistantDraftRisky(d assistantDraft) bool {
	return assistantRiskyToolName(d.Tool)
}

// resolveAssistantDrafts 在回复下发前做两件事：
//  1. 对每个草稿运行依赖预检（assistantDraftWarnings），把执行必然失败的
//     原因提前展示给用户，而不是等用户点保存后才收到 409；
//  2. 按审批模式分流：confirm 全部保留；assisted 自动执行低风险且无依赖
//     冲突的草稿；auto 全部自动执行。自动执行的草稿标记 Auto 由前端渲染
//     为执行回执而不是审批卡片。
func (h *assistantHandlers) resolveAssistantDrafts(ctx context.Context, user domain.User, projectID string, drafts []assistantDraft, mode string) []assistantDraft {
	out := make([]assistantDraft, 0, len(drafts))
	auto := make([]assistantDraft, 0, len(drafts))
	for _, draft := range drafts {
		draft.Warnings = h.assistantDraftWarnings(draft)
		// 存在警告（依赖冲突、节点未绑定角色等必然失败或残缺）的草稿，
		// 即使在 auto 模式也不自动执行，降级为带警告的确认卡片。
		if len(draft.Warnings) == 0 && (mode == AssistantApprovalAuto || (mode == AssistantApprovalAssisted && !assistantDraftRisky(draft))) {
			draft.Auto = true
			draft.Body = injectAssistantDraftProjectID(draft.Tool, draft.Body, projectID)
			auto = append(auto, draft)
			continue
		}
		out = append(out, draft)
	}
	if len(auto) > 0 {
		applied := h.applyAssistantDrafts(withAssistantExecMode(ctx, assistantExecMode{AutoWrite: true}), user, projectID, auto)
		out = append(out, applied...)
	}
	return out
}

// injectAssistantDraftProjectID 给保存类草稿补上缺失的 projectId 与 name。
// 模型有时省略这些字段（校验只在字段存在时才比对），平台保存端点却必填；
// 自动执行前注入可避免必然失败的写入。
func injectAssistantDraftProjectID(tool string, body json.RawMessage, projectID string) json.RawMessage {
	tool = strings.TrimSpace(tool)
	projectID = strings.TrimSpace(projectID)
	if tool == "assistant_tasks_save" || !strings.HasSuffix(tool, "_save") || len(body) == 0 {
		return body
	}
	var obj map[string]any
	if json.Unmarshal(body, &obj) != nil || obj == nil {
		return body
	}
	changed := false
	if projectID != "" {
		if raw, exists := obj["projectId"]; !exists || func() bool { s, _ := raw.(string); return strings.TrimSpace(s) == "" }() {
			obj["projectId"] = projectID
			changed = true
		}
	}
	if raw, exists := obj["name"]; !exists || func() bool { s, _ := raw.(string); return strings.TrimSpace(s) == "" }() {
		if id, _ := obj["id"].(string); strings.TrimSpace(id) != "" {
			obj["name"] = id
			changed = true
		}
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// workflowDraftNodeWarnings 检查 workflows_save 草稿的节点完整性：
// agent 节点必须绑定已存在的角色，script 节点必须有脚本。缺失绑定的
// SOP 保存后无法执行，应在用户确认前暴露而不是等运行时失败。
func (h *assistantHandlers) workflowDraftNodeWarnings(d assistantDraft) []string {
	if len(d.Body) == 0 {
		return nil
	}
	var wf struct {
		Nodes []domain.WorkflowNode `json:"nodes"`
	}
	if json.Unmarshal(d.Body, &wf) != nil {
		return nil
	}
	var warns []string
	for _, node := range wf.Nodes {
		name := node.Name
		if name == "" {
			name = node.ID
		}
		switch strings.ToLower(strings.TrimSpace(node.Type)) {
		case "", "agent":
			if strings.TrimSpace(node.AgentID) == "" {
				warns = append(warns, fmt.Sprintf("节点 %s 是 agent 节点但未绑定角色（缺少 agentId），保存后该节点无法执行", name))
				continue
			}
			if _, ok := h.repo.Agent(strings.TrimSpace(node.AgentID)); !ok {
				warns = append(warns, fmt.Sprintf("节点 %s 绑定的角色 %s 不存在，请先创建该角色或改用现有角色", name, node.AgentID))
			}
		case "script":
			if strings.TrimSpace(node.Script) == "" {
				warns = append(warns, fmt.Sprintf("节点 %s 是 script 节点但缺少脚本内容，保存后无法执行", name))
			}
		case "sop":
			if strings.TrimSpace(node.SOPWorkflowID) == "" {
				warns = append(warns, fmt.Sprintf("节点 %s 是 sop 节点但未引用工作流（缺少 sopWorkflowId）", name))
			}
		}
	}
	return warns
}

// assistantDraftRefID 提取草稿目标资源的 id（path/query/body 均可能携带）。
func assistantDraftRefID(d assistantDraft) string {
	for _, value := range []string{
		assistantPathValue(d.Path, "id"),
		assistantPathValue(d.Query, "id"),
		assistantPathValue(d.Path, "workflowId"),
		assistantPathValue(d.Query, "workflowId"),
	} {
		if value != "" {
			return value
		}
	}
	if len(d.Body) == 0 {
		return ""
	}
	var obj map[string]any
	if json.Unmarshal(d.Body, &obj) != nil {
		return ""
	}
	for _, key := range []string{"id", "workflowId"} {
		if v, ok := obj[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// assistantDraftWarnings 复刻 catalog 删除端点的引用检查，在用户确认前
// 预先给出依赖冲突警告。返回空切片表示当前没有已知冲突。
func (h *assistantHandlers) assistantDraftWarnings(d assistantDraft) []string {
	if h == nil || h.repo == nil {
		return nil
	}
	if tool := strings.TrimSpace(d.Tool); tool == "workflows_save" {
		if warns := h.workflowDraftNodeWarnings(d); len(warns) > 0 {
			return warns
		}
	}
	id := assistantDraftRefID(d)
	if id == "" {
		if strings.HasSuffix(strings.TrimSpace(d.Tool), "_delete") {
			return []string{"草稿缺少目标资源 id，执行将失败"}
		}
		return nil
	}
	switch strings.TrimSpace(d.Tool) {
	case "agents_delete":
		agent, ok := h.repo.Agent(id)
		if !ok {
			return []string{fmt.Sprintf("角色 %s 不存在，执行将失败", id)}
		}
		out := []string{}
		if agent.Shared {
			out = append(out, "共享状态的角色不能删除，请先取消共享")
		}
		if names := h.repo.WorkflowNamesUsingAgent(agent); len(names) > 0 {
			out = append(out, fmt.Sprintf("角色仍被 %d 个流程引用（%s），请先解除引用", len(names), strings.Join(names, "、")))
		}
		return out
	case "rules_delete":
		rule, ok := h.repo.Rule(id)
		if !ok {
			return []string{fmt.Sprintf("规则 %s 不存在，执行将失败", id)}
		}
		return sharedAssetWarnings("规则", rule.Shared, h.agentNamesReferencing("rule", id), "角色")
	case "skills_delete":
		skill, ok := h.repo.Skill(id)
		if !ok {
			return []string{fmt.Sprintf("技能 %s 不存在，执行将失败", id)}
		}
		return sharedAssetWarnings("技能", skill.Shared, h.agentNamesReferencing("skill", id), "角色")
	case "dockerfiles_delete":
		item, ok := h.repo.Dockerfile(id)
		if !ok {
			return []string{fmt.Sprintf("Dockerfile %s 不存在，执行将失败", id)}
		}
		if id == repo.GenericBackendDockerfileID {
			return []string{"内置 Dockerfile generic-backend 不能删除"}
		}
		return sharedAssetWarnings("Dockerfile", item.Shared, h.sandboxNamesReferencing("dockerfile", id), "沙箱")
	case "environments_delete":
		item, ok := h.repo.Environment(id)
		if !ok {
			return []string{fmt.Sprintf("环境安装脚本 %s 不存在，执行将失败", id)}
		}
		return sharedAssetWarnings("环境安装脚本", item.Shared, h.sandboxNamesReferencing("environment", id), "沙箱")
	case "sandboxes_delete":
		if id == "local" || id == repo.GenericDockerSandboxID {
			return []string{fmt.Sprintf("内置沙箱 %s 不能删除", id)}
		}
		item, ok := h.repo.Sandbox(id)
		if !ok {
			return []string{fmt.Sprintf("沙箱 %s 不存在，执行将失败", id)}
		}
		return sharedAssetWarnings("沙箱", item.Shared, h.workflowNamesReferencingSandbox(id), "流程")
	case "workflows_delete":
		workflow, ok := h.repo.Workflow(id)
		if !ok {
			return []string{fmt.Sprintf("SOP %s 不存在，执行将失败", id)}
		}
		out := []string{}
		if workflow.Generic {
			out = append(out, "通用模板不可删除，如需修改请直接保存新版本")
		}
		running := 0
		for _, run := range h.repo.Runs() {
			if run.WorkflowID == id && (run.Status == "running" || run.Status == "waiting") {
				running++
			}
		}
		if running > 0 {
			out = append(out, fmt.Sprintf("该 SOP 还有 %d 个进行中的运行，请先终止后再删除", running))
		}
		var boundIssues []string
		for _, issue := range h.repo.Issues() {
			if issue.WorkflowID == id && issue.Status != "closed" {
				boundIssues = append(boundIssues, issue.ID)
			}
		}
		if len(boundIssues) > 0 {
			out = append(out, fmt.Sprintf("该 SOP 仍被 %d 个未关闭的 Issue 绑定（%s），请先改绑", len(boundIssues), strings.Join(boundIssues, "、")))
		}
		return out
	default:
		return nil
	}
}

func sharedAssetWarnings(kind string, shared bool, refNames []string, refKind string) []string {
	out := []string{}
	if shared {
		out = append(out, fmt.Sprintf("共享状态的%s不能删除，请先取消共享", kind))
	}
	if len(refNames) > 0 {
		out = append(out, fmt.Sprintf("%s仍被 %d 个%s引用（%s），请先解除引用", kind, len(refNames), refKind, strings.Join(refNames, "、")))
	}
	return out
}

func (h *assistantHandlers) agentNamesReferencing(kind, id string) []string {
	var names []string
	for _, agent := range h.repo.Agents() {
		refs := agent.Rules
		if kind == "skill" {
			refs = agent.Skills
		}
		for _, ref := range refs {
			if ref == id {
				names = append(names, agent.Name)
				break
			}
		}
	}
	return names
}

func (h *assistantHandlers) sandboxNamesReferencing(kind, id string) []string {
	var names []string
	for _, sandbox := range h.repo.Sandboxes() {
		if (kind == "dockerfile" && sandbox.DockerfileID == id) || (kind == "environment" && sandbox.EnvironmentID == id) {
			names = append(names, sandbox.Name)
		}
	}
	return names
}

func (h *assistantHandlers) workflowNamesReferencingSandbox(id string) []string {
	var names []string
	for _, workflow := range h.repo.Workflows() {
		if workflow.SandboxProfileID == id {
			names = append(names, workflow.Name)
		}
	}
	return names
}
