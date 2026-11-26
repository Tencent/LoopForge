package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/knowledge"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

// fakeKB is a minimal stateless kb MCP that records every tools/call.
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
	case "tools/list":
		tools := []map[string]any{}
		for _, name := range []string{"kb_search", "kb_fetch", "kb_list_sources", "kb_get_source", "kb_write_document"} {
			tools = append(tools, map[string]any{"name": name, "description": name, "inputSchema": map[string]any{"type": "object"}})
		}
		result = map[string]any{"tools": tools}
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
			result = text(map[string]any{"sources": []map[string]any{{"id": "a", "name": "a", "status": "ready", "doc_count": 2}, {"id": "b", "name": "b", "status": "ready", "doc_count": 1}}})
		case "kb_search":
			result = text(map[string]any{"query": p.Arguments["query"], "mode": "hybrid", "results": []map[string]any{{"source": "a", "commit": "c1", "path": "x.md", "start_line": 1, "end_line": 3, "snippet": "hit", "citation": "a@c1:x.md#L1-L3"}}})
		case "kb_fetch":
			result = text(map[string]any{"source": p.Arguments["source"], "path": p.Arguments["path"], "lines": []map[string]any{{"number": 1, "text": "# X"}}})
		case "kb_write_document":
			result = text(map[string]any{"source": p.Arguments["source"], "path": p.Arguments["path"], "commit": "new-commit"})
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

func newKnowledgeTestServer(t *testing.T) (*repo.Store, http.Handler, *fakeKB, domain.KnowledgeSettings) {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	kb := &fakeKB{}
	upstream := httptest.NewServer(kb)
	t.Cleanup(upstream.Close)
	settings := domain.KnowledgeSettings{
		Enabled: true, URL: upstream.URL + "/mcp", Token: "upstream-secret", WriteEnabled: true,
		ProjectSources: map[string][]string{"p1": {"a"}},
	}
	if err := s.PutKnowledgeSettings(settings); err != nil {
		t.Fatal(err)
	}
	handler := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	return s, handler, kb, settings
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

func TestKnowledgeProxyIsReadOnlyAndProjectScoped(t *testing.T) {
	_, h, kb, settings := newKnowledgeTestServer(t)
	call := func(token, body string) *httptest.ResponseRecorder {
		return doJSON(t, h, http.MethodPost, knowledge.ProxyPath, body, map[string]string{"Authorization": "Bearer " + token})
	}
	if rec := call("upstream-secret", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("upstream token must not authenticate the proxy: %d", rec.Code)
	}
	token, err := knowledge.SignProxyToken(settings, knowledge.ProxyClaims{ProjectID: "p1", AgentID: "dev"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	rec := call(token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var list struct {
		Result struct {
			Tools []struct{ Name string } `json:"tools"`
		} `json:"result"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	names := []string{}
	for _, tool := range list.Result.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "kb_search,kb_fetch,kb_list_sources,kb_get_source" {
		t.Fatalf("proxy tools = %v", names)
	}

	if _, isErr, msg := rpcText(t, call(token, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"kb_write_document","arguments":{"source":"a","path":"x.md","content":"x"}}}`)); !isErr || !strings.Contains(msg, "只读") {
		t.Fatalf("write tool must be rejected, got %q", msg)
	}
	if kb.lastCall("kb_write_document") != nil {
		t.Fatal("write reached upstream")
	}

	if _, isErr, _ := rpcText(t, call(token, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"mcp__agentflow-knowledge__kb_search","arguments":{"query":"帕鲁"}}}`)); isErr {
		t.Fatal("scoped search failed")
	}
	if got := kb.lastCall("kb_search"); got == nil || strings.Join(stringList(got["sources"]), ",") != "a" {
		t.Fatalf("search not narrowed to project sources: %#v", got)
	}

	if text, isErr, _ := rpcText(t, call(token, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"kb_search","arguments":{"query":"x","sources":["b"]}}}`)); !isErr || !strings.Contains(text, "授权范围") {
		t.Fatalf("out-of-scope search must fail: %q", text)
	}
	if text, isErr, _ := rpcText(t, call(token, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"kb_fetch","arguments":{"source":"b","path":"x.md"}}}`)); !isErr || !strings.Contains(text, "授权范围") {
		t.Fatalf("out-of-scope fetch must fail: %q", text)
	}

	text, _, _ := rpcText(t, call(token, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"kb_list_sources","arguments":{}}}`))
	if !strings.Contains(text, `"id":"a"`) || strings.Contains(text, `"id":"b"`) {
		t.Fatalf("list sources not filtered: %s", text)
	}

	for _, auth := range kb.auth {
		if auth != "Bearer upstream-secret" {
			t.Fatalf("upstream received %q", auth)
		}
	}
	usage := doJSON(t, h, http.MethodGet, "/api/knowledge/usage?projectId=p1", "", nil)
	if !strings.Contains(usage.Body.String(), `"tool":"kb_search"`) {
		t.Fatalf("activity not recorded: %s", usage.Body.String())
	}
}

