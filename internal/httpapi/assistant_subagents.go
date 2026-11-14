package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// roleSubagent is a mentioned role with the rules and skills it is bound to,
// resolved for the conversation's project the way an SOP node resolves them.
type roleSubagent struct {
	Role       domain.AgentDefinition
	Rules      []domain.RuleDefinition
	Skills     []domain.SkillDefinition
	SkillFiles map[string]map[string][]byte
}

// resolveRoleSubagent attaches the role's own rules and skills. MCP servers
// are not attached: chat only proxies the project's HTTP MCP through the
// session gateway and never starts stdio commands on the control plane.
func (h *assistantHandlers) resolveRoleSubagent(role domain.AgentDefinition, projectID string, userID int64) roleSubagent {
	out := roleSubagent{Role: role}
	if h.engine == nil {
		return out
	}
	assets := h.engine.RoleAssets(role, projectID, userID)
	bound := map[string]bool{}
	for _, id := range role.Rules {
		bound[id] = true
	}
	// The knowledge toggle adds a usage rule for an MCP chat does not mount.
	for _, rule := range assets.Rules {
		if bound[rule.ID] {
			out.Rules = append(out.Rules, rule)
		}
	}
	out.Skills, out.SkillFiles = assets.Skills, assets.SkillFiles
	return out
}

// roleSubagentName is the native subagent type a mentioned role runs as.
func roleSubagentName(id string) string {
	name := strings.Trim(roleSubagentUnsafe.ReplaceAllString(strings.ToLower(id), "-"), "-")
	return "role-" + name
}

var roleSubagentUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// roleAgentFile renders a role as a CodeBuddy agent definition: frontmatter
// with the subagent name, description and the role's skills (already written
// to the CLI home), then the role's brief followed by its rules.
func roleAgentFile(item roleSubagent) string {
	return renderRoleAgentFile(item, false)
}

func renderRoleAgentFile(item roleSubagent, workspace bool) string {
	role := item.Role
	description := strings.TrimSpace(role.Description)
	if description == "" {
		description = role.Name
	}
	quote := func(v string) string {
		raw, _ := json.Marshal(v)
		return string(raw)
	}
	var b strings.Builder
	b.WriteString("---\nname: " + roleSubagentName(role.ID) + "\n")
	b.WriteString("description: " + quote("角色「"+role.Name+"」："+description) + "\n")
	// Submitting the reply ends Main Bot's whole turn with the subagent's
	// text; a write that needs the user's confirmation goes back to Main Bot.
	b.WriteString("disallowedTools:\n")
	for _, tool := range subagentBlockedTools {
		b.WriteString("  - " + tool + "\n")
	}
	if len(item.Skills) > 0 {
		b.WriteString("skills:\n")
		for _, skill := range item.Skills {
			b.WriteString("  - " + runtime.SkillName(skill.ID) + "\n")
		}
	}
	b.WriteString("---\n")
	b.WriteString("你是「" + role.Name + "」，由 Main Bot 派发来完成一项任务。按以下角色定义工作，完成后简要汇报做了什么、改了哪些文件、结果如何；做不到时如实说明原因。\n\n")
	b.WriteString(subagentChatNotes)
	b.WriteString(subagentRolePrompt(role.Prompt) + subagentRules(item.Rules) + "\n")
	// The body is rendered by the CLI on the control plane like a skill.
	if workspace {
		return b.String()
	}
	return runtime.InertCLIText(b.String())
}

// subagentChatNotes tells a role subagent what Main Bot's system prompt tells
// Main Bot, which a subagent does not receive: where the code is, and that a
// chat dispatch is not an SOP node.
// subagentBlockedTools are Main Bot's alone: submitting the reply ends the
// whole turn, and opening a workspace switches the one Main Bot is bound to.
var subagentBlockedTools = []string{
	"mcp__agentflow_session__" + submitAssistantReplyTool,
	"mcp__agentflow_session__workbench_workspace_open",
}

