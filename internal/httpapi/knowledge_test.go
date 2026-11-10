package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/knowledge"
	kbengine "agentflow/internal/knowledge/engine"
	"agentflow/internal/notification"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newKnowledgeTestServer(t *testing.T) (*repo.Store, http.Handler) {
	t.Helper()
	t.Setenv("AGENTFLOW_SESSION_SECRET", "test-session")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, id := range []string{"a", "b"} {
		if err := s.PutKnowledgeBase(domain.KnowledgeBase{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
		if err := s.PutKnowledgeSource(domain.KnowledgeSource{ID: id, BaseID: id, Name: id, Kind: "hosted"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutKnowledgeBinding(domain.KnowledgeBinding{ProjectID: "p1", BaseID: "a", Permission: "submit"}); err != nil {
		t.Fatal(err)
	}
	eng := kbengine.NewEngine(s, s, kbengine.NewMySQLKeywordIndex(s))
	if _, err := eng.WriteDocument(context.Background(), "a", "x.md", "# X\n\n检索正文"); err != nil {
		t.Fatal(err)
	}
	h := newWithNotifications(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "", notification.New(s), nil, nil, kbengine.NewEngine(s, s, knowledgeTestIndex{}))
	return s, h
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
	_, h := newKnowledgeTestServer(t)
	call := func(token, body string) *httptest.ResponseRecorder {
		return doJSON(t, h, http.MethodPost, knowledge.ProxyPath, body, map[string]string{"Authorization": "Bearer " + token})
	}
	if rec := call("upstream-secret", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("upstream token must not authenticate the proxy: %d", rec.Code)
	}
	token, err := knowledge.SignProxyToken(knowledge.SessionSecret(), knowledge.ProxyClaims{ProjectID: "p1", AgentID: "dev"}, time.Now())
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

	if _, isErr, _ := rpcText(t, call(token, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"mcp__agentflow-knowledge__kb_search","arguments":{"query":"帕鲁"}}}`)); isErr {
		t.Fatal("scoped search failed")
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

	usage := doJSON(t, h, http.MethodGet, "/api/knowledge/usage?projectId=p1", "", nil)
	if !strings.Contains(usage.Body.String(), `"tool":"kb_search"`) {
		t.Fatalf("activity not recorded: %s", usage.Body.String())
	}
}

func TestKnowledgeProxyRejectsRotatedSecret(t *testing.T) {
	_, h := newKnowledgeTestServer(t)
	token, err := knowledge.SignProxyToken("old-session", knowledge.ProxyClaims{ProjectID: "p1"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, h, http.MethodPost, knowledge.ProxyPath, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, map[string]string{"Authorization": "Bearer " + token})
	if rec.Code != http.StatusUnauthorized {
		t.Fatal(rec.Code)
	}
}

// SQLite HTTP tests inject only the keyword adapter; content and authorization remain real.
type knowledgeTestIndex struct{}

func (knowledgeTestIndex) Search(_ context.Context, query string, sources []string, _ kbengine.SearchOptions) ([]kbengine.Hit, error) {
	return []kbengine.Hit{{Source: "a", Path: "x.md", Commit: "1", Text: "检索正文"}}, nil
}