func TestKnowledgeSettingsNeverReturnToken(t *testing.T) {
	_, h, _, _ := newKnowledgeTestServer(t)
	rec := doJSON(t, h, http.MethodGet, "/api/knowledge/settings", "", nil)
	if strings.Contains(rec.Body.String(), "upstream-secret") || !strings.Contains(rec.Body.String(), `"tokenConfigured":true`) {
		t.Fatalf("settings leak or missing flag: %s", rec.Body.String())
	}
	// Saving without a token keeps the stored one.
	rec = doJSON(t, h, http.MethodPut, "/api/knowledge/settings", `{"enabled":true,"url":"http://kb.example/mcp","writeEnabled":false}`, nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "upstream-secret") || !strings.Contains(rec.Body.String(), `"ready":true`) {
		t.Fatalf("save kept token? %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodPut, "/api/knowledge/settings", `{"enabled":true,"url":"file:///etc","clearToken":true}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-http url accepted: %d", rec.Code)
	}
}

func TestKnowledgeSubmissionFromArtifactPublishes(t *testing.T) {
	s, h, kb, _ := newKnowledgeTestServer(t)
	run := domain.Run{
		ID: "run-1", WorkflowID: "wf", Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "p1", IssueID: "iss-1", RunID: "run-1"},
		Artifacts: []domain.Artifact{{
			ID: "art-1", RunID: "run-1", NodeID: "distill", Name: "knowledge.md", Path: "knowledge.md", MediaType: "text/markdown",
			Content: "---\ntitle: 帕鲁断联\nsource: a\npath: playbooks/palu.md\n---\n正文", CreatedAt: time.Now(),
		}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, h, http.MethodGet, "/api/knowledge/candidates?projectId=p1", "", nil)
	if !strings.Contains(rec.Body.String(), `"id":"art-1"`) {
		t.Fatalf("candidate missing: %s", rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodPost, "/api/knowledge/submissions", `{"runId":"run-1","artifactId":"art-1"}`, nil)
	var item domain.KnowledgeSubmission
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil || item.Title != "帕鲁断联" || item.Source != "a" || item.Path != "playbooks/palu.md" || item.ProjectID != "p1" || item.Content != "正文" {
		t.Fatalf("submission = %#v (%s)", item, rec.Body.String())
	}
	if dup := doJSON(t, h, http.MethodPost, "/api/knowledge/submissions", `{"runId":"run-1","artifactId":"art-1"}`, nil); dup.Code != http.StatusConflict {
		t.Fatalf("duplicate artifact submission status = %d", dup.Code)
	}

	// Sources outside the project scope cannot be published to.
	doJSON(t, h, http.MethodPut, "/api/knowledge/submissions/"+item.ID, `{"title":"帕鲁断联","source":"b","path":"playbooks/palu.md"}`, nil)
	if rec := doJSON(t, h, http.MethodPost, "/api/knowledge/submissions/"+item.ID+"/publish", "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("out-of-scope publish status = %d %s", rec.Code, rec.Body.String())
	}
	doJSON(t, h, http.MethodPut, "/api/knowledge/submissions/"+item.ID, `{"title":"帕鲁断联","source":"a","path":"playbooks/palu.md"}`, nil)
	rec = doJSON(t, h, http.MethodPost, "/api/knowledge/submissions/"+item.ID+"/publish", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"commit":"new-commit"`) || !strings.Contains(rec.Body.String(), `"status":"published"`) {
		t.Fatalf("publish = %d %s", rec.Code, rec.Body.String())
	}
	args := kb.lastCall("kb_write_document")
	if args["source"] != "a" || args["path"] != "playbooks/palu.md" || args["content"] != "# 帕鲁断联\n\n正文" {
		t.Fatalf("upstream write args = %#v", args)
	}
	if rec := doJSON(t, h, http.MethodDelete, "/api/knowledge/submissions/"+item.ID, "", nil); rec.Code != http.StatusConflict {
		t.Fatalf("published submission deletable: %d", rec.Code)
	}
}
