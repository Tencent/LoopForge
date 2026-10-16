package httpapi

import (
	"encoding/json"
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
)

func TestCapabilitiesAndMCPShareAuthenticatedHTTPServer(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	handler := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "user", "pass")

	for _, path := range []string{"/api/capabilities", "/mcp"} {
		method, body := http.MethodGet, ""
		if path == "/mcp" {
			method = http.MethodPost
			body = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s without auth status = %d", path, rec.Code)
		}

		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.SetBasicAuth("user", "pass")
		if path == "/mcp" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s with auth status = %d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestCapabilitiesDescribeBothMCPTransports(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	handler := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	req := httptest.NewRequest(http.MethodGet, "/api/capabilities", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var result struct {
		Operations []any          `json:"operations"`
		MCP        map[string]any `json:"mcp"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Operations) < 70 || result.MCP["streamableHttp"] != "/mcp" || result.MCP["stdioCommand"] != "agentflow-mcp" {
		t.Fatalf("unexpected capabilities: %+v", result)
	}
}

func TestOpenAPIContractIsServedFromBinary(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	handler := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	req := httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "application/yaml") {
		t.Fatalf("status=%d content-type=%q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "openapi: 3.1.0") || !strings.Contains(rec.Body.String(), "/api/capabilities:") {
		t.Fatalf("unexpected OpenAPI body")
	}
}

func TestSingleProjectRepositoryAndTaskAPI(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "p1", Name: "P1", GitToken: "secret", CodeBuddyAPIKey: "secret-key"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRepository(domain.Repository{ID: "r1", ProjectID: "p1", Name: "Repo", Provider: "gongfeng", GongfengProjectID: "7", GitToken: "repo-secret"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTask(domain.Task{ID: "t1", IssueID: "i1", Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	handler := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	for _, path := range []string{"/api/projects/p1", "/api/repositories/r1", "/api/tasks/t1"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "repo-secret") || strings.Contains(rec.Body.String(), "secret-key") || strings.Contains(rec.Body.String(), `"gitToken":"secret"`) {
			t.Fatalf("%s leaked a secret: %s", path, rec.Body.String())
		}
	}
}

func TestAgentRevisionCanBeReadByID(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutAgent(domain.AgentDefinition{ID: "agent-1", Name: "Agent", Version: 1, Instructions: "v1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgent(domain.AgentDefinition{ID: "agent-1", Name: "Agent", Version: 2, Instructions: "v2"}); err != nil {
		t.Fatal(err)
	}
	revisions := s.Revisions("agent", "agent-1")
	if len(revisions) == 0 {
		t.Fatal("expected an Agent revision")
	}
	handler := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	req := httptest.NewRequest(http.MethodGet, "/api/agents/agent-1/revisions/"+revisions[0].ID, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), revisions[0].ID) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRepositoryMCPPasteWinsOverGeneratedIdentity(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	api := New(st, engine.New(st, runtime.NewManager(t.TempDir())), tracker.NewService(), "", "")
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"repositories_save","arguments":{"body":{"id":"mcp-url","projectId":"platform","url":"https://git.example.com/example/login","gongfengProjectId":"wrong/generated","webUrl":"https://git.example.com/wrong/generated","cloneUrl":"https://git.example.com/wrong/generated.git"}}}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, req)
	var rpc struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error any `json:"error"`
	}
	json.Unmarshal(w.Body.Bytes(), &rpc)
	if w.Code != 200 || rpc.Error != nil || rpc.Result.IsError {
		t.Fatal(w.Code, w.Body.String())
	}
	saved, ok := st.Repository("mcp-url")
	if !ok || saved.GongfengProjectID != "example/login" || saved.WebURL != "https://git.example.com/example/login" || saved.CloneURL != "https://git.example.com/example/login.git" {
		t.Fatalf("URL and stored identity diverged: %+v", saved)
	}
}
