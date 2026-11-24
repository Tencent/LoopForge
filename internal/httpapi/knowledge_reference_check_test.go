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
	return json.NewDecoder(r.Body).Decode(v)
}

func newCheckRefHandler(t *testing.T, mode string) (*knowledgeHandlers, *gin.Engine, domain.User, string) {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	upstream := httptest.NewServer(checkRefServer{mode: mode})
	t.Cleanup(upstream.Close)

	user := domain.User{ID: 1, LoginName: "lab"}
	if err := s.PutUser(user); err != nil {
		t.Fatal(err)
	}
	svc := knowledge.NewService(s, func() string { return "ext" })
	conn, err := svc.CreateConnector(context.Background(), user.ID, knowledge.ConnectorInput{
		Name:     "External",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: upstream.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	h := &knowledgeHandlers{
		deps: &deps{repo: s, knowledgeSvc: svc, knowledgeExec: knowledge.NewExecutor(svc)},
		svc:  svc,
		exec: knowledge.NewExecutor(svc),
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/knowledge/reference-check", h.checkReferences)
	return h, r, user, conn.ID
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

func TestCheckReferenceErrorSurfacesUnderlyingMessage(t *testing.T) {
	_, router, user, connID := newCheckRefHandler(t, "error")
	rec := doCheckRef(t, router, user, `{"knowledgeRefs":[{"connectorId":"`+connID+`","mode":"all"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "知识库 MCP 服务不可用或调用失败") {
		t.Fatalf("underlying error not surfaced: %s", body)
	}
}

func TestCheckReferenceNoReadOnlyToolsMessage(t *testing.T) {
	_, router, user, connID := newCheckRefHandler(t, "no-readonly")
	rec := doCheckRef(t, router, user, `{"knowledgeRefs":[{"connectorId":"`+connID+`","mode":"all"}]}`)
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 204/200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCheckReferenceReadOnlyToolSucceeds(t *testing.T) {
	_, router, user, connID := newCheckRefHandler(t, "readonly")
	rec := doCheckRef(t, router, user, `{"knowledgeRefs":[{"connectorId":"`+connID+`","mode":"all"}]}`)
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 204/200; body=%s", rec.Code, rec.Body.String())
	}
}
