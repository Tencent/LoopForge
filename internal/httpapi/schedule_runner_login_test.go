package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestScheduleJobUsesSaversDefaultRunnerLogin(t *testing.T) {
	for _, cliEngine := range []string{assistantEngineClaude, runtime.AgentCLIAntigravity} {
		t.Run(cliEngine, func(t *testing.T) { testScheduleJobUsesSaversDefaultRunnerLogin(t, cliEngine) })
	}
}

func testScheduleJobUsesSaversDefaultRunnerLogin(t *testing.T, cliEngine string) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "schedule-runner-login-test")
	h, owner := workbenchFixture(t)
	st := h.repo
	if err := st.PutUser(domain.User{LoginName: "schedule-runner-saver"}); err != nil {
		t.Fatal(err)
	}
	saver, _ := st.UserByLoginName("schedule-runner-saver")
	joinScheduledProject(t, st, "workbench", saver.ID)
	machine := domain.Runner{ID: "saver-machine", OwnerUserID: saver.ID, Status: domain.RunnerStatusOnline,
		RunnerVersion: domain.RunnerVersionCLISettings, Runtimes: []domain.RunnerRuntime{
			{Provider: cliEngine, Status: domain.RuntimeStatusOnline, AuthMode: domain.RuntimeAuthLocal},
		},
	}
	foreign := machine
	foreign.ID, foreign.OwnerUserID = "owner-machine", owner.ID
	for _, runner := range []domain.Runner{machine, foreign} {
		if err := st.PutRunner(runner); err != nil {
			t.Fatal(err)
		}
	}
	setDefault := func(id string) {
		t.Helper()
		if err := st.SetDefaultRunner(saver.ID, id, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	server := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	body := domain.ScheduleJob{
		ID: "runner-login-job", ProjectID: "workbench", Name: "巡检", SkipDispatch: true, AssistantPrompt: "检查",
		AssistantModel: cliEngine + ":default", RunAsUserID: owner.ID, IntervalSec: 3600, Enabled: true,
	}
	setDefault(foreign.ID)
	if code := scheduleAuthzStatus(t, server, saver, http.MethodPut, "/api/schedule-jobs", body); code != http.StatusBadRequest {
		t.Fatalf("a default runner owned by someone else enabled a keyless save: %d", code)
	}
	setDefault(machine.ID)
	saved := requestJSONAuthed[domain.ScheduleJob](t, server, saver, http.MethodPut, "/api/schedule-jobs", body, http.StatusOK)
	if saved.RunAsUserID != saver.ID || saved.AssistantModel != body.AssistantModel {
		t.Fatalf("saved run-as identity = %d, model = %q", saved.RunAsUserID, saved.AssistantModel)
	}

	for _, test := range []struct {
		query string
		want  bool
	}{{"", false}, {"?schedule=1", true}} {
		c, rec := workbenchContext(saver, http.MethodGet, "/api/assistant/models"+test.query, &bytes.Buffer{})
		h.models(c)
		var catalog assistantModelCatalog
		if err := json.Unmarshal(rec.Body.Bytes(), &catalog); err != nil {
			t.Fatal(err)
		}
		hasEngine := slices.ContainsFunc(catalog.Models, func(m assistantModel) bool {
			return m.Engine == cliEngine
		})
		if rec.Code != http.StatusOK || hasEngine != test.want {
			t.Fatalf("catalog %q lists keyless %s models = %v, status = %d", test.query, cliEngine, hasEngine, rec.Code)
		}
	}

	h.runnerControls = quietRunnerHub()
	conn := testRunnerConn(machine.ID)
	registerTestConn(h.runnerControls, conn)
	t.Cleanup(conn.close)
	type outcome struct {
		message string
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		message, err := h.prepareScheduleJob(saved)
		done <- outcome{message, err}
	}()

	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	var launched, completed bool
	for !completed {
		select {
		case msg := <-conn.send:
			var req runtime.ConversationProcessRequest
			if msg.WorkspaceRequest == nil || json.Unmarshal(msg.WorkspaceRequest.Conversation, &req) != nil {
				t.Fatal("expected runner conversation request")
			}
			result := runtime.ConversationProcessResult{ID: req.ID}
			switch req.Operation {
			case "start":
				if req.Command == nil || !req.Command.RunnerLogin || req.Command.Credentials.ClaudeCode.APIKey != "" || req.Command.Credentials.AgentCLI[cliEngine].APIKey != "" {
					t.Fatal("scheduled turn did not request CLI account login")
				}
				launched = true
			case "poll":
				result.Events = []domain.TelemetryEvent{{Name: "process.stdout", Message: `{"type":"result","result":"巡检完成"}`}}
				if cliEngine == runtime.AgentCLIAntigravity {
					result.Events = []domain.TelemetryEvent{
						{Name: "process.stdout", Message: `{"event":"init","conversation_id":"schedule-agy","init":{"model":"gemini-4-hydrogen-high"}}`},
						{Name: "process.stdout", Message: `{"event":"result","result":{"status":"SUCCESS","response":"巡检完成"}}`},
					}
				}
				result.Sequence, result.Done = 1, true
			case "stop":
				result.Done = true
			}
			raw, _ := json.Marshal(result)
			h.runnerControls.resolveWorkspace(conn, msg.RequestID, domain.RunnerWorkspaceResponse{Status: http.StatusOK, Payload: raw})
		case result := <-done:
			if result.err != nil || result.message != "巡检完成" || !launched {
				t.Fatalf("scheduled run = %q, %v, launched = %v", result.message, result.err, launched)
			}
			completed = true
		case <-timeout.C:
			t.Fatal("scheduled run did not finish")
		}
	}

	sessions := st.ListAssistantSessionsByTask(domain.AssistantSourceScheduled, saved.ID)
	if len(sessions) != 1 || sessions[0].UserID != saver.ID || sessions[0].Execution().RunnerID != machine.ID || !sessions[0].ExecutionLocked() {
		t.Fatal("scheduled session did not retain the saver and their machine")
	}
	machine.Status = domain.RunnerStatusOffline
	if err := st.PutRunner(machine); err != nil {
		t.Fatal(err)
	}
	const saverKey = "saver-test-key"
	keyAttempts := 0
	var keyError error
	h.generate = func(_ context.Context, _ string, actor domain.User) (string, error) {
		keyAttempts++
		key := actor.ClaudeCodeAPIKey
		if cliEngine == runtime.AgentCLIAntigravity {
			key = actor.AgentCLICredentials[cliEngine].APIKey
		}
		if actor.ID != saver.ID || key != saverKey {
			return "", errors.New("fallback used the wrong credentials")
		}
		return "云端巡检完成", keyError
	}
	if _, err := h.prepareScheduleJob(saved); err == nil || !strings.Contains(err.Error(), "已离线。未配置 "+assistantEngineLabel(cliEngine)+" API Key") || keyAttempts != 0 {
		t.Fatalf("offline runner without a key = %v, attempts = %d", err, keyAttempts)
	}

	update := repo.UserCredentialUpdate{ClaudeCodeAPIKey: saverKey}
	if cliEngine == runtime.AgentCLIAntigravity {
		update = repo.UserCredentialUpdate{AgentCLICredentials: map[string]repo.AgentCLICredentialUpdate{cliEngine: {APIKey: saverKey}}}
	}
	if _, err := st.UpdateUserCredentials(saver.ID, update); err != nil {
		t.Fatal(err)
	}
	if message, err := h.prepareScheduleJob(saved); err != nil || message != "云端巡检完成" || keyAttempts != 1 {
		t.Fatalf("offline fallback = %q, %v, attempts = %d", message, err, keyAttempts)
	}
	// Schedule jobs keep every execution's conversation, so the fallback run
	// opens a second one next to the earlier runner conversation.
	runnerSession := sessions[0].ID
	sessions = st.ListAssistantSessionsByTask(domain.AssistantSourceScheduled, saved.ID)
	cloud := slices.IndexFunc(sessions, func(s domain.AssistantSession) bool { return s.ID != runnerSession })
	if len(sessions) != 2 || cloud < 0 || sessions[cloud].UserID != saver.ID ||
		sessions[cloud].Execution().Pool != domain.ExecutionModeCloud || sessions[cloud].Model != saved.AssistantModel {
		t.Fatal("offline fallback did not run the same model and user in the cloud")
	}

	keyError = assistantCredentialError{engine: cliEngine}
	if _, err := h.prepareScheduleJob(saved); err == nil || !strings.Contains(err.Error(), "已离线") ||
		!strings.Contains(err.Error(), assistantEngineLabel(cliEngine)+" 拒绝了你的 API Key") || keyAttempts != 2 {
		t.Fatalf("offline fallback with rejected key = %v, attempts = %d", err, keyAttempts)
	}

	machine.Status = domain.RunnerStatusOnline
	machine.RunnerVersion = domain.RunnerVersionConversationCLI
	if err := st.PutRunner(machine); err != nil {
		t.Fatal(err)
	}
	keyError = nil
	if message, err := h.prepareScheduleJob(saved); err != nil || message != "云端巡检完成" || keyAttempts != 3 {
		t.Fatalf("fallback with a Runner from main = %q, %v, attempts = %d", message, err, keyAttempts)
	}
}
