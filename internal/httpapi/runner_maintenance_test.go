package httpapi

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

func TestRunnerMaintenanceRequiresOwnerAndDispatchesToBoundRunner(t *testing.T) {
	h, owner := workbenchFixture(t)
	row := domain.Runner{ID: "maintenance-runner", OwnerUserID: owner.ID, RunnerVersion: domain.RunnerVersionCLISettings,
		Status: domain.RunnerStatusOnline, Runtimes: []domain.RunnerRuntime{
			{Provider: "codex", Capabilities: []string{domain.RuntimeCapabilityManagedUpdate}},
			{Provider: "claude-code"},
			{Provider: "cursor", Capabilities: []string{domain.RuntimeCapabilityNativeUpdate}},
		}}
	if err := h.repo.PutRunner(row); err != nil {
		t.Fatal(err)
	}
	hub := quietRunnerHub()
	conn := testRunnerConn(row.ID)
	registerTestConn(hub, conn)
	t.Cleanup(conn.close)
	handlers := &runnersHandlers{deps: h.deps, runnerControls: hub}

	peer := domain.User{ID: owner.ID + 100, LoginName: "maintenance-peer"}
	c, w := workbenchContext(peer, http.MethodPost, "/", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: row.ID}}
	handlers.runnerMaintenance(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-owner update returned %d", w.Code)
	}
	c, w = workbenchContext(owner, http.MethodPost, "/?provider=claude-code", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: row.ID}}
	handlers.runnerMaintenance(c)
	if w.Code != http.StatusConflict {
		t.Fatalf("unmanaged CLI update returned %d", w.Code)
	}

	for _, tc := range []struct {
		method, query, provider string
		statusOnly              bool
		want                    int
	}{
		{http.MethodGet, "", "", false, 200},
		{http.MethodPost, "", "", false, 202},
		{http.MethodPost, "?provider=codex", "codex", false, 202},
		{http.MethodPost, "?provider=cursor", "cursor", false, 202},
		{http.MethodGet, "?provider=codex&status=1", "codex", true, 200},
	} {
		c, w := workbenchContext(owner, tc.method, "/"+tc.query, bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: row.ID}}
		done := make(chan struct{})
		go func() { handlers.runnerMaintenance(c); close(done) }()
		request := answerOnce(t, hub, conn, domain.RunnerWorkspaceResponse{Status: 200, Payload: []byte(`{"status":"updating"}`)})
		<-done
		want := domain.RunnerMaintenanceRequest{Provider: tc.provider, Apply: tc.method == http.MethodPost, StatusOnly: tc.statusOnly}
		if request.Operation != domain.RunnerWorkspaceOperationMaintenance || request.Maintenance == nil || *request.Maintenance != want ||
			w.Code != tc.want || !strings.Contains(w.Body.String(), "updating") {
			t.Fatalf("%s %s: request=%+v status=%d %s", tc.method, tc.query, request.Maintenance, w.Code, w.Body)
		}
	}
}
