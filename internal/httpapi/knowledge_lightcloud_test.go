package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
)

func TestLightCloudKnowledgeSwitchScopeAndReview(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "false")
	store, handler := newKnowledgeTestServer(t)
	var mu sync.Mutex
	var calls []string
	var lastWrite map[string]any
	var searchSources []any
	token := "lightcloud-test-token"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Error("upstream did not receive the configured credentials")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var request struct {
			ID     json.RawMessage
			Method string
			Params struct {
				Name      string
				Arguments map[string]any
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "test-lightcloud", "version": "1"}}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			names := []string{"kb_search", "kb_fetch", "kb_list_sources", "kb_get_source", "kb_write_document", "kb_grep", "kb_report_issue", "kb_remove"}
			tools := make([]map[string]any, 0, len(names))
			for _, name := range names {
				tools = append(tools, map[string]any{"name": name, "inputSchema": map[string]any{"type": "object"}})
			}
			result = map[string]any{"tools": tools}
		case "tools/call":
			calls = append(calls, request.Params.Name)
			var payload string
			switch request.Params.Name {
			case "kb_list_sources":
				payload = `{"sources":[{"id":"allowed","name":"Visible","doc_count":1},{"id":"private","name":"Other project"}]}`
			case "kb_search":
				searchSources, _ = request.Params.Arguments["sources"].([]any)
				payload = `{"query":"needle","mode":"lexical","results":[{"source":"allowed","path":"read.md"},{"source":"private","path":"private.md"}]}`
			case "kb_fetch":
				payload = `{"source":"allowed","path":"read.md","lines":[{"number":1,"text":"upstream text"}]}`
			case "kb_get_source":
				payload = `{"source":{"id":"allowed"},"documents":[{"path":"read.md"}]}`
			case "kb_write_document":
				lastWrite = request.Params.Arguments
				payload = `{"source":"allowed","path":"reviewed.md","commit":"external-commit"}`
			default:
				t.Errorf("unexpected upstream tool %q", request.Params.Name)
			}
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": payload}}}
		default:
			t.Errorf("unexpected upstream method %q", request.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer upstream.Close()

	// Simulate the stored settings of an upgraded installation with no provider.
	if err := store.PutKnowledgeSettings(domain.KnowledgeSettings{
		URL: upstream.URL, Token: token, Enabled: true,
		DefaultSources: []string{"private"}, ProjectSources: map[string][]string{"p1": {"allowed"}},
	}); err != nil {
		t.Fatal(err)
	}
	api := func(method, path, body string, want int) string {
		t.Helper()
		r := doJSON(t, handler, method, path, body, nil)
		if r.Code != want {
			t.Fatalf("%s %s = %d, want %d, body %s", method, path, r.Code, want, r.Body.String())
		}
		return r.Body.String()
	}
	settings := api("GET", "/api/knowledge/settings", "", http.StatusOK)
	if !strings.Contains(settings, `"provider":"lightcloud"`) ||
		!strings.Contains(settings, `"tokenConfigured":true`) || strings.Contains(settings, token) {
		t.Fatal("expected inferred LightCloud and redacted settings")
	}
	if checked := api("POST", "/api/knowledge/check", `{}`, 200); !strings.Contains(checked, `"ok":true`) {
		t.Fatal("configured upstream must pass connection check")
	}
	proxyToken, err := knowledge.SignProxyToken(knowledge.SessionSecret(), knowledge.ProxyClaims{ProjectID: "p1", AgentID: "test"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rpc := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		return doJSON(t, handler, "POST", knowledge.ProxyPath, body, map[string]string{"Authorization": "Bearer " + proxyToken})
	}
	tools := func() []string {
		t.Helper()
		var list struct {
			Result struct{ Tools []struct{ Name string } }
		}
		if err := json.Unmarshal(rpc(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`).Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, item := range list.Result.Tools {
			names = append(names, item.Name)
		}
		return names
	}
	if got := tools(); !reflect.DeepEqual(got, []string{"kb_search", "kb_fetch", "kb_list_sources", "kb_get_source"}) {
		t.Fatalf("LightCloud tools: %v", got)
	}
	call := func(name, args string) (string, bool) {
		t.Helper()
		response := rpc(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, name, args))
		text, failed, _ := rpcText(t, response)
		return text, failed
	}
	for _, name := range []string{"kb_write_document", "kb_grep", "kb_report_issue", "kb_remove"} {
		if _, failed := call(name, `{}`); !failed {
			t.Fatalf("%s should be blocked", name)
		}
	}
	if text, failed := call("kb_list_sources", `{}`); failed || !strings.Contains(text, `"id":"allowed"`) || strings.Contains(text, "private") {
		t.Fatalf("project source list: %s", text)
	}
	if text, failed := call("kb_search", `{"query":"needle"}`); failed || !strings.Contains(text, "read.md") || strings.Contains(text, "private.md") {
		t.Fatalf("project search: %s", text)
	}
	if text, failed := call("kb_fetch", `{"source":"allowed","path":"read.md"}`); failed || !strings.Contains(text, "upstream text") {
		t.Fatalf("fetch: %s", text)
	}
	if text, failed := call("kb_get_source", `{"source":"allowed"}`); failed || !strings.Contains(text, "read.md") {
		t.Fatalf("get source: %s", text)
	}
	for name, args := range map[string]string{
		"kb_search":     `{"query":"needle","sources":["private"]}`,
		"kb_fetch":      `{"source":"private","path":"private.md"}`,
		"kb_get_source": `{"source":"private"}`,
	} {
		if text, failed := call(name, args); !failed || !strings.Contains(text, "授权范围") {
			t.Fatalf("project scope %s: %s", name, text)
		}
	}
	mu.Lock()
	gotCalls, gotScope := strings.Join(calls, ","), append([]any(nil), searchSources...)
	mu.Unlock()
	if !reflect.DeepEqual(gotScope, []any{"allowed"}) || gotCalls != "kb_list_sources,kb_list_sources,kb_search,kb_fetch,kb_get_source" {
		t.Fatalf("unexpected forwarding: calls=%s scope=%v", gotCalls, gotScope)
	}
	for _, path := range []string{"/api/knowledge/bases", "/api/knowledge/governance", "/api/knowledge/bases/vector", "/api/knowledge/citations"} {
		if text := api("GET", path+"?projectId=p1", "", 400); !strings.Contains(text, "仅内置") {
			t.Fatalf("missing mode error for %s", path)
		}
	}
	newReview := func(source string) string {
		t.Helper()
		raw := api("POST", "/api/knowledge/submissions", fmt.Sprintf(`{"projectId":"p1","title":"Review","source":%q,"path":"reviewed.md","content":"approved text"}`, source), 200)
		var item domain.KnowledgeSubmission
		if err := json.Unmarshal([]byte(raw), &item); err != nil || item.ID == "" {
			t.Fatal("review id missing", err)
		}
		return "/api/knowledge/submissions/" + item.ID
	}
	review := newReview("allowed")
	api("POST", review+"/publish", "", 403)
	api("PUT", "/api/knowledge/settings", `{"provider":"lightcloud","writeEnabled":true}`, 200)
	api("POST", newReview("private")+"/publish", "", 403)
	if text := api("POST", review+"/publish", "", 200); !strings.Contains(text, `"commit":"external-commit"`) {
		t.Fatalf("expected upstream commit in reviewed result: %s", text)
	}
	mu.Lock()
	write := lastWrite
	mu.Unlock()
	if !reflect.DeepEqual(write, map[string]any{"source": "allowed", "path": "reviewed.md", "content": "# Review\n\napproved text"}) {
		t.Fatalf("write request: %+v", write)
	}
	api("POST", review+"/retract", "", 400)
	api("PUT", "/api/knowledge/settings", `{"provider":"builtin"}`, 200)
	if got := tools(); !reflect.DeepEqual(got, knowledge.AgentTools) {
		t.Fatalf("same proxy token did not switch back to builtin: %v", got)
	}
	if text, failed := call("kb_fetch", `{"source":"a","path":"x.md"}`); failed || !strings.Contains(text, "检索正文") {
		t.Fatalf("builtin document must survive a switch: %s", text)
	}
	api("GET", review, "", 404)
	builtinReview := newReview("a")
	api("PUT", "/api/knowledge/settings", `{"provider":"lightcloud"}`, 200)
	api("GET", builtinReview, "", 404)
	api("GET", review, "", 200)
	if got := tools(); len(got) != 4 {
		t.Fatalf("saved connection should survive builtin mode: %v", got)
	}
}
