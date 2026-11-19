package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

// checkRefServer is a tiny MCP endpoint used to drive the two failure modes
// of checkReferences independently.
type checkRefServer struct {
	mode string // "error", "no-readonly", "readonly"
}

func (s checkRefServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     any    `json:"id"`
		Method string `json:"method"`
	}
	_ = jsonDecode(r, &req)
	w.Header().Set("Content-Type", "application/json")
	if req.Method != "tools/list" {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		return
	}
	switch s.mode {
	case "error":
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"upstream boom"}}`))
	case "no-readonly":
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[
			{"name":"write_thing","description":"writes","inputSchema":{"type":"object"}}
		]}}`))
	case "readonly":
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[
			{"name":"search_knowledge","description":"search","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}}
		]}}`))
	}
}

func jsonDecode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	return dec.Decode(v)
}

func newCheckRefHandler(t *testing.T, mode string) (*knowledgeHandlers, *gin.Engine, domain.User) {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	upstream := httptest.NewServer(checkRefServer{mode: mode})
	t.Cleanup(upstream.Close)

	const project = "p1"
	const serverID = "ext-mcp"
	if err := s.PutMCPServer(domain.MCPServerDefinition{
		ID: serverID, Name: "External", ProjectID: project, Scope: "project",
		Transport: "http", URL: upstream.URL,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutProject(domain.Project{ID: project, Name: "P1"}); err != nil {
		t.Fatal(err)
	}
	user := domain.User{ID: 1, LoginName: "lab"}
	if err := s.PutUser(user); err != nil {
		t.Fatal(err)
	}

	h := &knowledgeHandlers{
		deps: &deps{repo: s},
		svc:  knowledge.NewService(s, func() string { return "id" }),
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/knowledge/reference-check", h.checkReferences)
	return h, r, user
}

func doCheckRef(t *testing.T, router *gin.Engine, user domain.User, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/knowledge/reference-check?projectId=p1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, domainUserToAuth(user)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

const checkRefBody = `{"knowledgeRefs":[{"mcpServerId":"ext-mcp","mode":"all"}]}`

func TestCheckReferenceErrorSurfacesUnderlyingMessage(t *testing.T) {
	_, router, user := newCheckRefHandler(t, "error")
	rec := doCheckRef(t, router, user, checkRefBody)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "当前身份无法发现可用只读工具") {
		t.Fatalf("stale misleading message returned: %s", body)
	}
	if !strings.Contains(body, "知识服务不可用或调用失败") {
		t.Fatalf("underlying error not surfaced: %s", body)
	}
}

func TestCheckReferenceNoReadOnlyToolsMessage(t *testing.T) {
	_, router, user := newCheckRefHandler(t, "no-readonly")
	rec := doCheckRef(t, router, user, checkRefBody)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "未发现声明为只读的工具") {
		t.Fatalf("expected no-readonly message, got: %s", body)
	}
	if strings.Contains(body, "当前身份无法发现可用只读工具") {
		t.Fatalf("stale misleading message returned: %s", body)
	}
}

func TestCheckReferenceReadOnlyToolSucceeds(t *testing.T) {
	_, router, user := newCheckRefHandler(t, "readonly")
	rec := doCheckRef(t, router, user, checkRefBody)
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 204/200; body=%s", rec.Code, rec.Body.String())
	}
}
