package httpapi

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"github.com/gin-gonic/gin"
)

func TestSteerKeepsConversationCLI(t *testing.T) {
	for _, tc := range []struct {
		name, executor, savedModel, requested, wantModel string
		runnerLogin, missingKey                          bool
		wantStatus                                       int
	}{
		{"codex login prefixed", "codex", "codex:gpt-old", "codex:gpt-selected", "codex:gpt-selected", true, false, 200},
		{"codex login unprefixed", "codex", "codex:gpt-old", "gpt-selected", "codex:gpt-selected", true, false, 200},
		{"codex login omitted", "codex", "codex:gpt-old", "", "codex:gpt-old", true, false, 200},
		{"codex legacy", "codex", "gpt-old", "gpt-selected", "codex:gpt-selected", true, false, 200},
		{"codex key", "codex", "codex:gpt-old", "gpt-selected", "codex:gpt-selected", false, false, 200},
		{"cursor prefixed", "cursor", "cursor:old", "cursor:selected", "cursor:selected", false, false, 200},
		{"cursor unprefixed", "cursor", "cursor:old", "selected", "cursor:selected", false, false, 200},
		{"cursor legacy", "cursor", "old", "selected", "cursor:selected", false, false, 200},
		{"codebuddy", "codebuddy", "default", "default", "default", false, false, 200},
		{"codex missing key", "codex", "codex:gpt-old", "gpt-selected", "", false, true, 400},
		{"cursor missing key", "cursor", "cursor:old", "selected", "", false, true, 400},
		{"codex to cursor", "codex", "codex:gpt-old", "cursor:selected", "", true, false, 409},
		{"cursor to codex", "cursor", "cursor:old", "codex:gpt-selected", "", false, false, 409},
		{"codex to codebuddy", "codex", "codex:gpt-old", "codebuddy:default", "", true, false, 409},
		{"codebuddy to codex", "codebuddy", "default", "codex:gpt-selected", "", false, false, 409},
		{"invalid codex model", "codex", "codex:gpt-old", "codex:bad model", "", true, false, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, user := workbenchFixture(t)
			stubCLIModelDiscovery(t)
			credentials := repo.UserCredentialUpdate{CodeBuddyAPIKeyClear: tc.executor != "codebuddy"}
			if !tc.missingKey && !tc.runnerLogin {
				credentials.CodexAPIKey = "test-codex"
				credentials.AgentCLICredentials = map[string]repo.AgentCLICredentialUpdate{"cursor": {APIKey: "test-cursor"}}
			}
			var err error
			user, err = h.repo.UpdateUserCredentials(user.ID, credentials)
			if err != nil {
				t.Fatal(err)
			}
			target := domain.AssistantExecutionTarget{Pool: domain.ExecutionModeCloud}
			if tc.runnerLogin {
				runner := domain.Runner{ID: "login-runner", OwnerUserID: user.ID, Runtimes: []domain.RunnerRuntime{{Provider: "codex", AuthMode: domain.RuntimeAuthLocal}}}
				if err := h.repo.PutRunner(runner); err != nil {
					t.Fatal(err)
				}
				target = domain.AssistantExecutionTarget{Pool: domain.ExecutionModeRunner, RunnerID: runner.ID}
			}
			session := domain.AssistantSession{ID: "steer-cli", Executor: tc.executor, Model: tc.savedModel, ExecutionTarget: &target, Messages: []assistantMessage{
				{Role: "user", Content: "first"},
				{Role: "assistant", Content: "first reply", Model: tc.savedModel},
				{Role: "user", Content: "continue"},
			}}
			g := domain.AssistantGeneration{ID: "running-turn", SessionID: session.ID, UserID: user.ID, Status: "running", MessageCount: len(session.Messages)}
			if err := h.repo.BeginAssistantGeneration(g, session); err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(domain.AssistantSteer{Content: "redirect", Model: tc.requested})
			c, rec := workbenchContext(user, "POST", "/api/assistant/sessions/steer-cli/generation/steer?generationId="+g.ID, bytes.NewBuffer(body))
			c.Params = gin.Params{{Key: "id", Value: session.ID}}
			h.steerGeneration(c)
			if rec.Code != tc.wantStatus {
				t.Fatalf("steer: %d %s, want %d", rec.Code, rec.Body, tc.wantStatus)
			}
			current, _ := h.repo.AssistantGeneration(user.ID, session.ID)
			if tc.wantStatus != 200 {
				if current.CancelRequested || current.Steer != nil || current.Status != "running" {
					t.Fatalf("rejected request interrupted the turn: %+v", current)
				}
				if tc.wantStatus == 409 && !strings.Contains(rec.Body.String(), "不能中途切换 CLI") {
					t.Fatalf("missing switch refusal: %s", rec.Body)
				}
				return
			}
			if !current.CancelRequested || current.Steer == nil || current.Steer.Model != tc.wantModel {
				t.Fatalf("steer: %+v, want model %q", current.Steer, tc.wantModel)
			}
		})
	}
}
