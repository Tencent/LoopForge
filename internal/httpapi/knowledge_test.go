package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/knowledge"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

// fakeKB is a stateless mock MCP exposing both read-only and write tools.
type fakeKB struct {
	mu    stdsync.Mutex
	calls []map[string]any
	auth  []string
}

func (f *fakeKB) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     any             `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	text := func(v any) map[string]any {
		raw, _ := json.Marshal(v)
		return map[string]any{"content": []map[string]any{{"type": "text", "text": string(raw)}}}
	}
	var result any = map[string]any{}
	switch req.Method {
	case "initialize":
		result = map[string]any{"protocolVersion": "2024-11-05"}
	case "tools/list":
		result = map[string]any{"tools": []map[string]any{
			{"name": "kb_search", "description": "kb_search", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}},
			{"name": "kb_fetch", "description": "kb_fetch", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}},
			{"name": "kb_list_sources", "description": "kb_list_sources", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}},
			{"name": "kb_write_document", "description": "kb_write_document", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true}},
		}}
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		f.mu.Lock()
		f.calls = append(f.calls, map[string]any{"name": p.Name, "args": p.Arguments})
		f.mu.Unlock()
		switch p.Name {
		case "kb_list_sources":
			result = text(map[string]any{"sources": []map[string]any{{"id": "a", "name": "研发规范", "status": "ready", "doc_count": 2}, {"id": "b", "name": "私有草稿", "status": "ready", "doc_count": 1}}})
		case "kb_search":
			result = text(map[string]any{"query": p.Arguments["query"], "mode": "hybrid", "results": []map[string]any{{"source": "a", "source_name": "研发规范", "path": "x.md", "file_name": "x.md", "start_line": 1, "end_line": 3, "snippet": "hit"}}})
		case "kb_fetch":
			result = text(map[string]any{"source": p.Arguments["source"], "path": p.Arguments["path"], "lines": []map[string]any{{"number": 1, "text": "# X"}}})
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

func (f *fakeKB) lastCall(name string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i]["name"] == name {
			args, _ := f.calls[i]["args"].(map[string]any)
			return args
		}
	}
	return nil
}

func newKnowledgeTestServer(t *testing.T) (*repo.Store, http.Handler, *fakeKB, string) {
	t.Helper()
	t.Setenv("AGENTFLOW_SESSION_SECRET", "knowledge-http-test-secret")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	kb := &fakeKB{}
	upstream := httptest.NewServer(kb)
	t.Cleanup(upstream.Close)
	handler := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	return s, handler, kb, upstream.URL + "/mcp"
}

func doJSON(t *testing.T, h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func doJSONAsUser(t *testing.T, h http.Handler, u domain.User, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	client := auth.NewClientFromEnv()
	cookie, err := client.SignUser(&auth.User{ID: u.ID, LoginName: u.LoginName, AuthTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: cookie})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func rpcText(t *testing.T, rec *httptest.ResponseRecorder) (string, bool, string) {
	t.Helper()
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool             `json:"isError"`
			Tools   []map[string]any `json:"tools"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode rpc: %v body=%s", err, rec.Body.String())
	}
	if resp.Error != nil {
		return "", true, resp.Error.Message
	}
	text := ""
	if len(resp.Result.Content) > 0 {
		text = resp.Result.Content[0].Text
	}
	return text, resp.Result.IsError, ""
}

