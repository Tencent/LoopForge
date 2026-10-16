package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/mcpclient"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestAgentToolMCPProtocolAndSecurity(t *testing.T) {
	resetAgentToolSessionsForTest()
	resetAgentToolCatalogCacheForTest()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user, ok := st.UserByLoginName("demo-owner")
	if !ok {
		t.Fatal("seed user")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize", "notifications/initialized", "ping":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[
				{"name":"proxy_execute_tool","description":"proxy","inputSchema":{"type":"object","properties":{"tool_name":{"type":"string"},"tool_args":{"type":"object"}}}},
				{"name":"comments_create","description":"write comment"},
				{"name":"stories_get","description":"get story","inputSchema":{"type":"object"}}
			]}}`))
		case "tools/call":
			if req.Params.Name == "proxy_execute_tool" {
				inner, _ := req.Params.Arguments["tool_name"].(string)
				if inner == "comments_create" {
					_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"should not happen"}],"isError":false}}`))
					return
				}
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"story ok Authorization: Bearer leaked-token"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		}
	}))
	t.Cleanup(upstream.Close)
	if err := st.PutMCPServer(domain.MCPServerDefinition{ID: "tapd-p", Name: "TAPD", ProjectID: "platform", Scope: "project", MarketID: "70", Transport: "http", URL: upstream.URL}); err != nil {
		t.Fatal(err)
	}
	handler := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "user", "pass")
	session := &AgentToolSession{
		UserID:     user.ID,
		ProjectID:  "platform",
		Mode:       AgentToolClarify,
		ExpiresAt:  time.Now().Add(time.Minute),
		MCPServers: []domain.MCPServerDefinition{{ID: "tapd-p", Name: "TAPD", ProjectID: "platform", Scope: "project", MarketID: "70", Transport: "http", URL: upstream.URL}},
		User:       user,
		Final:      make(chan AgentTerminalResult, 1),
	}
	if err := agentToolSessions.create(session); err != nil {
		t.Fatal(err)
	}
	call := func(token, body string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/internal/agent-tools/"+token+"/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	status, _ := call("deadbeef", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("invalid token status=%d", status)
	}

	status, body := call(session.Token, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if status != http.StatusOK || !strings.Contains(body, agentToolSessionMCPName) {
		t.Fatalf("initialize: %d %s", status, body)
	}
	if strings.Contains(body, "不要输出 JSON") || strings.Contains(body, "mcpCalls") {
		t.Fatalf("initialize instructions still mention JSON protocol: %s", body)
	}
	status, body = call(session.Token, `{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if status != http.StatusOK {
		t.Fatalf("ping: %d %s", status, body)
	}
	status, body = call(session.Token, `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`)
	if status != http.StatusOK {
		t.Fatalf("tools/list: %d %s", status, body)
	}
	if strings.Contains(body, "comments_create") || strings.Contains(body, "issues_save") || strings.Contains(body, "submit_assistant_reply") {
		t.Fatalf("write or assistant tools leaked into clarify catalog: %s", body)
	}
	if !strings.Contains(body, "tapd__proxy_execute_tool") || !strings.Contains(body, "submit_clarification") {
		t.Fatalf("expected namespaced read + terminal tools: %s", body)
	}

	status, body = call(session.Token, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"tapd__proxy_execute_tool","arguments":{"tool_name":"comments_create","tool_args":{"description":"x"}}}}`)
	if status != http.StatusOK || !strings.Contains(body, "拒绝") && !strings.Contains(body, "不允许") && !strings.Contains(body, "只允许") && !strings.Contains(body, "allow") && !strings.Contains(body, "终态") && !strings.Contains(strings.ToLower(body), "error") {
		t.Fatalf("write tapd call should fail: %d %s", status, body)
	}

	status, body = call(session.Token, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"mcp__agentflow_session__tapd__stories_get","arguments":{"id":"1"}}}`)
	if status != http.StatusOK || strings.Contains(body, "leaked-token") {
		t.Fatalf("read call/redact: %d %s", status, body)
	}

	status, body = call(session.Token, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"submit_clarification","arguments":{"summary":"ready now","questions":[],"ready":true}}}`)
	if status != http.StatusOK || !strings.Contains(body, "提交成功") {
		t.Fatalf("submit: %d %s", status, body)
	}
	status, body = call(session.Token, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"tapd__stories_get","arguments":{}}}`)
	if status != http.StatusOK || !strings.Contains(body, "终态已提交") {
		t.Fatalf("post-terminal call: %d %s", status, body)
	}
	got, ok := session.takeFinal()
	if !ok || got.Clarification == nil || !got.Clarification.Ready {
		t.Fatalf("captured terminal: %+v", got)
	}
}

func TestAgentToolMCPDoesNotUseBasicAuthOrCookies(t *testing.T) {
	resetAgentToolSessionsForTest()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user, _ := st.UserByLoginName("demo-owner")
	handler := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "user", "pass")
	session := &AgentToolSession{UserID: user.ID, ProjectID: "platform", Mode: AgentToolAssistant, ExpiresAt: time.Now().Add(time.Minute), User: user, Final: make(chan AgentTerminalResult, 1)}
	if err := agentToolSessions.create(session); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/agent-tools/"+session.Token+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("session token must skip basic auth, status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAssistantCatalogExposesReadonlyPlatformTools(t *testing.T) {
	resetAgentToolSessionsForTest()
	gw := &agentToolGateway{}
	session := &AgentToolSession{Mode: AgentToolAssistant, ExpiresAt: time.Now().Add(time.Minute), Final: make(chan AgentTerminalResult, 1)}
	catalog := gw.buildCatalog(nil, session)
	var names []string
	for _, item := range catalog {
		names = append(names, item.Name)
		if item.Kind == "agentflow" && strings.Contains(item.UpstreamName, "save") {
			t.Fatalf("write platform tool in assistant catalog: %s", item.Name)
		}
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "agentflow__issues_list") || !strings.Contains(joined, "submit_assistant_reply") || strings.Contains(joined, "submit_clarification") {
		t.Fatalf("assistant catalog=%s", joined)
	}
}

func TestScheduledCatalogExposesWritePlatformToolsWithoutSubmit(t *testing.T) {
	resetAgentToolSessionsForTest()
	gw := &agentToolGateway{}
	deferred := &AgentToolSession{Mode: AgentToolScheduled, ExecMode: assistantExecMode{AutoWrite: true, DeferSOP: true}, ExpiresAt: time.Now().Add(time.Minute), Final: make(chan AgentTerminalResult, 1)}
	catalog := gw.buildCatalog(nil, deferred)
	joined := catalogToolNames(catalog)
	if !strings.Contains(joined, "agentflow__issues_save") || !strings.Contains(joined, "agentflow__issues_list") {
		t.Fatalf("scheduled catalog should expose platform writes: %s", joined)
	}
	if strings.Contains(joined, "issues_dispatch") || strings.Contains(joined, "runs_start") || strings.Contains(joined, "workflows_publish") {
		t.Fatalf("DeferSOP tools leaked into scheduled catalog: %s", joined)
	}
	if strings.Contains(joined, "submit_assistant_reply") || strings.Contains(joined, "submit_clarification") || strings.Contains(joined, "submit_scheduled") {
		t.Fatalf("scheduled catalog must not include terminal submit tools: %s", joined)
	}
	open := &AgentToolSession{Mode: AgentToolScheduled, ExecMode: assistantExecMode{AutoWrite: true}, ExpiresAt: time.Now().Add(time.Minute), Final: make(chan AgentTerminalResult, 1)}
	openNames := catalogToolNames(gw.buildCatalog(nil, open))
	if !strings.Contains(openNames, "agentflow__issues_dispatch") {
		t.Fatalf("non-defer scheduled catalog should allow dispatch: %s", openNames)
	}
}

func catalogToolNames(catalog []agentToolDescriptor) string {
	names := make([]string, 0, len(catalog))
	for _, item := range catalog {
		names = append(names, item.Name)
	}
	return strings.Join(names, ",")
}

func TestScheduledAgentToolWritesPlatformAndDefersSOP(t *testing.T) {
	resetAgentToolSessionsForTest()
	resetAgentToolCatalogCacheForTest()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user, ok := st.UserByLoginName("demo-owner")
	if !ok {
		t.Fatal("seed user")
	}
	if err := st.PutIssue(domain.Issue{
		ID: "iss-sched", ProjectID: "platform", Title: "keep", Status: "backlog", Source: "tapd",
		ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "303"},
	}); err != nil {
		t.Fatal(err)
	}
	handler := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	session := &AgentToolSession{
		UserID:    user.ID,
		ProjectID: "platform",
		Mode:      AgentToolScheduled,
		ExecMode:  assistantExecMode{AutoWrite: true, DeferSOP: true},
		ExpiresAt: time.Now().Add(time.Minute),
		User:      user,
		Final:     make(chan AgentTerminalResult, 1),
	}
	if err := agentToolSessions.create(session); err != nil {
		t.Fatal(err)
	}
	call := func(body string) string {
		req := httptest.NewRequest(http.MethodPost, "/internal/agent-tools/"+session.Token+"/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	body := call(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"agentflow__issues_save","arguments":{"path":{"id":"iss-sched"},"body":{"id":"iss-sched","automationState":"ignored"}}}}`)
	if strings.Contains(body, `"isError":true`) {
		t.Fatalf("scheduled issues_save should succeed: %s", body)
	}
	got, ok := st.Issue("iss-sched")
	if !ok || got.Title != "keep" || got.AutomationState != "ignored" {
		t.Fatalf("native scheduled write did not persist: %+v", got)
	}
	denied := call(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"agentflow__issues_dispatch","arguments":{"path":{"id":"iss-sched"}}}}`)
	if !strings.Contains(denied, "提示词执行结束后") && !strings.Contains(denied, "不在当前场景") && !strings.Contains(denied, "unknown") {
		t.Fatalf("scheduled dispatch should be blocked: %s", denied)
	}
	submit := call(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"submit_assistant_reply","arguments":{"message":"done","drafts":[]}}}`)
	if !strings.Contains(submit, "不允许") && !strings.Contains(submit, "禁止") && !strings.Contains(submit, "不在当前场景") && !strings.Contains(strings.ToLower(submit), "error") {
		t.Fatalf("scheduled must reject assistant submit tool: %s", submit)
	}
}

