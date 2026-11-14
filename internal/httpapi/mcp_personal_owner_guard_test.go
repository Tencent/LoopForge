package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	"agentflow/internal/domain"
)

// The owner must still be able to delete, edit and upload files for their own
// personal MCP through every endpoint that now shares requirePersonalMCPOwner;
// TestPersonalMCPEndpointsRejectOtherUsers only covers the deny side.
func TestPersonalMCPEndpointsAllowOwner(t *testing.T) {
	t.Setenv("AGENTFLOW_PREVIEW_LOCAL_LOGIN", "")
	st := cvAStore(t)
	if err := st.PutMCPServer(domain.MCPServerDefinition{ID: "mine", Name: "Mine", Scope: "personal", OwnerUserID: 7, Transport: "http", URL: "https://example.test/mcp"}); err != nil {
		t.Fatal(err)
	}
	h := &mcpHandlers{deps: &deps{repo: st}}
	owner := &domain.User{ID: 7, LoginName: "owner"}

	c, rec := cvAContext(t, http.MethodPost, "/api/mcp-servers/mine/files", "", owner)
	if server, ok := h.editableMCP(c, "mine"); !ok || server.ID != "mine" {
		t.Fatalf("editableMCP denied the owner: ok=%v server=%+v body=%s", ok, server, rec.Body.String())
	}
	if c.Writer.Written() {
		t.Fatalf("editableMCP wrote a response for the owner: %d %s", rec.Code, rec.Body.String())
	}

	// The payload tries to move the MCP to another owner/scope; the handler must
	// keep the stored ownership after the owner check passes.
	c, rec = cvAContext(t, http.MethodPut, "/api/mcp-servers", `{"id":"mine","name":"Changed","scope":"project","projectId":"p1","ownerUserId":8,"transport":"http","url":"https://example.test/changed"}`, owner)
	h.mcpServers(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner PUT = %d %s", rec.Code, rec.Body.String())
	}
	saved, ok := st.MCPServer("mine")
	if !ok || saved.Name != "Changed" || saved.URL != "https://example.test/changed" {
		t.Fatalf("owner PUT did not persist: %+v %v", saved, ok)
	}
	if saved.Scope != "personal" || saved.OwnerUserID != 7 || saved.ProjectID != "" {
		t.Fatalf("owner PUT changed ownership: scope=%q owner=%d project=%q", saved.Scope, saved.OwnerUserID, saved.ProjectID)
	}

	c, rec = cvAContext(t, http.MethodDelete, "/api/mcp-servers?id=mine", "", owner)
	h.mcpServers(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner DELETE = %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := st.MCPServer("mine"); ok {
		t.Fatal("owner DELETE left the personal MCP in place")
	}
}

// requestUser is called with allowBearer=true, so an API token belonging to
// the owner must be accepted while another user's token is still rejected.
func TestRequirePersonalMCPOwnerAcceptsOwnerBearerToken(t *testing.T) {
	t.Setenv("AGENTFLOW_PREVIEW_LOCAL_LOGIN", "")
	st := cvAStore(t)
	for _, u := range []domain.User{{ID: 7, LoginName: "owner"}, {ID: 8, LoginName: "other"}} {
		if err := st.PutUser(u); err != nil {
			t.Fatal(err)
		}
	}
	tokens := map[int64]string{7: "af_test_owner_token", 8: "af_test_other_token"}
	for userID, raw := range tokens {
		sum := sha256.Sum256([]byte(raw))
		if err := st.PutAPIToken(domain.APIToken{ID: "tok-" + raw, UserID: userID, Name: "t", Prefix: "af_test", TokenHash: hex.EncodeToString(sum[:])}); err != nil {
			t.Fatal(err)
		}
	}
	h := &mcpHandlers{deps: &deps{repo: st}}
	personal := domain.MCPServerDefinition{ID: "mine", Scope: "personal", OwnerUserID: 7}

	c, rec := cvAContext(t, http.MethodPut, "/api/mcp-servers", "", nil)
	c.Request.Header.Set("Authorization", "Bearer "+tokens[7])
	if !h.requirePersonalMCPOwner(c, personal, "拒绝文案") {
		t.Fatalf("owner bearer token was denied: %d %s", rec.Code, rec.Body.String())
	}
	if c.Writer.Written() {
		t.Fatalf("allowed bearer request wrote a response: %d %s", rec.Code, rec.Body.String())
	}

	c, rec = cvAContext(t, http.MethodPut, "/api/mcp-servers", "", nil)
	c.Request.Header.Set("Authorization", "Bearer "+tokens[8])
	if h.requirePersonalMCPOwner(c, personal, "拒绝文案") {
		t.Fatal("another user's bearer token was accepted")
	}
	assertMCPForbidden(t, rec.Code, rec.Body.Bytes(), "拒绝文案")
}

// Only the personal scope is owner-restricted: platform-level (empty scope)
// MCPs fall through to the caller's other checks, even for anonymous calls.
func TestRequirePersonalMCPOwnerPassesNonPersonalScopes(t *testing.T) {
	t.Setenv("AGENTFLOW_PREVIEW_LOCAL_LOGIN", "")
	h := &mcpHandlers{deps: &deps{repo: cvAStore(t)}}
	for _, server := range []domain.MCPServerDefinition{
		{ID: "platform", Scope: ""},
		{ID: "platform-owned", Scope: "", OwnerUserID: 7},
		{ID: "project-owned", Scope: "project", ProjectID: "p1", OwnerUserID: 7},
	} {
		c, rec := cvAContext(t, http.MethodPut, "/api/mcp-servers", "", &domain.User{ID: 8, LoginName: "other"})
		if !h.requirePersonalMCPOwner(c, server, "拒绝文案") || c.Writer.Written() {
			t.Fatalf("%s scope=%q must pass through: %d %s", server.ID, server.Scope, rec.Code, rec.Body.String())
		}
	}
}

// A missing id must still get the 404 before any ownership check runs, so an
// anonymous or foreign caller sees "MCP 不存在" rather than a 403 or a panic on
// the zero-value definition.
func TestPersonalMCPMissingIDStillReturnsNotFound(t *testing.T) {
	t.Setenv("AGENTFLOW_PREVIEW_LOCAL_LOGIN", "")
	h := &mcpHandlers{deps: &deps{repo: cvAStore(t)}}
	for _, user := range []*domain.User{nil, {ID: 8, LoginName: "other"}} {
		c, rec := cvAContext(t, http.MethodPost, "/api/mcp-servers/missing/files", "", user)
		if _, ok := h.editableMCP(c, "missing"); ok {
			t.Fatal("editableMCP allowed a missing MCP")
		}
		assertMCPError(t, rec.Code, rec.Body.Bytes(), http.StatusNotFound, "MCP 不存在")

		c, rec = cvAContext(t, http.MethodDelete, "/api/mcp-servers?id=missing", "", user)
		h.mcpServers(c)
		assertMCPError(t, rec.Code, rec.Body.Bytes(), http.StatusNotFound, "MCP 不存在")
	}
}

func assertMCPError(t *testing.T, code int, body []byte, wantCode int, message string) {
	t.Helper()
	if code != wantCode {
		t.Fatalf("status = %d, want %d; body=%s", code, wantCode, body)
	}
	var payload map[string]string
	if err := json.Unmarshal(body, &payload); err != nil || payload["error"] != message {
		t.Fatalf("body = %s, want error %q", body, message)
	}
}
