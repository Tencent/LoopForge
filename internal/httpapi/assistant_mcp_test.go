package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
	"github.com/gin-gonic/gin"
)

func TestAssistantMCPStaysInProjectAndRejectsStdio(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user, ok := st.UserByLoginName("demo-owner")
	if !ok {
		t.Fatal("seed user")
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize", "notifications/initialized":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"stories_list","description":"list TAPD stories"}]}}`))
		case "tools/call":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"story-1 待办"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		}
	}))
	t.Cleanup(gateway.Close)
	if err := st.PutMCPServer(domain.MCPServerDefinition{ID: "tapd-p", Name: "TAPD", ProjectID: "platform", Scope: "project", Transport: "http", URL: gateway.URL, AuthType: ""}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutMCPServer(domain.MCPServerDefinition{ID: "tapd-other", Name: "Other TAPD", ProjectID: "other", Scope: "project", Transport: "http", URL: gateway.URL}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutMCPServer(domain.MCPServerDefinition{ID: "stdio-p", Name: "Local", ProjectID: "platform", Scope: "project", Transport: "stdio", Command: "echo"}); err != nil {
		t.Fatal(err)
	}
	h := &assistantHandlers{deps: &deps{repo: st}}
	if _, ok := h.lookupAssistantMCP("platform", user.ID, "tapd-other"); ok {
		t.Fatal("other project MCP must stay hidden")
	}
	got, err := h.callAssistantMCP(user, "platform", assistantMCPCall{Server: "tapd-p", Tool: "tools/list"})
	if err != nil || !strings.Contains(got, "stories_list") {
		t.Fatalf("list tools: %s %v", got, err)
	}
	called, err := h.callAssistantMCP(user, "platform", assistantMCPCall{Server: "tapd-p", Tool: "stories_list"})
	if err != nil || !strings.Contains(called, "story-1") {
		t.Fatalf("call tool: %s %v", called, err)
	}
	if _, err := h.callAssistantMCP(user, "platform", assistantMCPCall{Server: "stdio-p", Tool: "run"}); err == nil || !strings.Contains(err.Error(), "stdio") {
		t.Fatalf("stdio MCP should be rejected: %v", err)
	}
	if _, err := h.callAssistantMCP(user, "", assistantMCPCall{Server: "tapd-p"}); err == nil {
		t.Fatal("empty project must not call MCP")
	}
}

func TestAssistantChatRecoversWithoutJSONFollowup(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "mcp-user", CodeBuddyAPIKey: "alice-secret"}); err != nil {
		t.Fatal(err)
	}
	user, ok := st.UserByLoginName("mcp-user")
	if !ok {
		t.Fatal("seed user")
	}
	project, _ := st.Project("platform")
	project.Members = append(project.Members, domain.ProjectMember{UserID: user.ID, Role: "member"})
	if err := st.PutProject(project); err != nil {
		t.Fatal(err)
	}
	rounds := 0
	h := &assistantHandlers{deps: &deps{repo: st}, generate: func(ctx context.Context, prompt string, u domain.User) (string, error) {
		rounds++
		if strings.Contains(prompt, "你是 AgentFlow") {
			t.Fatal("system prompt must stay on --system-prompt, not -p")
		}
		if rounds > 1 && !strings.Contains(prompt, "submit_assistant_reply") {
			t.Fatalf("retry should ask for submit_assistant_reply: %s", prompt)
		}
		return `{"message":"已查到待办","drafts":[]}`, nil
	}}
	req := httptest.NewRequest("POST", "/api/assistant/chat", strings.NewReader(`{"projectId":"platform","messages":[{"role":"user","content":"用 TAPD 看待办"}]}`))
	req = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, domainUserToAuth(user)))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	h.chat(c)
	if rec.Code != 200 {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	var reply assistantReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || reply.Message != "已查到待办" {
		t.Fatalf("%s %v", rec.Body.String(), err)
	}
	if rounds != 1 {
		t.Fatalf("expected conversational answer without terminal retry, got %d rounds", rounds)
	}
}

func TestNormalizeAssistantTaskRequiresProject(t *testing.T) {
	if _, err := normalizeAssistantTask(assistantTask{Name: "巡检", Prompt: "看一下", IntervalSec: 60}, assistantTask{}); err == nil {
		t.Fatal("project id required")
	}
}

