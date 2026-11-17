package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

func TestRunnerModelProbeUsesOwnersSelectedMachine(t *testing.T) {
	h, owner := workbenchFixture(t)
	row := domain.Runner{
		ID: "models-runner", OwnerUserID: owner.ID, Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionCLISettings,
		Runtimes: []domain.RunnerRuntime{{Provider: "codex"}, {Provider: "claude-code", AuthMode: domain.RuntimeAuthPersonal}},
	}
	if err := h.repo.PutRunner(row); err != nil {
		t.Fatal(err)
	}
	hub := quietRunnerHub()
	conn := testRunnerConn(row.ID)
	registerTestConn(hub, conn)
	t.Cleanup(conn.close)
	handlers := &runnersHandlers{deps: h.deps, runnerControls: hub}
	probe := func(user domain.User, provider string) (*gin.Context, *httptest.ResponseRecorder) {
		c, w := workbenchContext(user, http.MethodPost, "/", bytes.NewBufferString(`{"provider":"`+provider+`"}`))
		c.Params = gin.Params{{Key: "id", Value: row.ID}}
		return c, w
	}
	for _, check := range []struct {
		user     domain.User
		provider string
		status   int
	}{
		{domain.User{ID: owner.ID + 100, LoginName: "other"}, "codex", http.StatusForbidden},
		{owner, "claude-code", http.StatusConflict},
		{owner, "cursor", http.StatusConflict},
	} {
		c, w := probe(check.user, check.provider)
		handlers.probeRunnerModels(c)
		if w.Code != check.status {
			t.Fatalf("%s returned %d, want %d", check.provider, w.Code, check.status)
		}
	}
	c, w := probe(owner, "codex")
	done := make(chan struct{})
	go func() { handlers.probeRunnerModels(c); close(done) }()
	request := answerOnce(t, hub, conn, domain.RunnerWorkspaceResponse{Status: 200, Payload: []byte(`[{"id":"runner-model"}]`)})
	<-done
	response := w.Result()
	if request.Operation != domain.RunnerWorkspaceOperationLoginModels || request.ModelProvider != "codex" ||
		response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("request=%+v response=%+v", request, response)
	}
}
