package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestScheduleJobEffortValidationAndPersistence(t *testing.T) {
	h, owner := workbenchFixture(t)
	models := []domain.CLIModel{{ID: "opus", Efforts: map[string]string{"low": "opus-low", "high": "opus-high"}, DefaultEffort: "low"}}
	if _, err := h.repo.UpdateUserCredentials(owner.ID, repo.UserCredentialUpdate{AgentCLICredentials: map[string]repo.AgentCLICredentialUpdate{
		"cursor": {APIKey: "test-only", Models: &models},
	}}); err != nil {
		t.Fatal(err)
	}
	server := New(h.repo, engine.New(h.repo, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	body := map[string]any{
		"name": "Inspect", "projectId": "workbench", "skipDispatch": true, "intervalSec": 3600,
		"assistantPrompt": "Inspect", "assistantModel": "cursor:opus", "assistantEffort": " high ",
	}
	saved := requestJSON[domain.ScheduleJob](t, server, http.MethodPost, "/api/schedule-jobs", body, http.StatusOK)
	stored, _ := h.repo.ScheduleJob(saved.ID)
	if saved.AssistantEffort != "high" || stored.AssistantEffort != "high" {
		t.Fatalf("effort: response=%q stored=%q", saved.AssistantEffort, stored.AssistantEffort)
	}
	body["id"] = saved.ID
	// Cursor accepts medium in general, but this owner's model does not.
	body["assistantEffort"] = "medium"
	refused := requestJSON[map[string]string](t, server, http.MethodPut, "/api/schedule-jobs", body, http.StatusBadRequest)
	if !strings.Contains(refused["error"], "思考强度") {
		t.Fatalf("unsupported effort error = %q", refused["error"])
	}
	if stored, _ := h.repo.ScheduleJob(saved.ID); stored.AssistantEffort != "high" {
		t.Fatal("rejected update changed the saved effort")
	}
	delete(body, "assistantEffort")
	cleared := requestJSON[domain.ScheduleJob](t, server, http.MethodPut, "/api/schedule-jobs", body, http.StatusOK)
	if cleared.AssistantEffort != "" {
		t.Fatalf("default effort = %q", cleared.AssistantEffort)
	}
	if err := h.repo.PutWorkflow(domain.WorkflowDefinition{ID: "effort-flow", Name: "Effort", SandboxProfileID: "local"}); err != nil {
		t.Fatal(err)
	}
	dispatch := requestJSON[domain.ScheduleJob](t, server, http.MethodPost, "/api/schedule-jobs", map[string]any{
		"projectId": "workbench", "workflowId": "effort-flow", "assistantEffort": "high",
	}, http.StatusOK)
	if dispatch.AssistantEffort != "" {
		t.Fatal("a dispatch job kept an assistant effort")
	}
}

func TestScheduledAssistantEffortReachesCLI(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_MODELS", assistantChatDefaultModel)
	for _, mode := range []string{"", domain.ScheduleSessionFixed} {
		t.Run("session-"+mode, func(t *testing.T) {
			h, owner := workbenchFixture(t)
			_, calls := nativeCLIRecorder(t)
			job := domain.ScheduleJob{ID: "effort-job", Name: "Inspect", ProjectID: "workbench", OwnerUserID: owner.ID, AssistantPrompt: "Inspect", AssistantModel: assistantChatDefaultModel, SessionMode: mode}
			for _, effort := range []string{"high", "max", ""} {
				before := len(calls())
				job.AssistantEffort = effort
				_, sessionID, err := h.runScheduleJobPrompt(job)
				if err != nil {
					t.Fatal(err)
				}
				got := calls()
				if len(got) != before+1 || got[before].flag("--effort") != effort {
					t.Fatalf("effort %q did not reach the CLI: %#v", effort, got)
				}
				session, _ := h.repo.AssistantSession(owner.ID, sessionID)
				if session.Effort != effort || session.TaskID != job.ID {
					t.Fatalf("scheduled session did not keep effort %q: %+v", effort, session)
				}
				if mode == domain.ScheduleSessionFixed {
					if job.FixedSessionID != "" && sessionID != job.FixedSessionID {
						t.Fatal("changing effort replaced the fixed conversation")
					}
					job.FixedSessionID = sessionID
				}
			}
			before := len(calls())
			task := assistantTask{ID: "invalid-effort", ProjectID: "workbench", Model: assistantChatDefaultModel, Prompt: "Inspect", Effort: "ultra"}
			if err := h.executeAssistantTask(context.Background(), owner, &task); err == nil || !strings.Contains(task.LastError, "思考强度") {
				t.Fatalf("unavailable effort was not reported: %v %q", err, task.LastError)
			}
			if len(calls()) != before || task.SessionID != "" {
				t.Fatal("an unavailable effort started a CLI or conversation")
			}
		})
	}
}

func TestScheduleJobEffortUsesSaversCatalog(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "schedule-effort-test-secret")
	h, _ := workbenchFixture(t)
	if err := h.repo.PutUser(domain.User{LoginName: "effort-saver", AgentCLICredentials: map[string]domain.AgentCLICredential{
		"cursor": {APIKey: "test-only", Models: []domain.CLIModel{{ID: "custom", Efforts: map[string]string{"low": "custom-low"}, DefaultEffort: "low"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	saver, _ := h.repo.UserByLoginName("effort-saver")
	joinScheduledProject(t, h.repo, "workbench", saver.ID)
	server := New(h.repo, engine.New(h.repo, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	body := map[string]any{"projectId": "workbench", "name": "Inspect", "skipDispatch": true, "assistantPrompt": "Inspect", "assistantModel": "cursor:custom", "assistantEffort": "high"}
	refused := requestJSONAuthed[map[string]string](t, server, saver, http.MethodPost, "/api/schedule-jobs", body, http.StatusBadRequest)
	if !strings.Contains(refused["error"], "思考强度") {
		t.Fatalf("unsupported saver effort: %q", refused["error"])
	}
	body["assistantEffort"] = "low"
	saved := requestJSONAuthed[domain.ScheduleJob](t, server, saver, http.MethodPost, "/api/schedule-jobs", body, http.StatusOK)
	if saved.RunAsUserID != saver.ID || saved.AssistantEffort != "low" {
		t.Fatalf("effort did not use the saver's catalog: %+v", saved)
	}
}
