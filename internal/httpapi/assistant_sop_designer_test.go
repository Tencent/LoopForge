package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
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
	if err := h.repo.PutWorkflow(domain.WorkflowDefinition{ID: "workbench-ref", Name: "参考流程", ProjectID: "workbench", SandboxProfileID: repo.TestNativeSandboxID, Nodes: []domain.WorkflowNode{{ID: "ref-a", Name: "参考节点", Type: "approval"}}}); err != nil {
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
	if !strings.Contains(prompt, "脚本共 500 字") {
		t.Fatalf("a long script reads as empty to the model:\n%s", prompt)
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
	second := saved
	second.ID, second.ProjectID = "designer-second", "second"
	if err := h.repo.PutAssistantSession(user.ID, second); err != nil {
		t.Fatal(err)
	}
	for project, want := range map[string]string{"workbench": "designer-chat", "second": "designer-second"} {
		ids := listedSessionIDs(t, h, user, "?surface=sop-designer&workflowId=sop-a&projectId="+project)
		if len(ids) != 1 || ids[0] != want {
			t.Fatalf("project %s: got %v, want %s", project, ids, want)
		}
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
	result, err = gw.callTerminal(session, submitAssistantReplyTool, json.RawMessage(`{"message":"已搭好","drafts":[{"tool":"workflows_save","body":{"id":"workbench-delivery","projectId":"workbench","sandboxProfileId":"test-native","name":"交付","nodes":[],"edges":[]}}]}`))
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

const designerOpsCanvas = `{"id":"sop-a","projectId":"workbench","sandboxProfileId":"test-native","name":"交付","description":"旧说明","preview":{"enabled":true},
"nodes":[{"id":"plan","name":"计划","type":"agent","agentId":"dev","prompt":"写计划","position":{"x":10,"y":20}},
{"id":"impl","name":"实现","type":"script","script":"make build","position":{"x":300,"y":20}},
{"id":"old","name":"旧节点","type":"approval","position":{"x":600,"y":20}},
{"id":"af-preview-build","name":"构建静态站点","type":"agent","agentId":"af-preview-builder","position":{"x":900,"y":20}}],
"edges":[{"id":"e1","source":"plan","target":"impl","when":"success"},{"id":"e2","source":"impl","target":"old","when":"success"},{"id":"e3","source":"impl","target":"af-preview-build","when":"success"}]}`

func expandDesignerOps(t *testing.T, ops string) (map[string]any, error) {
	t.Helper()
	raw := json.RawMessage(`{"message":"改好了","drafts":[{"tool":"workflows_save","body":{"operations":` + ops + `}}]}`)
	out, err := expandSOPDesignerReply(raw, json.RawMessage(designerOpsCanvas))
	if err != nil {
		return nil, err
	}
	var reply struct {
		Drafts []struct {
			Body map[string]any `json:"body"`
		} `json:"drafts"`
	}
	if err := json.Unmarshal(out, &reply); err != nil {
		t.Fatal(err)
	}
	return reply.Drafts[0].Body, nil
}

func designerNode(body map[string]any, id string) map[string]any {
	for _, item := range body["nodes"].([]any) {
		if node := item.(map[string]any); node["id"] == id {
			return node
		}
	}
	return nil
}

// Operations edit the canvas in place: everything the model did not name,
// including layout, scripts and the preview build node, survives the turn.
func TestSOPDesignerOperationsEditTheCanvas(t *testing.T) {
	body, err := expandDesignerOps(t, `[
		{"op":"add_node","node":{"id":"review","name":"人工评审","type":"approval","position":{"x":1,"y":1}}},
		{"op":"update_node","id":"plan","set":{"prompt":"写详细计划","position":{"x":0,"y":0},"agentId":null}},
		{"op":"remove_node","id":"old"},
		{"op":"add_edge","source":"impl","target":"review"},
		{"op":"remove_edge","source":"plan","target":"impl"},
		{"op":"add_edge","source":"plan","target":"impl"},
		{"op":"update_workflow","set":{"name":"交付 v2"}}]`)
	if err != nil {
		t.Fatal(err)
	}
	if body["id"] != "sop-a" || body["projectId"] != "workbench" || body["sandboxProfileId"] != "test-native" || body["name"] != "交付 v2" || body["description"] != "旧说明" || body["preview"] == nil {
		t.Fatalf("workflow fields lost: %+v", body)
	}
	if _, has := body["operations"]; has {
		t.Fatal("operations leaked into the saved body")
	}
	plan := designerNode(body, "plan")
	if plan["prompt"] != "写详细计划" || plan["agentId"] != nil || plan["position"].(map[string]any)["x"] != float64(10) {
		t.Fatalf("update_node = %+v", plan)
	}
	if impl := designerNode(body, "impl"); impl["script"] != "make build" {
		t.Fatalf("untouched node changed: %+v", impl)
	}
	if designerNode(body, "old") != nil || designerNode(body, "af-preview-build") == nil {
		t.Fatal("remove_node removed the wrong nodes")
	}
	review := designerNode(body, "review")
	if review == nil || review["position"] != nil || review["type"] != "approval" {
		t.Fatalf("add_node = %+v", review)
	}
	var pairs []string
	for _, item := range body["edges"].([]any) {
		edge := item.(map[string]any)
		pairs = append(pairs, edge["id"].(string)+":"+edge["source"].(string)+">"+edge["target"].(string))
	}
	if strings.Join(pairs, ",") != "e3:impl>af-preview-build,edge-impl-review:impl>review,edge-plan-impl:plan>impl" {
		t.Fatalf("edges = %v", pairs)
	}
}

func TestSOPDesignerOperationErrorsAreActionable(t *testing.T) {
	for ops, want := range map[string]string{
		`[]`: "operations 为空",
		`[{"op":"add_node","node":{"id":"plan","name":"x"}}]`:                                           "已存在",
		`[{"op":"update_node","id":"ghost","set":{"name":"x"}}]`:                                        "ghost 不存在",
		`[{"op":"update_node","id":"plan","set":{"id":"p2"}}]`:                                          "不能修改节点 id",
		`[{"op":"add_edge","source":"plan","target":"ghost"}]`:                                          "不存在的节点",
		`[{"op":"add_edge","source":"plan","target":"impl"}]`:                                           "已存在",
		`[{"op":"remove_edge","source":"old","target":"plan"}]`:                                         "不存在",
		`[{"op":"update_workflow","set":{"projectId":"other"}}]`:                                        "不能用 update_workflow",
		`[{"op":"rename","id":"plan"}]`:                                                                 "未知操作",
		`[{"op":"update_node","id":"impl","set":{"rejectTo":"plan"}},{"op":"remove_node","id":"plan"}]`: "第 2 个操作",
	} {
		if _, err := expandDesignerOps(t, ops); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: err = %v, want %q", ops, err, want)
		}
	}
	if _, err := expandSOPDesignerReply(json.RawMessage(`{"message":"x","drafts":[{"tool":"workflows_save","body":{"operations":[{"op":"remove_node","id":"a"}]}}]}`), nil); err == nil || !strings.Contains(err.Error(), "画布是空的") {
		t.Fatalf("operations on an empty canvas: %v", err)
	}
}

func TestSOPDesignerFullBodyKeepsCanvasIdentity(t *testing.T) {
	raw := json.RawMessage(`{"message":"重搭","drafts":[{"tool":"workflows_save","body":{"name":"新流程","nodes":[{"id":"a","name":"A","type":"approval"}],"edges":[]}}]}`)
	out, err := expandSOPDesignerReply(raw, json.RawMessage(designerOpsCanvas))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"id":"sop-a"`) || !strings.Contains(string(out), `"sandboxProfileId":"test-native"`) || strings.Contains(string(out), "旧节点") {
		t.Fatalf("full body = %s", out)
	}
	own := json.RawMessage(`{"message":"另建","drafts":[{"tool":"workflows_save","body":{"id":"workbench-new","name":"新流程","nodes":[],"edges":[]}}]}`)
	out, _ = expandSOPDesignerReply(own, json.RawMessage(designerOpsCanvas))
	if !strings.Contains(string(out), `"id":"workbench-new"`) {
		t.Fatalf("model-chosen id replaced: %s", out)
	}
}

func TestSOPDesignerTerminalAppliesOperations(t *testing.T) {
	resetAgentToolSessionsForTest()
	gw := &agentToolGateway{}
	session := &AgentToolSession{Mode: AgentToolAssistant, Surface: assistantSurfaceSOPDesigner, ProjectID: "workbench", DesignerCanvas: json.RawMessage(designerOpsCanvas), ExpiresAt: time.Now().Add(time.Minute), Final: make(chan AgentTerminalResult, 1)}
	result, err := gw.callTerminal(session, submitAssistantReplyTool, json.RawMessage(`{"message":"改","drafts":[{"tool":"workflows_save","body":{"operations":[{"op":"remove_node","id":"ghost"}]}}]}`))
	encoded, _ := json.Marshal(result)
	if err != nil || result["isError"] != true || session.finished() || !strings.Contains(string(encoded), "ghost 不存在") {
		t.Fatalf("bad operation was not sent back: %s %v", encoded, err)
	}
	result, err = gw.callTerminal(session, submitAssistantReplyTool, json.RawMessage(`{"message":"加评审","drafts":[{"tool":"workflows_save","body":{"operations":[{"op":"add_node","node":{"id":"review","name":"评审","type":"approval"}},{"op":"add_edge","source":"impl","target":"review"}]}}]}`))
	if err != nil || result["isError"] == true || !session.finished() {
		t.Fatalf("operations draft rejected: %v %v", result, err)
	}
	final, _ := session.takeFinal()
	body := string(final.Assistant.Drafts[0].Body)
	if !strings.Contains(body, `"review"`) || !strings.Contains(body, `"make build"`) || !strings.Contains(body, `"position"`) {
		t.Fatalf("expanded draft = %s", body)
	}
}

// The model reads long scripts as an "omitted" marker; whatever path its
// draft takes, the marker never replaces the real script.
func TestSOPDesignerOmittedScriptIsRestored(t *testing.T) {
	long := strings.Repeat("echo test; ", 60)
	canvas := json.RawMessage(`{"id":"sop-a","projectId":"workbench","sandboxProfileId":"test-native","nodes":[{"id":"checks","name":"测试","type":"script","script":"` + long + `"}],"edges":[]}`)
	compact := string(compactSOPDesignerWorkflow(canvas))
	if !strings.Contains(compact, "脚本共 660 字") || strings.Contains(compact, "echo test") {
		t.Fatalf("compact canvas = %s", compact)
	}
	marker := fmt.Sprintf(sopDesignerScriptOmitted, 660)
	for name, draft := range map[string]string{
		"full body":   `{"name":"重搭","nodes":[{"id":"checks","name":"测试","type":"script","script":"` + marker + `"},{"id":"new","name":"新","type":"script","script":"` + marker + `"}],"edges":[]}`,
		"update_node": `{"operations":[{"op":"update_node","id":"checks","set":{"name":"跑测试","script":"` + marker + `"}}]}`,
	} {
		out, err := expandSOPDesignerReply(json.RawMessage(`{"message":"x","drafts":[{"tool":"workflows_save","body":`+draft+`}]}`), canvas)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(string(out), "已省略") || !strings.Contains(string(out), "echo test") {
			t.Fatalf("%s: marker kept or script lost: %s", name, out)
		}
	}
}

// The roles offered to the model and the draft warnings follow the rule a SOP
// save enforces, so the model never picks a role the save then rejects.
func TestSOPDesignerRolesMatchTheSaveRule(t *testing.T) {
	h, _ := workbenchFixture(t)
	for _, role := range []domain.AgentDefinition{
		{ID: "own-dev", Name: "本项目开发", ProjectID: "workbench"},
		{ID: "other-shared", Name: "别的项目共享", ProjectID: "other", Shared: true},
		{ID: "other-private", Name: "别的项目私有", ProjectID: "other"},
		{ID: "global-builtin", Name: "无项目内置", Shared: true},
	} {
		if err := h.repo.PutAgent(role); err != nil {
			t.Fatal(err)
		}
	}
	text := sopDesignerRolesContext(h.repo.Agents(), "workbench")
	for id, want := range map[string]bool{"own-dev": true, "other-shared": true, "other-private": false, "global-builtin": false} {
		if strings.Contains(text, "- "+id+"：") != want {
			t.Fatalf("role %s offered=%v, want %v:\n%s", id, !want, want, text)
		}
	}
	draft := assistantDraft{Tool: "workflows_save", Body: json.RawMessage(`{"projectId":"workbench","nodes":[{"id":"a","name":"A","type":"agent","agentId":"global-builtin"},{"id":"b","name":"B","type":"agent","agentId":"own-dev"},{"id":"c","name":"C","type":"agent","agentId":"other-shared"}],"edges":[]}`)}
	warns := h.assistantDraftWarnings(draft)
	if len(warns) != 1 || !strings.Contains(warns[0], "节点 A") || !strings.Contains(warns[0], "保存会失败") {
		t.Fatalf("warnings = %v", warns)
	}
}
