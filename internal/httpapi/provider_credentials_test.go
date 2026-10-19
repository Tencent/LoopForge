package httpapi

import (
	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestProviderCredentialAPIAuthorizationAndURLs(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.PutUser(domain.User{LoginName: "provider-alice"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutUser(domain.User{LoginName: "provider-bob"}); err != nil {
		t.Fatal(err)
	}
	alice, _ := st.UserByLoginName("provider-alice")
	authClient := auth.NewClient(auth.Config{SessionSecret: "provider-test-secret"})
	s := &Server{deps: &deps{repo: st, authClient: authClient}}
	s.users = &usersHandlers{deps: s.deps}
	g := gin.New()
	g.PUT("/api/users/:id/credentials", s.users.user)
	for _, tc := range []struct {
		name, login, body string
		code              int
	}{
		{"guest", "", "{\"codexApiKey\":\"secret\"}", 403},
		{"other", "provider-bob", "{\"claudeCodeApiKey\":\"secret\"}", 403},
		{"invalid", "provider-alice", "{\"codexApiKey\":\"secret\",\"codexBaseUrl\":\"file:///tmp/x\"}", 400},
		{"url-secret", "provider-alice", "{\"claudeCodeBaseUrl\":\"https://user:secret@example.com\"}", 400},
		{"query", "provider-alice", "{\"codexBaseUrl\":\"https://example.com?key=secret\"}", 400},
		{"invalid-taihu", "provider-alice", "{\"taihuPat\":\"not-a-pat\"}", 400},
		{"valid", "provider-alice", "{\"taihuPat\":\"Bearer tai_pat_test-secret\",\"codexApiKey\":\"codex-test-secret\",\"codexBaseUrl\":\"http://localhost:9999/v1\",\"claudeCodeApiKey\":\"claude-test-secret\",\"claudeCodeBaseUrl\":\"https://example.com/anthropic\"}", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/users/%d/credentials", alice.ID), strings.NewReader(tc.body))
			if tc.login != "" {
				identity, ok := st.UserByLoginName(tc.login)
				if !ok {
					t.Fatalf("missing test identity %s", tc.login)
				}
				token, err := authClient.SignUser(&auth.User{ID: identity.ID, LoginName: identity.LoginName, AuthTime: time.Now().UTC()})
				if err != nil {
					t.Fatal(err)
				}
				req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
			}
			rec := httptest.NewRecorder()
			g.ServeHTTP(rec, req)
			if rec.Code != tc.code {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "test-secret") {
				t.Fatal("API leaked key")
			}
			if tc.code == 200 {
				var public domain.User
				if err := json.Unmarshal(rec.Body.Bytes(), &public); err != nil {
					t.Fatal(err)
				}
				if !public.TaihuPATConfigured || !public.CodexAPIKeyConfigured || !public.ClaudeCodeAPIKeyConfigured {
					t.Fatal("configured state missing")
				}
				stored, _ := st.User(alice.ID)
				if stored.TaihuPAT != "tai_pat_test-secret" {
					t.Fatalf("Taihu PAT was not normalized: %q", stored.TaihuPAT)
				}
			}
		})
	}
}