func TestAssistantPlatformReadsCurrentProjectIssues(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user, ok := st.UserByLoginName("demo-owner")
	if !ok {
		t.Fatal("seed user")
	}
	if err := st.PutProject(domain.Project{ID: "other", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutIssue(domain.Issue{ID: "iss-p", ProjectID: "platform", Title: "当前项目需求", Status: "todo"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutIssue(domain.Issue{ID: "iss-other", ProjectID: "other", Title: "其他项目需求", Status: "todo"}); err != nil {
		t.Fatal(err)
	}
	api := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	h := &assistantHandlers{deps: &deps{repo: st}, api: api}
	listed, err := h.callAssistantPlatform(context.Background(), user, "platform", assistantMCPCall{Server: "agentflow", Tool: "issues_list"})
	if err != nil || !strings.Contains(listed, "当前项目需求") {
		t.Fatalf("issues_list: %s %v", listed, err)
	}
	if strings.Contains(listed, "其他项目需求") {
		t.Fatal("issues_list must stay in the current project")
	}
	if !strings.Contains(listed, `"items"`) || !strings.Contains(listed, `"hasMore"`) {
		t.Fatalf("issues_list should paginate: %s", listed)
	}
	if _, err := h.callAssistantPlatform(context.Background(), user, "platform", assistantMCPCall{Tool: "issues_get", Arguments: map[string]any{"path": map[string]any{"id": "iss-other"}}}); err == nil || !strings.Contains(err.Error(), "当前项目") {
		t.Fatalf("foreign issues_get should be rejected: %v", err)
	}
	if _, err := h.callAssistantPlatform(context.Background(), user, "platform", assistantMCPCall{Tool: "issues_save"}); err == nil || !strings.Contains(err.Error(), "drafts") {
		t.Fatalf("writes must stay drafts: %v", err)
	}
	tools, err := h.callAssistantPlatform(context.Background(), user, "platform", assistantMCPCall{Server: "agentflow", Tool: "tools/list"})
	if err != nil || !strings.Contains(tools, "assistant_tasks_save") || !strings.Contains(tools, "issues_list") {
		t.Fatalf("platform tools/list: %s %v", tools, err)
	}
}

func TestAssistantScheduledWritesApplyIssuePatches(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user, ok := st.UserByLoginName("demo-owner")
	if !ok {
		t.Fatal("seed user")
	}
	user.CodeBuddyAPIKey = "scheduled-secret"
	if err := st.PutUser(user); err != nil {
		t.Fatal(err)
	}
	if err := st.PutIssue(domain.Issue{
		ID: "iss-triage", ProjectID: "platform", Title: "keep title", Status: "backlog", Source: "tapd",
		ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "101"},
	}); err != nil {
		t.Fatal(err)
	}
	api := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	h := &assistantHandlers{deps: &deps{repo: st}, api: api}
	if _, err := h.callAssistantPlatform(context.Background(), user, "platform", assistantMCPCall{Tool: "issues_save"}); err == nil {
		t.Fatal("chat writes must stay drafts")
	}
	ctx := withAssistantExecMode(context.Background(), assistantExecMode{AutoWrite: true})
	if _, err := h.callAssistantPlatform(ctx, user, "platform", assistantMCPCall{Tool: "issues_save", Arguments: map[string]any{"body": map[string]any{"id": "iss-triage", "automationState": "ignored"}}}); err != nil {
		t.Fatalf("scheduled writes should execute: %v", err)
	}
	if _, err := h.callAssistantPlatform(ctx, user, "platform", assistantMCPCall{
		Tool: "issues_save",
		Path: map[string]any{"id": "iss-triage"},
		Body: map[string]any{"status": "todo"},
	}); err != nil {
		t.Fatalf("path.id issues_save should merge: %v", err)
	}
	got, ok := st.Issue("iss-triage")
	if !ok || got.Title != "keep title" || got.AutomationState != "ignored" || got.Status != "todo" {
		t.Fatalf("partial issues_save should merge: %+v", got)
	}
	deferCtx := withAssistantExecMode(context.Background(), assistantExecMode{AutoWrite: true, DeferSOP: true})
	if _, err := h.callAssistantPlatform(deferCtx, user, "platform", assistantMCPCall{Tool: "issues_dispatch", Arguments: map[string]any{"path": map[string]any{"id": "iss-triage"}}}); err == nil || !strings.Contains(err.Error(), "提示词执行结束后") {
		t.Fatalf("combined job must defer dispatch: %v", err)
	}
}

func TestScheduledTaskKeepsPlaintextAndDoesNotParseJSON(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "task-native", CodeBuddyAPIKey: "task-secret"}); err != nil {
		t.Fatal(err)
	}
	user, ok := st.UserByLoginName("task-native")
	if !ok {
		t.Fatal("user")
	}
	if err := st.PutIssue(domain.Issue{
		ID: "iss-native", ProjectID: "platform", Title: "keep", Status: "backlog", Source: "tapd",
	}); err != nil {
		t.Fatal(err)
	}
	api := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	h := &assistantHandlers{deps: &deps{repo: st}, api: api, generate: func(ctx context.Context, prompt string, u domain.User) (string, error) {
		if strings.Contains(prompt, "drafts=[]") || strings.Contains(prompt, "走 mcpCalls") || strings.Contains(prompt, "只用 message") {
			t.Fatalf("native scheduled prompt should not mention JSON protocol: %s", prompt)
		}
		if !strings.Contains(prompt, "agentflow__") && !strings.Contains(prompt, "直接调用") {
			t.Fatalf("native scheduled prompt should tell the model to call platform MCP: %s", prompt)
		}
		return `{"message":"should not parse","drafts":[{"tool":"issues_save","body":{"id":"iss-native","title":"hijacked"}}]}`, nil
	}}
	task := assistantTask{Name: "提示词", Prompt: "标 ignored", IntervalSec: 60, Enabled: true, ProjectID: "platform"}
	if err := h.executeAssistantTask(context.Background(), user, &task); err != nil {
		t.Fatal(err)
	}
	if task.LastStatus != "ok" || !strings.Contains(task.LastMessage, "should not parse") {
		t.Fatalf("native scheduled should keep plaintext result: %+v", task)
	}
	got, ok := st.Issue("iss-native")
	if !ok || got.Title != "keep" {
		t.Fatalf("native scheduled must not apply JSON drafts: %+v", got)
	}
}
