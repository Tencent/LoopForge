package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func sopDesignerTurn(t *testing.T, h *assistantHandlers, user domain.User, req assistantRequest) (int, string) {
	t.Helper()
	body, _ := json.Marshal(req)
	c, rec := workbenchContext(user, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
	h.chat(c)
	return rec.Code, rec.Body.String()
}

func listedSessionIDs(t *testing.T, h *assistantHandlers, user domain.User, query string) []string {
	t.Helper()
	c, rec := workbenchContext(user, "GET", "/api/assistant/sessions"+query, bytes.NewBuffer(nil))
	h.listSessions(c)
	if rec.Code != 200 {
		t.Fatalf("list %s: %d %s", query, rec.Code, rec.Body.String())
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// The SOP canvas runs its own conversation: a designer prompt with the canvas,
// roles and referenced SOPs injected by the server, a transcript of only what
// the user typed, and a session that stays out of the workbench history.
func TestSOPDesignerConversationIsPrivateAndServerOwned(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_MODELS", assistantChatDefaultModel)
	h, user := workbenchFixture(t)
	_, calls := nativeCLIRecorder(t)
	if err := h.repo.PutAgent(domain.AgentDefinition{ID: "workbench-dev", Name: "开发工程师", ProjectID: "workbench", Description: "实现代码"}); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutWorkflow(domain.WorkflowDefinition{ID: "workbench-ref", Name: "参考流程", ProjectID: "workbench", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "ref-a", Name: "参考节点", Type: "approval"}}}); err != nil {
		t.Fatal(err)
	}
	canvas := json.RawMessage(`{"id":"sop-a","name":"交付","nodes":[{"id":"n1","name":"实现","type":"agent","agentId":"workbench-dev","position":{"x":10,"y":20},"script":"` + strings.Repeat("x", 500) + `"}],"edges":[]}`)
	code, body := sopDesignerTurn(t, h, user, assistantRequest{
		Surface: assistantSurfaceSOPDesigner, ProjectID: "workbench", SessionID: "designer-chat", Model: assistantChatDefaultModel,
		ApprovalMode: AssistantApprovalAuto, WorkflowID: "sop-a", Canvas: canvas, ReferenceWorkflowIDs: []string{"workbench-ref", "workbench-ref", "missing"},
		Messages: []assistantMessage{{Role: "user", Content: "加一个评审节点"}},
	})
	if code != 200 {
		t.Fatalf("designer turn: %d %s", code, body)
	}
	first := calls()[0]
	system, prompt := first.flag("--system-prompt"), first.prompt()
	if !strings.Contains(system, "AI 编排助手") || strings.Contains(system, "workbench_workspace_open") {
		t.Fatalf("designer turn did not use its own system prompt: %s", system)
	}
	for _, want := range []string{"workbench-dev：开发工程师", `"id":"sop-a"`, "参考的已有 SOP「参考流程」", "加一个评审节点"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("designer prompt lacks %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{"当前是平台模式", "源码文件列表", "子 Agent", `"position"`, strings.Repeat("x", 500)} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("designer prompt carries workbench or layout text %q:\n%s", unwanted, prompt)
		}
	}
	saved, ok := h.repo.AssistantSession(user.ID, "designer-chat")
	if !ok || saved.Source != domain.AssistantSourceSOPDesigner || saved.WorkflowID != "sop-a" || len(saved.Messages) != 2 || saved.Messages[0].Content != "加一个评审节点" {
		t.Fatalf("designer session = %+v", saved)
	}

	// The canvas sends only the next message; the server appends it.
	code, body = sopDesignerTurn(t, h, user, assistantRequest{
		Surface: assistantSurfaceSOPDesigner, ProjectID: "workbench", SessionID: "designer-chat", Model: assistantChatDefaultModel,
		WorkflowID: "sop-a", Canvas: canvas, Messages: []assistantMessage{{Role: "user", Content: "再加人工确认"}},
	})
	if code != 200 {
		t.Fatalf("second designer turn: %d %s", code, body)
	}
	saved, _ = h.repo.AssistantSession(user.ID, "designer-chat")
	if len(saved.Messages) != 4 || saved.Messages[0].Content != "加一个评审节点" || saved.Messages[2].Content != "再加人工确认" {
		t.Fatalf("designer transcript = %+v", saved.Messages)
	}
	if got := calls(); len(got) != 2 || got[1].flag("--resume") == "" {
		t.Fatal("second designer turn did not resume the native session")
	}

	if _, shared := h.repo.ProjectAssistantSession("designer-chat"); shared {
		t.Fatal("designer conversation was shared with the project")
	}
	if containsString(listedSessionIDs(t, h, user, ""), "designer-chat") {
		t.Fatal("designer conversation leaked into the workbench history")
	}
	if !containsString(listedSessionIDs(t, h, user, "?surface=sop-designer&workflowId=sop-a"), "designer-chat") {
		t.Fatal("designer conversation is not listed for its SOP")
	}
	if containsString(listedSessionIDs(t, h, user, "?surface=sop-designer&workflowId=sop-b"), "designer-chat") {
		t.Fatal("designer conversation listed for another SOP")
	}
	code, _ = sopDesignerTurn(t, h, user, assistantRequest{ProjectID: "workbench", SessionID: "designer-chat", Model: assistantChatDefaultModel, Messages: append(saved.Messages, assistantMessage{Role: "user", Content: "改代码"})})
	if code != 409 {
		t.Fatalf("workbench continued a designer conversation: %d", code)
	}
}

func TestSOPDesignerRequestValidation(t *testing.T) {
	for name, req := range map[string]assistantRequest{
		"unknown surface": {Surface: "other", ProjectID: "p"},
		"no project":      {Surface: assistantSurfaceSOPDesigner},
		"run bound":       {Surface: assistantSurfaceSOPDesigner, ProjectID: "p", RunID: "run-1"},
		"canvas not json": {Surface: assistantSurfaceSOPDesigner, ProjectID: "p", Canvas: json.RawMessage(`[1]`)},
		"canvas too big":  {Surface: assistantSurfaceSOPDesigner, ProjectID: "p", Canvas: json.RawMessage(`{"name":"` + strings.Repeat("a", sopDesignerCanvasLimit) + `"}`)},
	} {
		if err := normalizeAssistantSurface(&req); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	req := assistantRequest{Surface: assistantSurfaceSOPDesigner, ProjectID: "p", ApprovalMode: AssistantApprovalAuto, WorkflowID: " sop ", MCPServerIDs: []string{"tapd"}, ReferenceWorkflowIDs: []string{"a", "sop", "a", "b", "c"}}
	if err := normalizeAssistantSurface(&req); err != nil {
		t.Fatal(err)
	}
	if req.ApprovalMode != AssistantApprovalConfirm || req.MCPServerIDs != nil || req.WorkflowID != "sop" || strings.Join(req.ReferenceWorkflowIDs, ",") != "a,b" {
		t.Fatalf("designer request not normalized: %+v", req)
	}
	workbench := assistantRequest{WorkflowID: "sop", Canvas: json.RawMessage(`{}`), ReferenceWorkflowIDs: []string{"a"}}
	if err := normalizeAssistantSurface(&workbench); err != nil || workbench.WorkflowID != "" || workbench.Canvas != nil || workbench.ReferenceWorkflowIDs != nil {
		t.Fatalf("workbench request kept designer fields: %+v %v", workbench, err)
	}
}

func TestSOPDesignerCatalogIsReadOnlyAndDraftOnly(t *testing.T) {
	resetAgentToolSessionsForTest()
	gw := &agentToolGateway{}
	session := &AgentToolSession{Mode: AgentToolAssistant, Surface: assistantSurfaceSOPDesigner, ProjectID: "workbench", ChatSessionID: "designer-chat", ExpiresAt: time.Now().Add(time.Minute), Final: make(chan AgentTerminalResult, 1)}
	names := catalogToolNames(gw.buildCatalog(context.Background(), session))
	for _, want := range []string{"agentflow__workflows_get", "agentflow__agents_list", "workbench_tool_schema", "submit_assistant_reply"} {
		if !strings.Contains(names, want) {
			t.Fatalf("designer catalog lacks %s: %s", want, names)
		}
	}
	for _, unwanted := range []string{"workbench_workspace_open", "workbench_browser", "agentflow__issues_list", "agentflow__runs_get", "_save", "_dispatch"} {
		if strings.Contains(names, unwanted) {
			t.Fatalf("designer catalog exposes %s: %s", unwanted, names)
		}
	}
	for _, call := range []string{
		`{"name":"workbench_workspace_open","arguments":{}}`,
		`{"name":"agentflow__issues_list","arguments":{}}`,
		`{"name":"workbench_publish_artifact","arguments":{}}`,
	} {
		if _, err := gw.callTool(context.Background(), session, json.RawMessage(call)); err == nil {
			t.Fatalf("designer called a tool outside its catalog: %s", call)
		}
	}
	result, err := gw.callTerminal(session, submitAssistantReplyTool, json.RawMessage(`{"message":"建需求","drafts":[{"tool":"issues_save","body":{"projectId":"workbench","title":"x"}}]}`))
	encoded, _ := json.Marshal(result)
	if err != nil || result["isError"] != true || session.finished() || !strings.Contains(string(encoded), "只能提交 workflows_save") {
		t.Fatalf("designer accepted a non-SOP draft: %v %v", result, err)
	}
	result, err = gw.callTerminal(session, submitAssistantReplyTool, json.RawMessage(`{"message":"已搭好","drafts":[{"tool":"workflows_save","body":{"id":"workbench-delivery","projectId":"workbench","sandboxProfileId":"local","name":"交付","nodes":[],"edges":[]}}]}`))
	if err != nil || result["isError"] == true || !session.finished() {
		t.Fatalf("designer rejected its workflows_save draft: %v %v", result, err)
	}
}

func TestSOPDesignerDraftNormalization(t *testing.T) {
	req := assistantRequest{Surface: assistantSurfaceSOPDesigner, WorkflowID: "sop-a", Canvas: json.RawMessage(`{"id":"sop-a"}`)}
	encoded, _ := json.Marshal(`{"name":"交付","nodes":[]}`)
	drafts := sopDesignerDrafts(req, []assistantDraft{{Tool: "issues_save"}, {Tool: "workflows_save", Body: encoded}, {Tool: "workflows_save", Body: json.RawMessage(`{"id":"other"}`)}})
	if len(drafts) != 1 {
		t.Fatalf("drafts = %+v", drafts)
	}
	var body map[string]any
	if err := json.Unmarshal(drafts[0].Body, &body); err != nil || body["id"] != "sop-a" || body["name"] != "交付" {
		t.Fatalf("draft body = %s %v", drafts[0].Body, err)
	}
	fresh := sopDesignerDrafts(assistantRequest{Surface: assistantSurfaceSOPDesigner, WorkflowID: "sop-new"}, []assistantDraft{{Tool: "workflows_save", Body: json.RawMessage(`{"name":"新流程"}`)}})
	if strings.Contains(string(fresh[0].Body), "sop-new") {
		t.Fatal("an empty canvas pinned the draft to a SOP id")
	}
}

func TestWorkflowDraftGraphWarnings(t *testing.T) {
	ok := json.RawMessage(`{"nodes":[{"id":"a","name":"A"},{"id":"b","name":"B"},{"id":"c","name":"C"}],"edges":[{"source":"a","target":"b"},{"source":"a","target":"c"},{"source":"b","target":"c"}]}`)
	if warns := workflowDraftGraphWarnings(ok); len(warns) != 0 {
		t.Fatalf("valid DAG warned: %v", warns)
	}
	bad := json.RawMessage(`{"nodes":[{"id":"a","name":"需求"},{"id":"b","name":"实现"},{"id":"b","name":"重复"},{"id":"c","name":"评审"}],"edges":[{"source":"a","target":"b"},{"source":"b","target":"c"},{"source":"c","target":"b"},{"source":"c","target":"c"},{"source":"a","target":"ghost"}]}`)
	joined := strings.Join(workflowDraftGraphWarnings(bad), "\n")
	for _, want := range []string{"节点 id b 重复", "评审 连向了自己", "需求 → ghost 指向不存在的节点", "流程里有环", "rejectTo"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("graph warnings lack %q:\n%s", want, joined)
		}
	}
}

func TestWorkflowDraftWarningsAcceptPreviewBuildNode(t *testing.T) {
	h, _ := workbenchFixture(t)
	draft := assistantDraft{Tool: "workflows_save", Body: json.RawMessage(`{"nodes":[{"id":"af-preview-build","name":"构建静态站点","type":"agent","agentId":"af-preview-builder"}],"edges":[]}`)}
	if warns := h.assistantDraftWarnings(draft); len(warns) != 0 {
		t.Fatalf("preview build node warned: %v", warns)
	}
}
