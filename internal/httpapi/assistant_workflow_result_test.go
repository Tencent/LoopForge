package httpapi

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestToolResultRedactionKeepsTheBody(t *testing.T) {
	const secret = "wf-tool-secret-9f3a"
	body := strings.Repeat("节", 20000) + " " + secret
	got := redactAgentToolResult(body, domain.User{CodeBuddyAPIKey: secret})
	if utf8.RuneCountInString(got) < 20000 || strings.Contains(got, secret) || !strings.Contains(got, "[redacted]") {
		t.Fatalf("redacted result runes=%d", utf8.RuneCountInString(got))
	}
	for _, mode := range []string{"", AssistantApprovalAuto, AssistantApprovalAssisted} {
		prompt := assistantSystemPromptForMode(mode)
		if !strings.Contains(prompt, "读取完整定义再改") || !strings.Contains(prompt, "未提交的节点会被删除") {
			t.Fatalf("mode %q prompt missing the workflow document rule", mode)
		}
	}
}

func TestWorkflowGetReturnsFullDefinition(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user, ok := st.UserByLoginName("demo-owner")
	if !ok {
		t.Fatal("seed user")
	}
	const secret = "wf-tool-secret-9f3a"
	const marker = "TAIL_NODE_SCRIPT_MARKER"
	user.CodeBuddyAPIKey = secret
	script := strings.Repeat("env-prep phpstan\n", 800)
	wf := domain.WorkflowDefinition{
		ID: "wf-large", Name: "大 SOP", ProjectID: "platform", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "zg-index", Name: "索引", Type: "script", ScriptLanguage: "shell", Script: script},
			{ID: "design", Name: "设计", Type: "script", ScriptLanguage: "shell", Script: script},
			{ID: "env-prep", Name: "环境", Type: "script", ScriptLanguage: "shell", Script: script},
			{ID: "close", Name: "收尾", Type: "script", ScriptLanguage: "shell", Script: strings.Repeat("close-node\n", 400) + marker + " " + secret},
		},
		Edges: []domain.WorkflowEdge{
			{ID: "e1", Source: "zg-index", Target: "design", When: "success"},
			{ID: "e2", Source: "design", Target: "env-prep", When: "success"},
			{ID: "e3", Source: "env-prep", Target: "close", When: "success"},
		},
	}
	if err := st.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	api := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	h := &assistantHandlers{deps: &deps{repo: st}, api: api}
	got, err := h.callAssistantPlatform(context.Background(), user, "platform", assistantMCPCall{
		Server: "agentflow", Tool: "workflows_get", Arguments: map[string]any{"path": map[string]any{"id": wf.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFullWorkflow(t, got, secret, marker)

	listed, err := h.callAssistantPlatform(context.Background(), user, "platform", assistantMCPCall{Server: "agentflow", Tool: "workflows_list"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listed, marker) || strings.Contains(listed, secret) {
		t.Fatal("workflows_list lost the full SOP")
	}
	var listedFlows []domain.WorkflowDefinition
	if err := json.Unmarshal([]byte(listed), &listedFlows); err != nil {
		t.Fatalf("workflows_list json: %v", err)
	}
	if !workflowListHas(listedFlows, wf.ID) {
		t.Fatalf("workflows_list missing %s", wf.ID)
	}

	stored, ok := st.Workflow(wf.ID)
	if !ok {
		t.Fatal("stored workflow missing")
	}
	stored.Name = "大 SOP 第二版"
	if err := st.PutWorkflow(stored); err != nil {
		t.Fatal(err)
	}
	revs := st.Revisions("workflow", wf.ID)
	if len(revs) == 0 {
		t.Fatal("expected a workflow revision")
	}
	revText, err := h.callAssistantPlatform(context.Background(), user, "platform", assistantMCPCall{
		Server: "agentflow", Tool: "workflows_revision_get",
		Arguments: map[string]any{"path": map[string]any{"id": wf.ID, "revisionId": revs[0].ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var rev domain.DefinitionRevision
	if err := json.Unmarshal([]byte(revText), &rev); err != nil {
		t.Fatalf("revision json: %v\n%s", err, tail(revText))
	}
	assertFullWorkflow(t, string(rev.Payload), secret, marker)
	revList, err := h.callAssistantPlatform(context.Background(), user, "platform", assistantMCPCall{
		Server: "agentflow", Tool: "workflows_revisions_list",
		Arguments: map[string]any{"path": map[string]any{"id": wf.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(revList, marker) || strings.Contains(revList, secret) {
		t.Fatal("workflows_revisions_list lost the snapshot")
	}
	var revItems []domain.DefinitionRevision
	if err := json.Unmarshal([]byte(revList), &revItems); err != nil {
		t.Fatalf("workflows_revisions_list json: %v", err)
	}
	if len(revItems) == 0 {
		t.Fatal("workflows_revisions_list empty")
	}

	gw := &agentToolGateway{deps: &deps{repo: st, api: api}}
	session := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "platform", User: user}
	viaGateway, err := gw.dispatch(context.Background(), session, agentToolDescriptor{
		Name: "agentflow__workflows_get", Kind: "agentflow", UpstreamName: "workflows_get",
	}, map[string]any{"path": map[string]any{"id": wf.ID}})
	if err != nil {
		t.Fatal(err)
	}
	assertFullWorkflow(t, viaGateway, secret, marker)
}

func assertFullWorkflow(t *testing.T, text, secret, marker string) {
	t.Helper()
	if strings.Contains(text, secret) || !strings.Contains(text, "[redacted]") {
		t.Fatal("workflow document must still redact the caller secret")
	}
	var got domain.WorkflowDefinition
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("workflow json: %v\n%s", err, tail(text))
	}
	if len(got.Nodes) != 4 || len(got.Edges) != 3 {
		t.Fatalf("nodes=%d edges=%d", len(got.Nodes), len(got.Edges))
	}
	want := []string{"zg-index", "design", "env-prep", "close"}
	for i, id := range want {
		if got.Nodes[i].ID != id {
			t.Fatalf("node %d = %s, want %s", i, got.Nodes[i].ID, id)
		}
	}
	if !strings.Contains(got.Nodes[3].Script, marker) || strings.Contains(got.Nodes[3].Script, secret) {
		t.Fatalf("close script = %q", tail(got.Nodes[3].Script))
	}
}

func workflowListHas(items []domain.WorkflowDefinition, id string) bool {
	for _, item := range items {
		if item.ID == id && len(item.Nodes) == 4 && len(item.Edges) == 3 {
			return true
		}
	}
	return false
}

func tail(text string) string {
	runes := []rune(text)
	if len(runes) <= 80 {
		return text
	}
	return string(runes[len(runes)-80:])
}