func TestKnowledgeConnectorCRUDAndUserIsolation(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	s, h, _, mcpURL := newKnowledgeTestServer(t)

	alice := domain.User{ID: 101, LoginName: "alice"}
	bob := domain.User{ID: 202, LoginName: "bob"}
	_ = s.PutUser(alice)
	_ = s.PutUser(bob)
	_ = s.PutProject(domain.Project{ID: "p1", Name: "P1", OwnerID: alice.ID, Members: []domain.ProjectMember{{UserID: bob.ID, Role: "member"}}})
	_ = s.PutAgent(domain.AgentDefinition{ID: "a1", Name: "A1", Model: "mock"})
	_ = s.PutWorkflow(domain.WorkflowDefinition{ID: "wf-1", Name: "WF", ProjectID: "p1", Nodes: []domain.WorkflowNode{{ID: "n1", Name: "N1", Type: "agent", AgentID: "a1"}}})

	// Unauthenticated access rejected when auth is required.
	if rec := doJSON(t, h, http.MethodGet, "/api/knowledge/connectors", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list status = %d, want 401", rec.Code)
	}

	// Connector types metadata returned.
	typesRec := doJSONAsUser(t, h, alice, http.MethodGet, "/api/knowledge/connector-types", "")
	if typesRec.Code != http.StatusOK || !strings.Contains(typesRec.Body.String(), `"weknora"`) || !strings.Contains(typesRec.Body.String(), `"mcp"`) {
		t.Fatalf("connector-types failed: %d %s", typesRec.Code, typesRec.Body.String())
	}

	// Alice creates an MCP connector with a secret token.
	createRec := doJSONAsUser(t, h, alice, http.MethodPost, "/api/knowledge/connectors", `{"name":"Alice Docs","type":"mcp","endpoint":"`+mcpURL+`","token":"alice-top-secret"}`)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("createConnector status = %d, body=%s", createRec.Code, createRec.Body.String())
	}
	if strings.Contains(createRec.Body.String(), "alice-top-secret") || strings.Contains(createRec.Body.String(), "SecretEnc") {
		t.Fatalf("secret leaked in create response: %s", createRec.Body.String())
	}
	var aliceConn domain.KnowledgeConnector
	_ = json.Unmarshal(createRec.Body.Bytes(), &aliceConn)
	if aliceConn.ID == "" || !aliceConn.SecretConfigured || !aliceConn.Enabled {
		t.Fatalf("unexpected created connector: %+v", aliceConn)
	}

	// Bob cannot list, inspect, modify, check, list sources, or delete Alice's connector.
	if bobList := doJSONAsUser(t, h, bob, http.MethodGet, "/api/knowledge/connectors", ""); bobList.Code != 200 || strings.Contains(bobList.Body.String(), aliceConn.ID) {
		t.Fatalf("bob saw alice connector: %s", bobList.Body.String())
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/knowledge/connectors/" + aliceConn.ID, ""},
		{http.MethodPut, "/api/knowledge/connectors/" + aliceConn.ID, `{"name":"hijack"}`},
		{http.MethodDelete, "/api/knowledge/connectors/" + aliceConn.ID, ""},
		{http.MethodGet, "/api/knowledge/connectors/" + aliceConn.ID + "/sources", ""},
	} {
		rec := doJSONAsUser(t, h, bob, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("cross-user %s %s = %d, want 404 (%s)", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
	if rec := doJSONAsUser(t, h, bob, http.MethodPost, "/api/knowledge/reference-check", `{"knowledgeRefs":[{"connectorId":"`+aliceConn.ID+`","mode":"all"}]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bob reference-check for alice connector = %d, want 400", rec.Code)
	}
	if rec := doJSONAsUser(t, h, bob, http.MethodPost, "/api/runs", `{"workflowId":"wf-1","projectId":"p1","input":"hi","knowledgeRefs":[{"connectorId":"`+aliceConn.ID+`","mode":"all"}]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bob POST /api/runs with alice connector = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}

	// Alice checks, inspects sources, updates name/enabled without wiping secret, and binds reference.
	chk := doJSONAsUser(t, h, alice, http.MethodPost, "/api/knowledge/connectors/"+aliceConn.ID+"/check", `{}`)
	if chk.Code != http.StatusOK || !strings.Contains(chk.Body.String(), `"ok":true`) {
		t.Fatalf("check failed: %d %s", chk.Code, chk.Body.String())
	}
	srcs := doJSONAsUser(t, h, alice, http.MethodGet, "/api/knowledge/connectors/"+aliceConn.ID+"/sources", "")
	if srcs.Code != http.StatusOK || !strings.Contains(srcs.Body.String(), "研发规范") {
		t.Fatalf("sources failed: %d %s", srcs.Code, srcs.Body.String())
	}
	upd := doJSONAsUser(t, h, alice, http.MethodPut, "/api/knowledge/connectors/"+aliceConn.ID, `{"name":"Alice Docs v2"}`)
	if upd.Code != http.StatusOK || strings.Contains(upd.Body.String(), "alice-top-secret") {
		t.Fatalf("update failed: %d %s", upd.Code, upd.Body.String())
	}
	var updated domain.KnowledgeConnector
	_ = json.Unmarshal(upd.Body.Bytes(), &updated)
	if !updated.SecretConfigured || updated.Name != "Alice Docs v2" {
		t.Fatalf("update wiped secret or failed name update: %+v", updated)
	}

	// Valid allowlist check succeeds (204), invalid sub-source fails (400).
	if rec := doJSONAsUser(t, h, alice, http.MethodPost, "/api/knowledge/reference-check", `{"knowledgeRefs":[{"connectorId":"`+aliceConn.ID+`","mode":"allowlist","sources":["a"]}]}`); rec.Code != http.StatusNoContent {
		t.Fatalf("valid allowlist check = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := doJSONAsUser(t, h, alice, http.MethodPost, "/api/knowledge/reference-check", `{"knowledgeRefs":[{"connectorId":"`+aliceConn.ID+`","mode":"allowlist","sources":["missing-kb"]}]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid sub-source check = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}

	// Chat session knowledge rejects cross-user connector and accepts Alice's own connector.
	sess := domain.AssistantSession{ID: "sess-iso", UserID: bob.ID, ProjectID: "p1", Title: "Bob Chat"}
	_ = s.PutAssistantSession(bob.ID, sess)
	if rec := doJSONAsUser(t, h, bob, http.MethodPut, "/api/assistant/sessions/sess-iso/knowledge", `{"knowledgeRefs":[{"connectorId":"`+aliceConn.ID+`","mode":"all"}]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bob sessionKnowledge accepted alice connector: %d %s", rec.Code, rec.Body.String())
	}

	// Alice launches a manual SOP run with run-level KnowledgeRefs override.
	runRec := doJSONAsUser(t, h, alice, http.MethodPost, "/api/runs", `{"workflowId":"wf-1","projectId":"p1","input":"run with kb","knowledgeRefs":[{"connectorId":"`+aliceConn.ID+`","mode":"allowlist","sources":["a"]}]}`)
	if runRec.Code != http.StatusCreated {
		t.Fatalf("alice POST /api/runs = %d (%s)", runRec.Code, runRec.Body.String())
	}
	var createdRun domain.Run
	_ = json.Unmarshal(runRec.Body.Bytes(), &createdRun)
	if createdRun.ExecutionContext.KnowledgeRefs == nil || len(*createdRun.ExecutionContext.KnowledgeRefs) != 1 || (*createdRun.ExecutionContext.KnowledgeRefs)[0].ConnectorID != aliceConn.ID {
		t.Fatalf("run ExecutionContext.KnowledgeRefs missing override: %+v", createdRun.ExecutionContext)
	}
	// Cancel and wait for async execution to stop touching t.TempDir() before cleanup runs.
	_ = doJSONAsUser(t, h, alice, http.MethodPost, "/api/runs/"+createdRun.ID+"/cancel", `{}`)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if latest, ok := s.Run(createdRun.ID); ok && (latest.Status == "succeeded" || latest.Status == "failed" || latest.Status == "cancelled") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestKnowledgeProxyIsReadOnlyAndUserConnectorScoped(t *testing.T) {
	s, h, kb, mcpURL := newKnowledgeTestServer(t)

	svc := knowledge.NewService(s, nil)
	conn, err := svc.CreateConnector(context.Background(), 42, knowledge.ConnectorInput{
		Name:     "Dev MCP",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: mcpURL,
		Secret:   domain.KnowledgeConnectorSecret{Token: "upstream-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}

	call := func(token, body string) *httptest.ResponseRecorder {
		return doJSON(t, h, http.MethodPost, knowledge.ProxyPath, body, map[string]string{"Authorization": "Bearer " + token})
	}
	if rec := call("upstream-secret", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("upstream token must not authenticate proxy: %d", rec.Code)
	}

	ref := domain.KnowledgeRef{ConnectorID: conn.ID, Mode: "allowlist", Sources: []string{"a"}}
	// Proxy tokens are only valid while the claimed run node is executing.
	_ = s.PutRun(domain.Run{
		ID:               "run-proxy-iso",
		Status:           "running",
		ExecutionContext: domain.TelemetryContext{ProjectID: "p1", ResponsibleUserID: 42},
		Nodes:            map[string]*domain.NodeRun{"n1": {NodeID: "n1", Status: "running", Attempt: 1}},
	})
	token, err := knowledge.SignProxyToken(knowledge.ProxyClaims{RunID: "run-proxy-iso", NodeID: "n1", Attempt: 1, ProjectID: "p1", UserID: 42, AgentID: "dev", Ref: &ref}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// A token without run/node identity must be rejected outright.
	bareToken, _ := knowledge.SignProxyToken(knowledge.ProxyClaims{ProjectID: "p1", UserID: 42, AgentID: "dev", Ref: &ref}, time.Now())
	if rec := call(bareToken, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("proxy token without run/node identity = %d, want 403", rec.Code)
	}

	rec := call(token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "kb_write_document") || !strings.Contains(rec.Body.String(), "kb_search") {
		t.Fatalf("proxy exposed write tool or missed read tool: %d %s", rec.Code, rec.Body.String())
	}
	if text, isErr, _ := rpcText(t, call(token, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"kb_write_document","arguments":{"source":"a","path":"x.md","content":"bad"}}}`)); !isErr || !strings.Contains(text, "未开放只读工具") {
		t.Fatalf("write call was not blocked: isErr=%v text=%s", isErr, text)
	}
	text, isErr, rpcErr := rpcText(t, call(token, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"kb_search","arguments":{"query":"dns"}}}`))
	if isErr || rpcErr != "" {
		t.Fatalf("kb_search failed: text=%s rpcErr=%s", text, rpcErr)
	}
	if !strings.Contains(text, "[[knowledge:") || !strings.Contains(text, "研发规范") {
		t.Fatalf("expected citation suffix and source name, got: %s", text)
	}
	if got := kb.lastCall("kb_search")["sources"]; got == nil {
		t.Fatalf("expected allowlist sources injected into kb_search args, got %+v", kb.lastCall("kb_search"))
	}

	// Token forged for another userId cannot pass the run-ownership guard.
	wrongUserToken, _ := knowledge.SignProxyToken(knowledge.ProxyClaims{RunID: "run-proxy-iso", NodeID: "n1", Attempt: 1, ProjectID: "p1", UserID: 99, AgentID: "dev", Ref: &ref}, time.Now())
	if wrongRec := call(wrongUserToken, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"kb_search","arguments":{"query":"dns"}}}`); wrongRec.Code != http.StatusForbidden {
		t.Fatalf("cross-user proxy token = %d, want 403 (%s)", wrongRec.Code, wrongRec.Body.String())
	}

	// Verify usage counters recorded the successful proxy call (and ignored the failed cross-user lookup).
	dev42 := domain.User{ID: 42, LoginName: "dev42"}
	_ = s.PutUser(dev42)
	usageRec := doJSONAsUser(t, h, dev42, http.MethodGet, "/api/knowledge/usage", "")
	if usageRec.Code != http.StatusOK {
		t.Fatalf("GET /api/knowledge/usage failed: %d %s", usageRec.Code, usageRec.Body.String())
	}
	var summary domain.KnowledgeUsageSummary
	if err := json.Unmarshal(usageRec.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.TotalCalls != 1 || len(summary.Connectors) != 1 || summary.Connectors[0].ConnectorID != conn.ID || summary.Connectors[0].CallCount != 1 {
		t.Fatalf("expected 1 recorded connector call, got %+v", summary)
	}
	if len(summary.Documents) != 1 || summary.Documents[0].DocumentID != "x.md" || summary.Documents[0].CallCount != 1 {
		t.Fatalf("expected 1 recorded document call for x.md, got %+v", summary.Documents)
	}

	// Verify caller events endpoint returns the SOP caller metadata and query.
	eventsRec := doJSONAsUser(t, h, dev42, http.MethodGet, "/api/knowledge/usage/events?callerKind=sop", "")
	if eventsRec.Code != http.StatusOK {
		t.Fatalf("GET /api/knowledge/usage/events failed: %d %s", eventsRec.Code, eventsRec.Body.String())
	}
	var eventsPage domain.KnowledgeUsageEventPage
	if err := json.Unmarshal(eventsRec.Body.Bytes(), &eventsPage); err != nil {
		t.Fatal(err)
	}
	if eventsPage.Total != 1 || len(eventsPage.Items) != 1 || eventsPage.Items[0].DocumentID != "x.md" || eventsPage.Items[0].Caller.Kind != domain.KnowledgeCallerSOP || eventsPage.Items[0].Caller.AgentID != "dev" || eventsPage.Items[0].Query != "dns" {
		t.Fatalf("unexpected SOP usage event: %+v", eventsPage)
	}

	resetRec := doJSONAsUser(t, h, dev42, http.MethodDelete, "/api/knowledge/usage", "")
	if resetRec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/knowledge/usage failed: %d %s", resetRec.Code, resetRec.Body.String())
	}
	afterResetRec := doJSONAsUser(t, h, dev42, http.MethodGet, "/api/knowledge/usage", "")
	var afterSummary domain.KnowledgeUsageSummary
	if err := json.Unmarshal(afterResetRec.Body.Bytes(), &afterSummary); err != nil {
		t.Fatal(err)
	}
	if afterSummary.TotalCalls != 0 || len(afterSummary.Connectors) != 0 || len(afterSummary.Documents) != 0 {
		t.Fatalf("expected empty usage after reset, got %+v", afterSummary)
	}
}

func TestManualRunKnowledgeRefsOverridePersistence(t *testing.T) {
	s, h, _, mcpURL := newKnowledgeTestServer(t)

	_ = s.PutUser(domain.User{ID: 301, LoginName: "alice"})
	alice, _ := s.User(301)
	_ = s.PutProject(domain.Project{ID: "p-run-kb", Name: "Run KB Project", OwnerID: alice.ID})
	_ = s.PutAgent(domain.AgentDefinition{ID: "dev-kb", Name: "Dev KB", KnowledgeBase: true})

	createRec := doJSONAsUser(t, h, alice, http.MethodPost, "/api/knowledge/connectors", `{"name":"Alice KB","type":"mcp","endpoint":"`+mcpURL+`"}`)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create connector = %d (%s)", createRec.Code, createRec.Body.String())
	}
	var conn domain.KnowledgeConnector
	if err := json.Unmarshal(createRec.Body.Bytes(), &conn); err != nil {
		t.Fatal(err)
	}

	_ = s.PutWorkflow(domain.WorkflowDefinition{
		ID:        "wf-run-kb",
		Name:      "WF Run KB",
		ProjectID: "p-run-kb",
		Nodes: []domain.WorkflowNode{
			{
				ID:            "n1",
				Type:          "agent",
				AgentID:       "dev-kb",
				KnowledgeRefs: []domain.KnowledgeRef{{ConnectorID: conn.ID, Mode: "all"}},
			},
		},
	})

	startAndStop := func(body string) (domain.Run, domain.Run) {
		t.Helper()
		rec := doJSONAsUser(t, h, alice, http.MethodPost, "/api/runs", body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST /api/runs (%s) = %d (%s)", body, rec.Code, rec.Body.String())
		}
		var respRun domain.Run
		if err := json.Unmarshal(rec.Body.Bytes(), &respRun); err != nil {
			t.Fatal(err)
		}
		reloaded, ok := s.RunState(respRun.ID)
		if !ok {
			t.Fatalf("RunState(%q) not found", respRun.ID)
		}
		_ = doJSONAsUser(t, h, alice, http.MethodPost, "/api/runs/"+respRun.ID+"/cancel", `{}`)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if latest, ok := s.Run(respRun.ID); ok && (latest.Status == "succeeded" || latest.Status == "failed" || latest.Status == "cancelled") {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		return respRun, reloaded
	}

	// 1. Omitted knowledgeRefs keeps nil (follow node/role defaults).
	respOmitted, dbOmitted := startAndStop(`{"workflowId":"wf-run-kb","projectId":"p-run-kb","input":"omitted"}`)
	if respOmitted.ExecutionContext.KnowledgeRefs != nil || dbOmitted.ExecutionContext.KnowledgeRefs != nil {
		t.Fatalf("omitted knowledgeRefs should stay nil: resp=%#v db=%#v", respOmitted.ExecutionContext.KnowledgeRefs, dbOmitted.ExecutionContext.KnowledgeRefs)
	}

	// 2. Explicit empty array ("knowledgeRefs": []) must survive DB save & reload as non-nil empty slice.
	respEmpty, dbEmpty := startAndStop(`{"workflowId":"wf-run-kb","projectId":"p-run-kb","input":"none","knowledgeRefs":[]}`)
	if respEmpty.ExecutionContext.KnowledgeRefs == nil || len(*respEmpty.ExecutionContext.KnowledgeRefs) != 0 {
		t.Fatalf("explicit empty override lost in HTTP response: %#v", respEmpty.ExecutionContext.KnowledgeRefs)
	}
	if dbEmpty.ExecutionContext.KnowledgeRefs == nil || len(*dbEmpty.ExecutionContext.KnowledgeRefs) != 0 {
		t.Fatalf("explicit empty override lost after DB round-trip: %#v", dbEmpty.ExecutionContext.KnowledgeRefs)
	}

	// 3. Explicit non-empty allowlist override survives DB save & reload.
	respPinned, dbPinned := startAndStop(`{"workflowId":"wf-run-kb","projectId":"p-run-kb","input":"pinned","knowledgeRefs":[{"connectorId":"` + conn.ID + `","mode":"allowlist","sources":["a"]}]}`)
	if respPinned.ExecutionContext.KnowledgeRefs == nil || len(*respPinned.ExecutionContext.KnowledgeRefs) != 1 || (*respPinned.ExecutionContext.KnowledgeRefs)[0].ConnectorID != conn.ID {
		t.Fatalf("pinned override missing in HTTP response: %#v", respPinned.ExecutionContext.KnowledgeRefs)
	}
	if dbPinned.ExecutionContext.KnowledgeRefs == nil || len(*dbPinned.ExecutionContext.KnowledgeRefs) != 1 || (*dbPinned.ExecutionContext.KnowledgeRefs)[0].ConnectorID != conn.ID {
		t.Fatalf("pinned override missing after DB round-trip: %#v", dbPinned.ExecutionContext.KnowledgeRefs)
	}
}
