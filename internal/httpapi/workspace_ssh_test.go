package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

func issueWorkspaceSSHTestToken(t *testing.T, store *repo.Store, userID int64) string {
	t.Helper()
	id := fmt.Sprintf("workspace-ssh-test-%d", userID)
	token := "aft_" + id
	sum := sha256.Sum256([]byte(token))
	if err := store.PutAPIToken(domain.APIToken{ID: id, UserID: userID, TokenHash: hex.EncodeToString(sum[:])}); err != nil {
		t.Fatal(err)
	}
	return token
}

func TestWorkspaceSSHChecksEditPermission(t *testing.T) {
	t.Setenv("AGENTFLOW_WORKSPACE_SSH_ENABLED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "desktop-ssh-test-secret")
	a, owner, peer, outsider, _ := sharedWorkbenchFixture(t)
	project, _ := a.repo.Project("workbench")
	for i := range project.Members {
		if project.Members[i].UserID == peer.ID {
			project.Members[i].Role = "viewer"
		}
	}
	if err := a.repo.PutProject(project); err != nil {
		t.Fatal(err)
	}
	h := &runsHandlers{deps: a.deps, workspaceSSH: newWorkspaceSSHRelay()}
	h.engine = engine.New(h.repo, runtime.NewManager(t.TempDir()))
	g := gin.New()
	g.GET("/api/runs/:id/workspace/ssh/host-key", h.workspaceSSHHostKey)
	g.GET("/api/runs/:id/workspace/ssh", h.connectWorkspaceSSH)

	for _, tc := range []struct {
		name, token string
		status      int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"viewer", issueWorkspaceSSHTestToken(t, h.repo, peer.ID), http.StatusForbidden},
		{"outsider", issueWorkspaceSSHTestToken(t, h.repo, outsider.ID), http.StatusForbidden},
		{"owner", issueWorkspaceSSHTestToken(t, h.repo, owner.ID), http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, suffix := range []string{"/host-key", ""} {
				request := httptest.NewRequest(http.MethodGet, "/api/runs/shared-run/workspace/ssh"+suffix, nil)
				if tc.token != "" {
					request.Header.Set("Authorization", "Bearer "+tc.token)
				}
				expected := tc.status
				if expected == http.StatusOK && suffix == "" {
					expected = http.StatusBadRequest // no WebSocket upgrade
				}
				w := httptest.NewRecorder()
				g.ServeHTTP(w, request)
				if w.Code != expected {
					t.Fatalf("%s: got %d, want %d: %s", suffix, w.Code, expected, w.Body)
				}
			}
		})
	}
}

func TestWorkspaceSSHRelayTicketIsSingleUseAndRunnerBound(t *testing.T) {
	relay := newWorkspaceSSHRelay()
	want := &workspaceSSHAttachment{runnerID: "runner-a"}
	relay.pending["test-ticket"] = want
	if relay.claim("test-ticket", "runner-b") != nil {
		t.Fatal("another runner claimed the tunnel")
	}
	if relay.claim("test-ticket", "runner-a") != want {
		t.Fatal("assigned runner did not receive the tunnel")
	}
	if relay.claim("test-ticket", "runner-a") != nil {
		t.Fatal("tunnel accepted the same ticket twice")
	}
}
