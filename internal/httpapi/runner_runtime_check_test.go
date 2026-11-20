package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestRunnerCLITestUsesOwnerLoginAndCleansUp(t *testing.T) {
	h, owner := workbenchFixture(t)
	runner := domain.Runner{ID: "test-laptop", OwnerUserID: owner.ID, Status: domain.RunnerStatusOnline,
		RunnerVersion: domain.RunnerVersionCLISettings, Runtimes: []domain.RunnerRuntime{{Provider: "claude-code", AuthMode: "local"}}}
	if err := h.repo.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	h.runnerControls = quietRunnerHub()
	conn := testRunnerConn(runner.ID)
	registerTestConn(h.runnerControls, conn)
	t.Cleanup(conn.close)
	runners := &runnersHandlers{deps: h.deps, assistant: h}
	body := `{"runnerId":"test-laptop","provider":"claude-code"}`

	foreign := domain.User{ID: owner.ID + 1, LoginName: "other"}
	c, rec := workbenchContext(foreign, http.MethodPost, "/api/runners/runtimes/test", bytes.NewBufferString(body))
	runners.runnerRuntimeTest(c)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("other user could test the runner: %d %s", rec.Code, rec.Body)
	}
	c, rec = workbenchContext(owner, http.MethodPost, "/api/runners/runtimes/test", bytes.NewBufferString(body))
	done := make(chan struct{})
	go func() {
		defer close(done)
		runners.runnerRuntimeTest(c)
	}()
	var started bool
	var discarded bool
	for !discarded {
		select {
		case msg := <-conn.send:
			var req runtime.ConversationProcessRequest
			if msg.WorkspaceRequest == nil || json.Unmarshal(msg.WorkspaceRequest.Conversation, &req) != nil ||
				!req.Transient || msg.WorkspaceRequest.RunID != "" {
				t.Fatalf("probe requested an ordinary workspace: %+v", msg)
			}
			result := runtime.ConversationProcessResult{ID: req.ID}
			switch req.Operation {
			case "start":
				if req.Command == nil || !req.Command.RunnerLogin || req.Command.Credentials.ClaudeCode.APIKey != "" {
					t.Fatal("probe failed to select runner login without a personal key")
				}
				if !strings.Contains(strings.Join(req.Command.Argv, " "), "Respond with OK.") {
					t.Fatal("probe did not launch a model request")
				}
				started = true
			case "poll":
				result.Events = []domain.TelemetryEvent{{Name: "process.stdout", Message: `{"type":"result","result":"OK"}`}}
				result.Sequence, result.Done = 1, true
			case "stop":
				result.Done = true
			case "discard":
				discarded = true
			case "prepare", "resetAssets", "sync":
			default:
				t.Fatalf("unexpected runner operation %q", req.Operation)
			}
			raw, _ := json.Marshal(result)
			h.runnerControls.resolveWorkspace(conn, msg.RequestID, domain.RunnerWorkspaceResponse{Status: 200, Payload: raw})
		case <-done:
			t.Fatalf("test ended before runner cleanup: %d %s", rec.Code, rec.Body)
		case <-time.After(5 * time.Second):
			t.Fatal("probe stopped making progress")
		}
	}
	<-done
	if !started || rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("probe failed: %d %s", rec.Code, rec.Body)
	}
}
