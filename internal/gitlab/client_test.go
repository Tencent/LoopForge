package gitlab

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentflow/internal/gitwork"
)

func TestGitLabRejectsPrivateDestinations(t *testing.T) {
	t.Setenv("AGENTFLOW_GITLAB_PRIVATE_HOSTS", "other.example.test")
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "[::1]", "[::ffff:127.0.0.1]"} {
		err := NewClient("https://"+address, nil).VerifyToken(t.Context(), "fixture-pat")
		if !errors.Is(err, gitwork.ErrPrivateHTTPSDestination) {
			t.Errorf("VerifyToken for %s = %v, want private destination error", address, err)
		}
	}
}

func TestVerifyTokenWithoutSelfEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		self, user, projects int
		wantError            HTTPError
	}{
		{"missing endpoint", 404, 200, 200, 0},
		{"restricted endpoint", 403, 200, 200, 0},
		{"invalid token", 401, 401, 200, 401},
		{"missing repository search permission", 403, 200, 403, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("PRIVATE-TOKEN") != "test-pat" {
					t.Error("GitLab request omitted the PAT")
				}
				switch r.URL.Path {
				case "/api/v4/personal_access_tokens/self":
					w.WriteHeader(tc.self)
				case "/api/v4/user":
					w.WriteHeader(tc.user)
					fmt.Fprint(w, `{"id":42}`)
				case "/api/v4/projects":
					if tc.user != 200 || r.URL.Query().Get("membership") != "true" || r.URL.Query().Get("per_page") != "1" {
						t.Error("unexpected repository permission probe")
					}
					w.WriteHeader(tc.projects)
					fmt.Fprint(w, `[]`)
				default:
					t.Errorf("unexpected endpoint %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			err := NewClient(server.URL, server.Client().Transport).VerifyToken(t.Context(), "test-pat")
			if (tc.wantError == 0 && err != nil) || (tc.wantError != 0 && err != tc.wantError) {
				t.Fatalf("VerifyToken() = %v, want status %d", err, tc.wantError)
			}
		})
	}
}
