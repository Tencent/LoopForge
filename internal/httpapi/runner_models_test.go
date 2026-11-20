package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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
	request := answerOnce(t, hub, conn, domain.RunnerWorkspaceResponse{Status: 200, Payload: []byte(`[
		{"id":"default","name":"Default (recommended)"},
		{"id":"runner-model","discoveredReasoningEfforts":["xhigh","unsupported"]}
	]`)})
	<-done
	response := w.Result()
	if request.Operation != domain.RunnerWorkspaceOperationLoginModels || request.ModelProvider != "codex" ||
		response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("request=%+v response=%+v", request, response)
	}
	var catalog cliModelProbeResult
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil || len(catalog.Models) != 1 || catalog.Models[0].ID != "runner-model" {
		t.Fatalf("unexpected selectable models: %s (decode %v)", w.Body, err)
	}
	if got := catalog.Models[0].DiscoveredReasoningEfforts; got == nil || !slices.Equal(*got, []string{"xhigh"}) {
		t.Fatalf("runner metadata was not preserved and constrained: %s", w.Body)
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
	if got, err := h.resolveAssistantTurnModel(ctx, owner, model, "high", row); err != nil || got != "agy:claude-opus-5-5-high" {
		t.Errorf("advertised effort did not resolve to the runner variant: %q %v", got, err)
	}
	if _, err := h.resolveAssistantTurnModel(ctx, owner, model, "max", row); err == nil {
		t.Error("unadvertised effort accepted")
	}
	if err := validateAssistantEffort(ctx, owner, model, "high"); err == nil {
		t.Error("account-only effort accepted for API key execution")
	}
}

func TestAntigravityGenerationUsesLiveRunnerCatalog(t *testing.T) {
	h, owner := workbenchFixture(t)
	h.generate = func(context.Context, string, domain.User) (string, error) { return "test reply", nil }
	runner := domain.Runner{
		ID: "agy-live", OwnerUserID: owner.ID, Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionCLISettings,
		Runtimes: []domain.RunnerRuntime{{Provider: runtime.AgentCLIAntigravity, AuthMode: domain.RuntimeAuthLocal}},
	}
	if err := h.repo.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	hub := quietRunnerHub()
	h.runnerControls = hub
	conn := testRunnerConn(runner.ID)
	registerTestConn(hub, conn)
	defer conn.close()
	go func() {
		for {
			select {
			case <-conn.done:
				return
			case msg := <-conn.send:
				if msg.WorkspaceRequest != nil && msg.WorkspaceRequest.Operation == domain.RunnerWorkspaceOperationLoginModels {
					hub.resolveWorkspace(conn, msg.RequestID, domain.RunnerWorkspaceResponse{Status: 200, Payload: []byte(`[
						{"id":"gemini-3.8-flash-low"}, {"id":"gemini-3.8-flash-medium"}
					]`)})
				}
			}
		}
	}()
	probe := &runnersHandlers{deps: h.deps, runnerControls: hub}
	c, w := workbenchContext(owner, http.MethodPost, "/", bytes.NewBufferString(`{"provider":"agy","useSaved":true}`))
	c.Params = gin.Params{{Key: "id", Value: runner.ID}}
	probe.probeRunnerModels(c)
	var catalog cliModelProbeResult
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil || w.Code != http.StatusOK || catalog.Source != "runner" || len(catalog.Models) != 1 {
		t.Fatalf("probe returned %d: %s (decode %v)", w.Code, w.Body, err)
	}
	if levels := catalog.Models[0].DiscoveredReasoningEfforts; levels == nil || !slices.Equal(*levels, []string{"low", "medium"}) {
		t.Fatalf("unexpected runner model: %+v", catalog.Models[0])
	}

	request := assistantRequest{
		SessionID: "agy-live-turn", Model: "agy:" + catalog.Models[0].ID, Effort: "high",
		ExecutionTarget: &domain.AssistantExecutionTarget{Pool: domain.ExecutionModeRunner, RunnerID: runner.ID},
		Messages:        []assistantMessage{{Role: "user", Content: "hello"}},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	c, w = workbenchContext(owner, http.MethodPost, "/api/assistant/generations", bytes.NewBuffer(body))
	h.startGeneration(c)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "可选：默认、low、medium") {
		t.Fatalf("unadvertised effort returned %d: %s", w.Code, w.Body)
	}
	resolved, err := h.resolveAssistantTurnModel(t.Context(), owner, request.Model, "medium", runner)
	if err != nil || resolved != "agy:gemini-3.8-flash-medium" {
		t.Fatalf("advertised effort did not resolve to the runner variant: %q %v", resolved, err)
	}
	provider, modelID := assistantModelEngine(resolved)
	command := runtime.AgentCLICommand(provider, runtime.AgentCLICommandOptions{Model: modelID, Effort: "medium", RunnerLogin: true})
	if i := slices.Index(command.Argv, "--model"); i < 0 || i+1 == len(command.Argv) || command.Argv[i+1] != "gemini-3.8-flash-medium" {
		t.Fatalf("command did not use the advertised model ID: %v", command.Argv)
	}

	legacy := domain.AssistantSession{
		ID: "agy-live-legacy", Executor: runtime.AgentCLIAntigravity, Model: "agy:gemini-3.8-flash-high",
		ExecutionTarget: request.ExecutionTarget,
		Messages: []domain.AssistantMessage{
			{Role: "user", Content: "first message"},
			{Role: "assistant", Content: "first reply", Model: "agy:gemini-3.8-flash-high"},
		},
	}
	if err := h.repo.PutAssistantSession(owner.ID, legacy); err != nil {
		t.Fatal(err)
	}
	legacy, _ = h.repo.AssistantSession(owner.ID, legacy.ID)
	request.SessionID, request.Model, request.Effort = legacy.ID, legacy.Model, ""
	request.Messages = append(legacy.Messages, assistantMessage{Role: "user", Content: "continue"})
	body, err = json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	c, w = workbenchContext(owner, http.MethodPost, "/api/assistant/generations", bytes.NewBuffer(body))
	h.startGeneration(c)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "请重新选择模型") {
		t.Fatalf("retired legacy variant returned %d: %s", w.Code, w.Body)
	}
	if _, created := h.repo.AssistantGeneration(owner.ID, legacy.ID); created {
		t.Fatal("retired legacy variant created a generation")
	}
	if got, err := h.resolveAssistantTurnModel(t.Context(), owner, "agy:gemini-3.8-flash-medium", "", runner); err != nil || got != "agy:gemini-3.8-flash-medium" {
		t.Fatalf("available legacy variant was not preserved: %q %v", got, err)
	}
}