func TestScheduledAgentToolAllowsExternalTAPDWrite(t *testing.T) {
	resetAgentToolSessionsForTest()
	resetAgentToolCatalogCacheForTest()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user, ok := st.UserByLoginName("demo-owner")
	if !ok {
		t.Fatal("seed user")
	}
	var inner string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"proxy_execute_tool"}]}}`))
		case "tools/call":
			inner, _ = req.Params.Arguments["tool_name"].(string)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"updated"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		}
	}))
	t.Cleanup(upstream.Close)
	server := domain.MCPServerDefinition{ID: "tapd-p", Name: "TAPD", ProjectID: "platform", Scope: "project", MarketID: "70", Transport: "http", URL: upstream.URL}
	if err := st.PutMCPServer(server); err != nil {
		t.Fatal(err)
	}
	handler := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	session := &AgentToolSession{
		UserID: user.ID, ProjectID: "platform", Mode: AgentToolScheduled,
		ExecMode:  assistantExecMode{AutoWrite: true, DeferSOP: true},
		ExpiresAt: time.Now().Add(time.Minute), MCPServers: []domain.MCPServerDefinition{server},
		User: user, Final: make(chan AgentTerminalResult, 1),
	}
	if err := agentToolSessions.create(session); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/agent-tools/"+session.Token+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"tapd__proxy_execute_tool","arguments":{"tool_name":"stories_update","tool_args":{"workspace_id":"1","id":"2","name":"ok"}}}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "只允许只读") || strings.Contains(rec.Body.String(), `"isError":true`) {
		t.Fatalf("scheduled TAPD write should succeed: %d %s", rec.Code, rec.Body.String())
	}
	if inner != "stories_update" {
		t.Fatalf("upstream inner tool = %q", inner)
	}
}

func TestMCPClientPreservesInputSchema(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "tools/list" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"stories_get","description":"get","inputSchema":{"type":"object","required":["id"]},"annotations":{"readOnlyHint":true}}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	t.Cleanup(srv.Close)
	tools, err := mcpclient.New(srv.URL, nil).ListTools()
	if err != nil || len(tools) != 1 {
		t.Fatalf("list: %v %+v", err, tools)
	}
	if !bytes.Contains(tools[0].InputSchema, []byte(`"required"`)) || !bytes.Contains(tools[0].Annotations, []byte("readOnlyHint")) {
		t.Fatalf("schema not preserved: %+v", tools[0])
	}
}
