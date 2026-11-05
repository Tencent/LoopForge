package httpapi

import (
	"encoding/json"
	"os"
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

- 这是对话里的派发，不是 SOP 节点：以 Main Bot 交给你的任务为准。下面的角色定义里关于 SOP 节点的约定（要产出的固定文件、产物校验、{{…}} 输入输出）只在任务要求时照做，与任务冲突时按任务来。
- 代码在执行机器的工作区里，只能用 workbench_workspace_tree/read/write/command 访问，路径相对工作区根目录（如 file:///workspace/<仓库>/…）；在 workbench_workspace_command 里跑 grep 检索。你的本地 cwd 是控制面上的会话缓存目录，不是仓库，不要用本地 Read/Grep/Glob 找代码，本地没搜到不等于代码里没有。不要打开或切换工作区；工作区还没打开或需要别的工作区时如实汇报，交给 Main Bot 处理。
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
	if project := strings.TrimSpace(req.ProjectID); project != "" {
		for _, skill := range h.repo.Skills() {
			if skill.ProjectID != project {
				continue
			}
			var contents map[string][]byte
			if len(skill.FileRefs) > 0 {
				contents = h.repo.SkillFileContents(skill.ID)
			}
			add(skill, contents)
		}
	}
	for _, role := range req.roles {
		for _, skill := range role.Skills {
			add(skill, role.SkillFiles[skill.ID])
		}
	}
	for _, skill := range req.skills {
		var contents map[string][]byte
		if len(skill.FileRefs) > 0 {
			contents = h.repo.SkillFileContents(skill.ID)
		}
		add(skill, contents)
	}
	home := filepath.Join(root, "home", ".codebuddy")
	if _, err := runtime.WriteInertSkillTree(filepath.Join(home, "skills"), skills, files); err != nil {
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
		if err := os.WriteFile(filepath.Join(agents, name+".md"), []byte(roleAgentFile(role)), 0o644); err != nil {
			return err
		}
	}
	return nil
}
