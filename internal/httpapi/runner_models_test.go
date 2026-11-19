package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"

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

func TestAntigravityRunnerModelProbeGroupsAdvertisedEfforts(t *testing.T) {
	h, owner := workbenchFixture(t)
	provider := runtime.AgentCLIAntigravity
	row := domain.Runner{
		ID: "agy-models", OwnerUserID: owner.ID, Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionCLISettings,
		Runtimes: []domain.RunnerRuntime{{Provider: provider, AuthMode: domain.RuntimeAuthLocal}},
	}
	if err := h.repo.PutRunner(row); err != nil {
		t.Fatal(err)
	}
	hub := quietRunnerHub()
	conn := testRunnerConn(row.ID)
	registerTestConn(hub, conn)
	t.Cleanup(conn.close)
	handlers := &runnersHandlers{deps: h.deps, runnerControls: hub}
	probe := func(body string) (*gin.Context, *httptest.ResponseRecorder) {
		c, w := workbenchContext(owner, http.MethodPost, "/", bytes.NewBufferString(body))
		c.Params = gin.Params{{Key: "id", Value: row.ID}}
		return c, w
	}

	payload := []byte(`[
		{"id":"claude-opus-5-5-low","name":"Claude Opus 5.5 (Low)"},
		{"id":"claude-opus-5-5-medium","name":"Claude Opus 5.5 (Medium)"},
		{"id":"claude-opus-5-5-high","name":"Claude Opus 5.5 (High)"},
		{"id":"claude-sonnet-5-5-high","name":"Claude Sonnet 5.5 (High)"},
		{"id":"claude-sonnet-5-5-max","name":"Claude Sonnet 5.5 (Max)"}
	]`)
	check := func(w *httptest.ResponseRecorder, source string) {
		t.Helper()
		var result cliModelProbeResult
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != http.StatusOK || result.Source != source || len(result.Models) != 2 {
			t.Fatalf("probe returned %d: %s (decode %v)", w.Code, w.Body, err)
		}
		opus, sonnet := result.Models[0], result.Models[1]
		if opus.ID != "claude-opus-5-5-medium" || opus.Name != "Claude Opus 5.5" || opus.DefaultEffort != "medium" ||
			opus.DiscoveredReasoningEfforts == nil || !slices.Equal(*opus.DiscoveredReasoningEfforts, []string{"low", "medium", "high"}) ||
			!maps.Equal(opus.Efforts, map[string]string{"low": "claude-opus-5-5-low", "medium": "claude-opus-5-5-medium", "high": "claude-opus-5-5-high"}) {
			t.Errorf("unexpected Opus family: %+v", opus)
		}
		if sonnet.ID != "claude-sonnet-5-5-high" || sonnet.Name != "Claude Sonnet 5.5" || sonnet.DefaultEffort != "high" ||
			sonnet.DiscoveredReasoningEfforts == nil || !slices.Equal(*sonnet.DiscoveredReasoningEfforts, []string{"high", "max"}) {
			t.Errorf("unexpected Sonnet family: %+v", sonnet)
		}
	}
	c, w := probe(`{"provider":"agy","useSaved":true}`)
	done := make(chan struct{})
	go func() { handlers.probeRunnerModels(c); close(done) }()
	request := answerOnce(t, hub, conn, domain.RunnerWorkspaceResponse{Status: 200, Payload: payload})
	<-done
	if request.ModelProvider != provider {
		t.Fatalf("unexpected runner request: %+v", request)
	}
	check(w, "runner")

	var existing []domain.CLIModel
	if err := json.Unmarshal(payload, &existing); err != nil {
		t.Fatal(err)
	}
	owner, err := h.repo.UpdateUserCredentials(owner.ID, repo.UserCredentialUpdate{
		AgentCLICredentials: map[string]repo.AgentCLICredentialUpdate{provider: {Models: &existing}},
	})
	if err != nil {
		t.Fatal(err)
	}
	row.Status = domain.RunnerStatusOffline
	if err := h.repo.PutRunner(row); err != nil {
		t.Fatal(err)
	}
	c, w = probe(`{"provider":"agy","useSaved":true}`)
	handlers.probeRunnerModels(c)
	check(w, "saved")

	ctx := context.Background()
	const model = "agy:claude-opus-5-5-medium"
	if err := validateAssistantTurnEffort(ctx, owner, model, "high", true); err != nil {
		t.Errorf("advertised effort rejected: %v", err)
	}
	if err := validateAssistantTurnEffort(ctx, owner, model, "max", true); err == nil {
		t.Error("unadvertised effort accepted")
	}
	if err := validateAssistantTurnEffort(ctx, owner, model, "high", false); err == nil {
		t.Error("account-only effort accepted for API key execution")
	}
}
