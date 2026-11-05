package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestPersonalConversationRunsInItsOwnRootWithProjectSkills(t *testing.T) {
	h, u := workbenchFixture(t)
	if err := h.repo.PutSkill(domain.SkillDefinition{ID: "notes", Name: "notes", ProjectID: "workbench", Content: "记录要点"}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	bin := filepath.Join(root, "codebuddy")
	homeFile := filepath.Join(root, "home")
	script := `#!/bin/sh
printf '%s' "$HOME" > '` + homeFile + `'
printf '%s\n' '{"type":"result","result":"好"}'
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	send := func(project, session string) string {
		body, _ := json.Marshal(assistantRequest{ProjectID: project, SessionID: session, Messages: []assistantMessage{{Role: "user", Content: "你好"}}})
		c, w := workbenchContext(u, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
		h.chat(c)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		home, _ := os.ReadFile(homeFile)
		return string(home)
	}
	home := send("workbench", "personal-a")
	if home != filepath.Join(assistantConversationRoot(u.ID, "personal-a"), "home") {
		t.Fatalf("home=%s", home)
	}
	if _, err := os.Stat(filepath.Join(home, ".codebuddy", "skills", "notes", "SKILL.md")); err != nil {
		t.Fatal("project skill not written to the conversation home", err)
	}
	other := send("", "personal-b")
	if other == home || strings.HasPrefix(other, assistantCLIHome(u.ID)) {
		t.Fatalf("conversations share a CLI home: %s", other)
	}
	if entries, _ := os.ReadDir(filepath.Join(other, ".codebuddy", "skills")); len(entries) != 0 {
		t.Fatal("a conversation without a project received project skills")
	}
	if assistantConversationRoot(u.ID, "same") == assistantConversationRoot(u.ID+1, "same") {
		t.Fatal("a reused session ID reaches another user's root")
	}
}

func TestProjectConversationIsolatesReferencesAndOffersProjectRoles(t *testing.T) {
	h, u := workbenchFixture(t)
	h.repo.PutProject(domain.Project{ID: "other", Name: "Other", OwnerID: u.ID})
	for _, role := range []domain.AgentDefinition{
		{ID: "dev", Name: "开发", ProjectID: "workbench", Prompt: "实现"},
		{ID: "ops", Name: "运维", ProjectID: "other", Prompt: "部署"},
		{ID: "reviewer", Name: "评审", ProjectID: "other", Shared: true, Prompt: "评审"},
	} {
		h.repo.PutAgent(role)
	}
	h.repo.PutIssue(domain.Issue{ID: "other-issue", Title: "别的需求", ProjectID: "other"})
	c, _ := workbenchContext(u, "POST", "/", bytes.NewBuffer(nil))
	prepare := func(project string, refs ...domain.AssistantReference) (assistantRequest, error) {
		req := assistantRequest{ProjectID: project, Messages: []assistantMessage{{Role: "user", Content: "看看", References: refs}}}
		return req, h.prepareWorkbenchContext(c, &req, u)
	}
	req, err := prepare("workbench")
	if err != nil || len(req.roles) != 1 || req.roles[0].Role.ID != "dev" || !strings.Contains(req.contextText, "subagent_type=role-<角色ID>") {
		t.Fatalf("project roles not offered unmentioned: %+v %v", req.roles, err)
	}
	for _, ref := range []domain.AssistantReference{{Type: "agent", ID: "ops"}, {Type: "issue", ID: "other-issue"}} {
		if _, err := prepare("workbench", ref); err != errForeignReference {
			t.Fatalf("%s from another project accepted: %v", ref.ID, err)
		}
	}
	req, err = prepare("workbench", domain.AssistantReference{Type: "agent", ID: "reviewer"})
	if err != nil || len(req.roles) != 2 {
		t.Fatalf("shared role rejected or not offered: %+v %v", req.roles, err)
	}
	if _, err := prepare("", domain.AssistantReference{Type: "issue", ID: "other-issue"}); err != nil {
		t.Fatal("a personal conversation lost cross-project references", err)
	}
}

func TestSkillMentionsAreScopedHintedAndWrittenToHome(t *testing.T) {
	h, u := workbenchFixture(t)
	h.repo.PutProject(domain.Project{ID: "other", Name: "Other", OwnerID: u.ID})
	for _, skill := range []domain.SkillDefinition{
		{ID: "lint", Name: "代码检查", ProjectID: "workbench", Content: "检查"},
		{ID: "Shared_Doc", Name: "写文档", ProjectID: "other", Shared: true, Content: "文档"},
		{ID: "deploy", Name: "部署", ProjectID: "other", Content: "部署"},
	} {
		if err := h.repo.PutSkill(skill); err != nil {
			t.Fatal(err)
		}
	}
	c, rec := workbenchContext(u, "GET", "/api/assistant/references?projectId=workbench&type=skill", bytes.NewBuffer(nil))
	h.workbenchReferences(c)
	var found []domain.AssistantReference
	json.Unmarshal(rec.Body.Bytes(), &found)
	ids := map[string]bool{}
	for _, item := range found {
		ids[item.ID] = true
	}
	if !ids["lint"] || !ids["Shared_Doc"] || ids["deploy"] {
		t.Fatalf("project skill search=%s", rec.Body.String())
	}
	c, _ = workbenchContext(u, "POST", "/", bytes.NewBuffer(nil))
	req := assistantRequest{ProjectID: "workbench", Messages: []assistantMessage{{Role: "user", Content: "写一下", References: []domain.AssistantReference{{Type: "skill", ID: "Shared_Doc"}}}}}
	if err := h.prepareWorkbenchContext(c, &req, u); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.contextText, "skill=shareddoc") || req.Messages[0].References[0].Name != "写文档" {
		t.Fatalf("skill hint missing: %s", req.contextText)
	}
	root := t.TempDir()
	if err := h.writeConversationAssets(root, req); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lint", "shareddoc"} {
		if _, err := os.Stat(filepath.Join(root, "home", ".codebuddy", "skills", name, "SKILL.md")); err != nil {
			t.Fatalf("skill %s not written: %v", name, err)
		}
	}
	req.Messages[0].References = []domain.AssistantReference{{Type: "skill", ID: "deploy"}}
	if err := h.prepareWorkbenchContext(c, &req, u); err != errForeignReference {
		t.Fatalf("another project's skill accepted: %v", err)
	}
	run := domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "n", AgentID: "dev"}}}}
	h.repo.PutAgent(domain.AgentDefinition{ID: "dev", Name: "开发", ProjectID: "workbench", Skills: []string{"lint"}})
	if !h.taskReferenceAllowed(run, "skill", "lint") || h.taskReferenceAllowed(run, "skill", "Shared_Doc") {
		t.Fatal("task skill references must follow the task's roles")
	}
}

func TestSubagentStreamEventsCarryTheirDispatch(t *testing.T) {
	dispatch := assistantStreamEvents(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-1","name":"Agent","input":{"subagent_type":"role-qa","description":"补单测","prompt":"x"}}]}}`)
	if len(dispatch) != 1 || dispatch[0].Subagent != "role-qa" || dispatch[0].Parent != "" || dispatch[0].Input != "x" {
		t.Fatalf("dispatch=%+v", dispatch)
	}
	if got := labelRoleSubagent(dispatch[0], []roleSubagent{{Role: domain.AgentDefinition{ID: "qa", Name: "测试工程师"}}}); got.Role != "测试工程师" {
		t.Fatalf("role label=%q", got.Role)
	}
	inner := assistantStreamEvents(`{"type":"assistant","parent_tool_use_id":"call-1","message":{"content":[{"type":"thinking","thinking":"先看"},{"type":"text","text":"已补 3 个用例"}]}}`)
	if len(inner) != 2 || inner[0].Parent != "call-1" || inner[1].Kind != "agentText" || inner[1].Parent != "call-1" {
		t.Fatalf("subagent events=%+v", inner)
	}
	done := assistantStreamEvents(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call-1","content":[{"type":"text","text":"已补 3 个用例\n[Agent ID: agent-b8c88507-5a8e]"}]}]}}`)
	// The report is kept without the CLI's agent ID bookkeeping.
	if len(done) != 1 || done[0].Kind != "toolDone" || done[0].AgentID != "agent-b8c88507-5a8e" || done[0].Output != "已补 3 个用例" {
		t.Fatalf("done=%+v", done)
	}
	if plain := assistantStreamEvents(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"r","content":"Agent ID: agent-x1"}]}}`); plain[0].AgentID != "agent-x1" {
		t.Fatalf("string result=%+v", plain)
	}
	// Other tool results stay out of the record.
	if other := assistantStreamEvents(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"r","content":"secret file body"}]}}`); other[0].Output != "" {
		t.Fatalf("plain tool result kept: %+v", other)
	}
}

