package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"agentflow/internal/domain"
)

func TestRequirePersonalMCPOwner(t *testing.T) {
	t.Setenv("AGENTFLOW_PREVIEW_LOCAL_LOGIN", "")
	h := &mcpHandlers{deps: &deps{repo: cvAStore(t)}}
	owner := &domain.User{ID: 7, LoginName: "owner"}
	other := &domain.User{ID: 8, LoginName: "other"}
	personal := domain.MCPServerDefinition{ID: "mine", Scope: "personal", OwnerUserID: 7}
	project := domain.MCPServerDefinition{ID: "shared", Scope: "project", ProjectID: "p1"}

	for _, tc := range []struct {
		name    string
		server  domain.MCPServerDefinition
		user    *domain.User
		allowed bool
	}{
		{"project scope needs no login", project, nil, true},
		{"owner may act on personal", personal, owner, true},
		{"other user is denied", personal, other, false},
		{"anonymous is denied", personal, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := cvAContext(t, http.MethodPut, "/api/mcp-servers", "", tc.user)
			if got := h.requirePersonalMCPOwner(c, tc.server, "拒绝文案"); got != tc.allowed {
				t.Fatalf("allowed = %v, want %v", got, tc.allowed)
			}
			if tc.allowed {
				if c.Writer.Written() {
					t.Fatalf("allowed request wrote a response: %d %s", rec.Code, rec.Body.String())
				}
				return
			}
			assertMCPForbidden(t, rec.Code, rec.Body.Bytes(), "拒绝文案")
		})
	}
}

func TestPersonalMCPEndpointsRejectOtherUsers(t *testing.T) {
	t.Setenv("AGENTFLOW_PREVIEW_LOCAL_LOGIN", "")
	st := cvAStore(t)
	if err := st.PutMCPServer(domain.MCPServerDefinition{ID: "mine", Name: "Mine", Scope: "personal", OwnerUserID: 7, Transport: "http", URL: "https://example.test/mcp"}); err != nil {
		t.Fatal(err)
	}
	h := &mcpHandlers{deps: &deps{repo: st}}
	other := &domain.User{ID: 8, LoginName: "other"}

	c, rec := cvAContext(t, http.MethodDelete, "/api/mcp-servers?id=mine", "", other)
	h.mcpServers(c)
	assertMCPForbidden(t, rec.Code, rec.Body.Bytes(), "不能移除其他用户的个人 MCP")

	c, rec = cvAContext(t, http.MethodPut, "/api/mcp-servers", `{"id":"mine","name":"Changed","transport":"http","url":"https://example.test/changed"}`, other)
	h.mcpServers(c)
	assertMCPForbidden(t, rec.Code, rec.Body.Bytes(), "不能修改其他用户的个人 MCP")

	c, rec = cvAContext(t, http.MethodPost, "/api/mcp-servers/mine/files", "", other)
	if _, ok := h.editableMCP(c, "mine"); ok {
		t.Fatal("editableMCP allowed another user's personal MCP")
	}
	assertMCPForbidden(t, rec.Code, rec.Body.Bytes(), "不能修改其他用户的个人 MCP")

	if saved, ok := st.MCPServer("mine"); !ok || saved.Name != "Mine" {
		t.Fatalf("personal MCP changed after rejected requests: %+v %v", saved, ok)
	}
}

func assertMCPForbidden(t *testing.T, code int, body []byte, message string) {
	t.Helper()
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", code, body)
	}
	var payload map[string]string
	if err := json.Unmarshal(body, &payload); err != nil || payload["error"] != message {
		t.Fatalf("body = %s, want error %q", body, message)
	}
}
