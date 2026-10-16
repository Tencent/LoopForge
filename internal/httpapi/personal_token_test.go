package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestPersonalTokenAuthenticatesRESTAndMCPAndCanBeRevoked(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "personal-token-test-secret")

	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutUser(domain.User{LoginName: "alice", ChineseName: "Alice"}); err != nil {
		t.Fatal(err)
	}
	stored, ok := s.UserByLoginName("alice")
	if !ok || stored.ID <= 0 {
		t.Fatalf("stored user = %+v, ok=%v", stored, ok)
	}

	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	signed, err := auth.NewClientFromEnv().SignUser(&auth.User{
		ID: stored.ID, LoginName: stored.LoginName, ChineseName: stored.ChineseName,
		IsGuest: false, AuthTime: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	session := &http.Cookie{Name: authCookieName, Value: signed}

	unauthenticated := httptest.NewRequest(http.MethodGet, "/api/capabilities", nil)
	unauthenticatedRec := httptest.NewRecorder()
	h.ServeHTTP(unauthenticatedRec, unauthenticated)
	if unauthenticatedRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated capabilities status=%d body=%s", unauthenticatedRec.Code, unauthenticatedRec.Body.String())
	}

	create := httptest.NewRequest(http.MethodPost, "/api/auth/tokens",
		strings.NewReader(`{"name":"Alice Cursor","expiresInDays":30}`))
	create.Header.Set("Content-Type", "application/json")
	create.AddCookie(session)
	createRec := httptest.NewRecorder()
	h.ServeHTTP(createRec, create)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		Token    string          `json:"token"`
		Metadata domain.APIToken `json:"metadata"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Token, "afp_") || created.Metadata.ID == "" || created.Metadata.UserID != stored.ID {
		t.Fatalf("created token = %+v", created)
	}
	if strings.Contains(createRec.Body.String(), "tokenHash") {
		t.Fatal("token hash must never be serialized")
	}

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/capabilities", ""},
		{http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+created.Token)
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s with bearer status=%d body=%s", tc.path, rec.Code, rec.Body.String())
		}
	}

	list := httptest.NewRequest(http.MethodGet, "/api/auth/tokens", nil)
	list.AddCookie(session)
	listRec := httptest.NewRecorder()
	h.ServeHTTP(listRec, list)
	if listRec.Code != http.StatusOK || strings.Contains(listRec.Body.String(), created.Token) || !strings.Contains(listRec.Body.String(), created.Metadata.Prefix) {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}

	revoke := httptest.NewRequest(http.MethodDelete, "/api/auth/tokens/"+created.Metadata.ID, nil)
	revoke.AddCookie(session)
	revokeRec := httptest.NewRecorder()
	h.ServeHTTP(revokeRec, revoke)
	if revokeRec.Code != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", revokeRec.Code, revokeRec.Body.String())
	}

	revokedReq := httptest.NewRequest(http.MethodGet, "/api/capabilities", nil)
	revokedReq.Header.Set("Authorization", "Bearer "+created.Token)
	revokedRec := httptest.NewRecorder()
	h.ServeHTTP(revokedRec, revokedReq)
	if revokedRec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token status=%d body=%s", revokedRec.Code, revokedRec.Body.String())
	}
}

// An owner with no tokens must get [] rather than null: the Studio token tab
// treats a non-array payload as "still loading".
func TestPersonalTokenListIsEmptyArrayWhenOwnerHasNoTokens(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "empty-token-list-secret")

	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutUser(domain.User{LoginName: "bob", ChineseName: "Bob"}); err != nil {
		t.Fatal(err)
	}
	stored, ok := s.UserByLoginName("bob")
	if !ok {
		t.Fatal("user not stored")
	}
	if s.APITokens(stored.ID) == nil {
		t.Fatal("store returned nil token slice")
	}

	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	signed, err := auth.NewClientFromEnv().SignUser(&auth.User{
		ID: stored.ID, LoginName: stored.LoginName, ChineseName: stored.ChineseName,
		IsGuest: false, AuthTime: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/auth/tokens", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: signed})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Fatalf("body = %s, want []", got)
	}
}

func TestPersonalTokenManagementRequiresInteractiveLogin(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "false")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")

	req := httptest.NewRequest(http.MethodPost, "/api/auth/tokens", strings.NewReader(`{"name":"bad"}`))
	req.Header.Set("Authorization", "Bearer afp_not-a-session")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