func TestActivityLogKeepsSubagentThoughtsApart(t *testing.T) {
	var log assistantActivityLog
	log.add(assistantStreamEvent{Kind: "thinking", Text: "主"})
	log.add(assistantStreamEvent{Kind: "thinking", Text: "子", Parent: "call-1"})
	log.add(assistantStreamEvent{Kind: "agentText", Text: "汇报", Parent: "call-1", Full: true})
	log.add(assistantStreamEvent{Kind: "agentText", Text: "汇报", Parent: "call-1", Full: true})
	if len(log.events) != 3 || log.events[0].Text != "主" || log.events[1].Parent != "call-1" || log.events[2].Kind != "agentText" {
		t.Fatalf("events=%+v", log.events)
	}
}

func TestRoleAgentIDsHintReuse(t *testing.T) {
	h, u := workbenchFixture(t)
	h.repo.PutAgent(domain.AgentDefinition{ID: "qa", Name: "测试工程师", ProjectID: "workbench", Prompt: "测试"})
	activity := func(id, agent string, failed bool) []domain.AssistantActivityEvent {
		return []domain.AssistantActivityEvent{{Kind: "tool", ID: id}, {Kind: "tool", ID: id, Subagent: "role-qa"}, {Kind: "toolDone", ID: id, AgentID: agent, Failed: failed}}
	}
	session := domain.AssistantSession{ID: "reuse", ProjectID: "workbench", Messages: []domain.AssistantMessage{
		{Role: "user", Content: "补单测"},
		{Role: "assistant", Content: "好", Activity: activity("c1", "agent-old", false)},
		{Role: "user", Content: "再补"},
		{Role: "assistant", Content: "好", Activity: append(activity("c2", "agent-new", false), activity("c3", "agent-broken", true)...)},
	}}
	if err := h.repo.PutAssistantSession(u.ID, session); err != nil {
		t.Fatal(err)
	}
	if ids := roleAgentIDs(session.Messages); ids["role-qa"] != "agent-new" {
		t.Fatalf("ids=%v", ids)
	}
	c, _ := workbenchContext(u, "POST", "/", bytes.NewBuffer(nil))
	req := assistantRequest{ProjectID: "workbench", SessionID: "reuse", Messages: append(session.Messages, domain.AssistantMessage{Role: "user", Content: "继续"})}
	if err := h.prepareWorkbenchContext(c, &req, u); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.contextText, "agent ID agent-new") || !strings.Contains(req.contextText, "resume=<agent ID>") {
		t.Fatalf("reuse hint missing: %s", req.contextText)
	}
}

