package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"agentflow/internal/domain"
)

func TestAntigravityRunnerLoginRequiresMachineOwner(t *testing.T) {
	h, owner := workbenchFixture(t)
	row := domain.Runner{ID: "agy-laptop", OwnerUserID: owner.ID, Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionCLISettings, Runtimes: []domain.RunnerRuntime{{Provider: "agy", AuthMode: domain.RuntimeAuthLocal}}}
	if err := h.repo.PutRunner(row); err != nil {
		t.Fatal(err)
	}
	target := domain.AssistantExecutionTarget{Pool: domain.ExecutionModeRunner, RunnerID: row.ID}
	if !h.assistantUsesRunnerLogin(owner.ID, &target, "agy") {
		t.Fatal("machine owner could not reuse Antigravity login")
	}
	if h.assistantUsesRunnerLogin(owner.ID+1, &target, "agy") {
		t.Fatal("shared conversation member could use another user's login")
	}
	if h.assistantUsesRunnerLogin(owner.ID, &domain.AssistantExecutionTarget{Pool: domain.ExecutionModeCloud}, "agy") {
		t.Fatal("cloud target inherited a personal machine login")
	}
	called := false
	h.generate = func(context.Context, string, domain.User) (string, error) { called = true; return "account reply", nil }
	request := assistantRequest{SessionID: "agy-login", Executor: "agy", Model: "gemini-4-hydrogen-high", Messages: []assistantMessage{{Role: "user", Content: "hello"}}, ExecutionTarget: &target}
	body, _ := json.Marshal(request)
	c, rec := workbenchContext(owner, http.MethodPost, "/api/assistant/chat", bytes.NewBuffer(body))
	h.runnerControls = quietRunnerHub()
	conn := testRunnerConn(row.ID)
	registerTestConn(h.runnerControls, conn)
	defer conn.close()
	done := make(chan struct{})
	go func() { h.chat(c); close(done) }()
	lookup := answerOnce(t, h.runnerControls, conn, domain.RunnerWorkspaceResponse{Status: 200, Payload: []byte(`[{"id":"gemini-4-hydrogen-high"}]`)})
	<-done
	if lookup.Operation != domain.RunnerWorkspaceOperationLoginModels || lookup.ModelProvider != "agy" {
		t.Fatalf("unexpected account discovery: %+v", lookup)
	}
	if rec.Code != http.StatusOK || !called {
		t.Fatalf("account login was blocked by the API-key preflight: %d %s", rec.Code, rec.Body)
	}
	row.Runtimes[0].AuthMode = domain.RuntimeAuthPersonal
	if err := h.repo.PutRunner(row); err != nil {
		t.Fatal(err)
	}
	if h.assistantUsesRunnerLogin(owner.ID, &target, "agy") {
		t.Fatal("explicit API-key mode was ignored")
	}
}

func TestAntigravityRuntimeRegistrationKeepsAccountPreference(t *testing.T) {
	reported := []domain.RunnerRuntime{{Provider: "agy", AuthMode: domain.RuntimeAuthPersonal}, {Provider: "cursor", AuthMode: domain.RuntimeAuthLocal}}
	fresh := normalizeRunnerRuntimes(reported, nil)
	if fresh[0].AuthMode != domain.RuntimeAuthLocal || fresh[1].AuthMode != domain.RuntimeAuthPersonal {
		t.Fatalf("initial authentication modes: %+v", fresh)
	}
	for _, mode := range []string{domain.RuntimeAuthLocal, domain.RuntimeAuthPersonal} {
		existing := []domain.RunnerRuntime{{Provider: "agy", AuthMode: mode}}
		if normalized := normalizeRunnerRuntimes(reported, existing); normalized[0].AuthMode != mode {
			t.Fatalf("saved mode %s was overwritten", mode)
		}
	}
}