const subagentChatNotes = `## 在对话里工作

- 这是对话里的派发，不是 SOP 节点：以 Main Bot 交给你的任务为准。角色定义里要求的交付物文件照常写到工作区根目录下对应的路径（如 archive.md），你结束后平台会自动收集并展示给用户，不用再附加；只有 Main Bot 的任务明确说不要写文件时才不写。产物校验脚本、门禁命令和 {{…}} 输入输出是 SOP 节点才有的，对话里不适用。
- 当前是平台模式，CLI 在会话绑定机器的缓存目录内运行，不能在这里搜索或修改项目代码。需要读写代码或跑命令时请向 Main Bot 汇报，让它先打开 Docker 工作区并交接模式，再派发编程任务。你不能打开或切换工作区（workbench_workspace_open 对你不可用），也不能切换运行模式。SOP 执行中的文件可通过已提供的工作区只读工具查看。
- 写操作被拒绝、提示需要用户确认或放入 drafts 时，不要自己提交回复或草稿：在汇报里写明要做的操作和完整参数，由 Main Bot 交给用户确认。

`

// labelRoleSubagent names the role an Agent dispatch runs as, so the UI shows
// the role instead of its subagent type.
func labelRoleSubagent(event assistantStreamEvent, roles []roleSubagent) assistantStreamEvent {
	if event.Subagent == "" {
		return event
	}
	for _, role := range roles {
		if roleSubagentName(role.Role.ID) == event.Subagent {
			event.Role = role.Role.Name
			break
		}
	}
	return event
}

// roleAgentIDs maps each subagent type to the agent ID of its latest finished
// dispatch in the stored transcript, so a later turn can resume it.
func roleAgentIDs(messages []domain.AssistantMessage) map[string]string {
	out := map[string]string{}
	for _, message := range messages {
		types := map[string]string{}
		for _, event := range message.Activity {
			if event.Kind == "tool" && event.Subagent != "" && event.ID != "" {
				types[event.ID] = event.Subagent
			}
			if event.Kind == "toolDone" && event.AgentID != "" && !event.Failed {
				if kind := types[event.ID]; kind != "" {
					out[kind] = event.AgentID
				}
			}
		}
	}
	return out
}

// subagentRules renders rules like an SOP node's agent-system.md, highest
// priority first.
func subagentRules(rules []domain.RuleDefinition) string {
	if len(rules) == 0 {
		return ""
	}
	sorted := append([]domain.RuleDefinition(nil), rules...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Priority > sorted[j].Priority })
	var b strings.Builder
	b.WriteString("\n\n## Rules")
	for _, rule := range sorted {
		b.WriteString("\n\n### " + rule.Name + "\n" + rule.Content)
	}
	return b.String()
}

// subagentRolePrompt shows a role's run brief to a chat subagent, where no
// run is bound, so runtime placeholders such as {{input}} have no value.
func subagentRolePrompt(prompt string) string {
	return rolePromptPlaceholder.ReplaceAllString(strings.TrimSpace(prompt), "（运行时提供）")
}

var rolePromptPlaceholder = regexp.MustCompile(`\{\{[^{}]+\}\}`)