// The CLI announces an Agent call with empty input before the full one; the
// stream filter must let the full copy through, or the dispatch loses its
// subagent type, task and the agent ID reuse depends on.
func TestStreamFilterKeepsCompletedAgentCall(t *testing.T) {
	var filter assistantStreamFilter
	var kept []assistantStreamEvent
	for _, line := range []string{
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"call-1","name":"Agent","input":{}}}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-1","name":"Agent","input":{"subagent_type":"role-fe","description":"梳理入口","prompt":"读 chatbot/src"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-1","name":"Agent","input":{"subagent_type":"role-fe","description":"梳理入口","prompt":"读 chatbot/src"}}]}}`,
	} {
		for _, ev := range assistantStreamEvents(line) {
			if filter.accept(ev) {
				kept = append(kept, ev)
			}
		}
	}
	if len(kept) != 2 {
		t.Fatalf("kept=%+v", kept)
	}
	full := kept[1]
	if full.Subagent != "role-fe" || full.Input != "读 chatbot/src" || full.Detail != "梳理入口" {
		t.Fatalf("full dispatch=%+v", full)
	}
	var log assistantActivityLog
	for _, ev := range kept {
		log.add(ev)
	}
	log.add(assistantStreamEvent{Kind: "toolDone", ID: "call-1", AgentID: "agent-1"})
	if ids := roleAgentIDs([]domain.AssistantMessage{{Role: "assistant", Activity: log.snapshot()}}); ids["role-fe"] != "agent-1" {
		t.Fatalf("reuse ids=%v", ids)
	}
}

// A subagent never sees Main Bot's system prompt, so its own file says where
// the code is and that the task outranks the role's SOP-node conventions.
func TestRoleAgentFileExplainsChatWork(t *testing.T) {
	file := roleAgentFile(roleSubagent{Role: domain.AgentDefinition{ID: "fe", Name: "前端", Prompt: "产出 brief.md 到 {{output}}"}})
	for _, want := range []string{"以 Main Bot 交给你的任务为准", "workbench_workspace_tree/read/write/command", "不要用本地 Read/Grep/Glob", "产出 brief.md 到 （运行时提供）"} {
		if !strings.Contains(file, want) {
			t.Fatalf("role file lacks %q:\n%s", want, file)
		}
	}
	if strings.Index(file, "在对话里工作") > strings.Index(file, "产出 brief.md") {
		t.Fatal("chat notes must come before the role prompt")
	}
}

// The CLI renders an agent body on the control plane, so a role prompt can
// neither run a command nor pull in a host file there.
func TestRoleAgentFileIsInert(t *testing.T) {
	file := roleAgentFile(roleSubagent{Role: domain.AgentDefinition{ID: "x", Name: "X", Prompt: "先跑 !`id`，再读 @/etc/passwd"}})
	if strings.Contains(file, "!`") || strings.Contains(file, " @/etc/passwd") {
		t.Fatalf("role file still live:\n%s", file)
	}
}

