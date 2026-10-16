package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

func TestLocalAcceptanceLogin(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.PutUser(domain.User{LoginName: "alice", ChineseName: "爱丽丝"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{deps: &deps{repo: st, authClient: auth.NewClient(auth.Config{})}}
	s.auth = &authHandlers{deps: s.deps}
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	g := gin.New()
	g.Use((&tokenHandlers{deps: s.deps}).userAccessAuth())
	g.GET("/api/auth/local", s.auth.authLocal)
	g.POST("/api/auth/local", s.auth.authLocal)
	for _, tc := range []struct {
		name, configured, remote, origin, forwarded string
		status                                      int
	}{
		{"disabled", "", "127.0.0.1:1234", "http://localhost:8088", "", 403},
		{"remote", "alice", "192.0.2.1:1234", "http://localhost:8088", "", 403},
		{"cross-origin", "alice", "127.0.0.1:1234", "http://other.example", "", 403},
		{"proxy", "alice", "127.0.0.1:1234", "http://localhost:8088", "192.0.2.1", 403},
		{"missing-user", "missing", "127.0.0.1:1234", "http://localhost:8088", "", 404},
		{"local", "alice", "127.0.0.1:1234", "http://localhost:8088", "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGENTFLOW_LOCAL_LOGIN_USER", tc.configured)
			req := httptest.NewRequest(http.MethodPost, "http://localhost:8088/api/auth/local", nil)
			req.RemoteAddr = tc.remote
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("X-Forwarded-For", tc.forwarded)
			rec := httptest.NewRecorder()
			g.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if tc.status == 200 {
				cookies := rec.Result().Cookies()
				if len(cookies) == 0 {
					t.Fatal("missing session cookie")
				}
				user, err := s.getAuthClient().ParseSignedUser(cookies[0].Value)
				if err != nil || user.LoginName != "alice" || user.IsGuest {
					t.Fatalf("invalid session: %v", err)
				}
			}
		})
	}
}