// writeConversationAssets rewrites the conversation's CLI home each turn:
// .codebuddy/skills holds the project's skills plus those of its roles and
// the mentioned ones, and
// .codebuddy/agents holds one subagent per role, so Main Bot and its
// subagents use them natively. A conversation without a project and without
// mentioned shared roles gets empty directories.
func (h *assistantHandlers) writeConversationAssets(root string, req assistantRequest) error {
	if err := runtime.ValidateConversationRoot(root); err != nil {
		return err
	}
	if err := writeConversationAttachments(root, req); err != nil {
		return err
	}
	skills, files := h.conversationSkills(req)
	engine, _ := assistantModelEngine(req.Model)
	home := conversationAssetHome(root, engine)
	writer := runtime.WriteInertSkillTree
	if req.workspaceMode {
		writer = runtime.WriteSkillTree
	}
	if _, err := writer(filepath.Join(home, "skills"), skills, files); err != nil {
		return err
	}
	agents := filepath.Join(home, "agents")
	if err := os.RemoveAll(agents); err != nil {
		return err
	}
	if len(req.roles) == 0 {
		return nil
	}
	if err := os.MkdirAll(agents, 0o755); err != nil {
		return err
	}
	for _, role := range req.roles {
		name := roleSubagentName(role.Role.ID)
		if err := os.WriteFile(filepath.Join(agents, name+".md"), []byte(conversationRoleAgentFile(role, req.workspaceMode)), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// conversationSkills gathers the skills a conversation home receives: the
// project's skills, then those of its roles, then the mentioned ones. The
// first occurrence of a skill ID wins; file contents are read only for skills
// that reference files.
func (h *assistantHandlers) conversationSkills(req assistantRequest) ([]domain.SkillDefinition, map[string]map[string][]byte) {
	var skills []domain.SkillDefinition
	files := map[string]map[string][]byte{}
	seen := map[string]bool{}
	add := func(skill domain.SkillDefinition, contents map[string][]byte) {
		if seen[skill.ID] {
			return
		}
		seen[skill.ID] = true
		skills = append(skills, skill)
		if len(contents) > 0 {
			files[skill.ID] = contents
		}
	}
	stored := func(skill domain.SkillDefinition) map[string][]byte {
		if len(skill.FileRefs) == 0 {
			return nil
		}
		return h.repo.SkillFileContents(skill.ID)
	}
	if project := strings.TrimSpace(req.ProjectID); project != "" {
		for _, skill := range h.repo.Skills() {
			if skill.ProjectID == project {
				add(skill, stored(skill))
			}
		}
	}
	for _, role := range req.roles {
		for _, skill := range role.Skills {
			add(skill, role.SkillFiles[skill.ID])
		}
	}
	for _, skill := range req.skills {
		add(skill, stored(skill))
	}
	return skills, files
}

// conversationAssetHome is the directory under a conversation root whose
// skills/ and agents/ the given engine reads natively.
func conversationAssetHome(root, engine string) string {
	switch {
	case engine == runtime.AgentCLIPi:
		return filepath.Join(root, "home", ".pi", "agent")
	case engine == runtime.AgentCLIAntigravity:
		return filepath.Join(root, "home", ".gemini", "config")
	case runtime.IsAgentCLI(engine):
		return filepath.Join(root, "home", ".agents")
	default:
		return filepath.Join(root, "home", assistantEngineConfigDir(engine))
	}
}

type roleDeliverableKey struct{}

// collectRoleDeliverables does for a finished role dispatch what an SOP node
// does after it runs: the files the role's artifactOutputs name are read
// from the conversation workspace and attached to this turn's reply. Each
// one becomes a step under the dispatch, so the subagent's 产物 tab lists it;
// a required file that is missing is a failed step. Nothing is retried.
func (h *assistantHandlers) collectRoleDeliverables(ctx context.Context, s *AgentToolSession, dispatchID, subagent string, roles []roleSubagent) []assistantStreamEvent {
	if s == nil || dispatchID == "" || subagent == "" || s.workspaceRun() == "" {
		return nil
	}
	var role *domain.AgentDefinition
	for i := range roles {
		if roleSubagentName(roles[i].Role.ID) == subagent {
			role = &roles[i].Role
			break
		}
	}
	if role == nil || len(role.ArtifactOutputs) == 0 {
		return nil
	}
	gateway := &agentToolGateway{deps: h.deps}
	ctx = context.WithValue(ctx, roleDeliverableKey{}, true)
	var out []assistantStreamEvent
	for i, contract := range role.ArtifactOutputs {
		rel := path.Clean(strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(contract.Path), "\\", "/"), "./"))
		if rel == "." || rel == "" || path.IsAbs(rel) || strings.HasPrefix(rel, "..") {
			continue
		}
		id := fmt.Sprintf("deliverable-%s-%d", dispatchID, i)
		_, err := gateway.callMainBotWorkspace(ctx, s, "workbench_workspace_attach", map[string]any{"path": rel})
		if err != nil && !contract.Required {
			continue
		}
		step := assistantStreamEvent{Kind: "tool", ID: id, Parent: dispatchID, Tool: "交付物", Detail: path.Base(rel), Text: "已收集交付物 · " + rel}
		done := assistantStreamEvent{Kind: "toolDone", ID: id, Parent: dispatchID}
		if err != nil {
			step.Text = "缺少必需交付物 · " + rel
			done.Failed = true
			log.Printf("assistant deliverable session=%s role=%s path=%s: %v", s.ChatSessionID, role.ID, rel, err)
		}
		out = append(out, step, done)
	}
	return out
}

func conversationRoleAgentFile(role roleSubagent, workspace bool) string {
	body := renderRoleAgentFile(role, workspace)
	if !workspace {
		return body
	}
	body = strings.Replace(body, subagentChatNotes, "你和 Main Bot 在同一 Docker 工作区，使用原生文件和命令工具直接执行；平台与外部操作遵守当前审批模式。不能打开或切换工作区，不能切回平台模式；需要切换时向 Main Bot 汇报。\n", 1)
	body = strings.Replace(body, "  - mcp__agentflow_session__workbench_workspace_open\n", "  - mcp__agentflow_session__workbench_workspace_open\n  - mcp__agentflow_session__workbench_platform_mode\n", 1)
	return body
}