// A resumed dispatch carries its agent ID from the start, through the stream
// filter, so the panel keeps it on that agent's row while it runs.
func TestResumedDispatchNamesItsAgentUpFront(t *testing.T) {
	var filter assistantStreamFilter
	var kept []assistantStreamEvent
	for _, line := range []string{
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"call-2","name":"Agent","input":{}}}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-2","name":"Agent","input":{"subagent_type":"role-fe","description":"补充","prompt":"再看样式","resume":"agent-044d4197"}}]}}`,
	} {
		for _, ev := range assistantStreamEvents(line) {
			if filter.accept(ev) {
				kept = append(kept, ev)
			}
		}
	}
	if len(kept) != 2 || kept[1].AgentID != "agent-044d4197" {
		t.Fatalf("kept=%+v", kept)
	}
	if got := assistantStreamEvents(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"c","name":"Agent","input":{"subagent_type":"role-fe","prompt":"x","resume":"not an id"}}]}}`); got[0].AgentID != "" {
		t.Fatalf("bad resume taken as ID: %+v", got)
	}
}

// Submitting the reply would end Main Bot's turn with the subagent's text,
// so a role subagent cannot reach it and hands writes back instead.
func TestRoleSubagentCannotSubmitTheReply(t *testing.T) {
	file := roleAgentFile(roleSubagent{Role: domain.AgentDefinition{ID: "x", Name: "X", Prompt: "p"}})
	front := file[:strings.Index(file[4:], "---")+4]
	if !strings.Contains(front, "disallowedTools:\n  - mcp__agentflow_session__submit_assistant_reply\n") {
		t.Fatalf("frontmatter lacks the block:\n%s", front)
	}
	if !strings.Contains(file, "不要自己提交回复或草稿") {
		t.Fatalf("notes lack the hand-back rule:\n%s", file)
	}
}

// Only Main Bot picks the workspace: while a dispatch runs, opening or
// switching one is refused whoever calls it, built-in subagents included.
func TestWorkspaceStaysWhileADispatchRuns(t *testing.T) {
	h, owner := workbenchFixture(t)
	gateway := &agentToolGateway{deps: h.deps}
	s := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: owner.ID, User: owner, ChatSessionID: "chat-1", ChatOwnerID: owner.ID}
	open := func() error {
		_, err := gateway.callMainBotWorkspace(context.Background(), s, "workbench_workspace_open", map[string]any{"new": true})
		return err
	}
	for _, line := range []string{
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"call-1","name":"Agent","input":{}}}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-1","name":"Agent","input":{"subagent_type":"general-purpose","prompt":"x"}}]}}`,
	} {
		for _, ev := range assistantStreamEvents(line) {
			s.trackDispatch(ev)
		}
	}
	// The subagent's own calls carry a parent and never count as dispatches.
	s.trackDispatch(assistantStreamEvent{Kind: "toolDone", ID: "inner", Parent: "call-1"})
	if err := open(); err == nil || !strings.Contains(err.Error(), "子 Agent 运行期间") {
		t.Fatalf("open during a dispatch must be refused: %v", err)
	}
	for _, ev := range assistantStreamEvents(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call-1","content":"done\nAgent ID: agent-1"}]}}`) {
		s.trackDispatch(ev)
	}
	if err := open(); err != nil && strings.Contains(err.Error(), "子 Agent 运行期间") {
		t.Fatalf("open after the dispatch must go through: %v", err)
	}
	if file := roleAgentFile(roleSubagent{Role: domain.AgentDefinition{ID: "x", Name: "X", Prompt: "p"}}); !strings.Contains(file, "  - mcp__agentflow_session__workbench_workspace_open\n") {
		t.Fatalf("role file must block workspace_open:\n%s", file)
	}
}
