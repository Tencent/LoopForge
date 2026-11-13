package httpapi

import (
	"bytes"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"github.com/gin-gonic/gin"
)

// steerFixture opens a running generation on the user's dsh CLI and returns
// the handler plus the stored user.
func steerFixture(t *testing.T) (*assistantHandlers, domain.User) {
	t.Helper()
	h, u := workbenchFixture(t)
	baseURL, protocol := "https://dsh.example/v1", domain.AgentCLIProtocolChat
	updated, err := h.repo.UpdateUserCredentials(u.ID, repo.UserCredentialUpdate{AgentCLICredentials: map[string]repo.AgentCLICredentialUpdate{
		"dsh": {APIKey: "dsh-test-key", BaseURL: &baseURL, Protocol: &protocol},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return h, updated
}

func startSteerGeneration(t *testing.T, h *assistantHandlers, u domain.User, session assistantConversation) {
	t.Helper()
	g := domain.AssistantGeneration{ID: "gen-cli", SessionID: session.ID, UserID: u.ID, Status: "running", StartedAt: time.Now().UTC()}
	if err := h.repo.BeginAssistantGeneration(g, session); err != nil {
		t.Fatal(err)
	}
}

func postSteer(t *testing.T, h *assistantHandlers, u domain.User, sessionID, body string) *httptestResponse {
	t.Helper()
	c, rec := workbenchContext(u, "POST", "/api/assistant/sessions/"+sessionID+"/generation/steer?generationId=gen-cli", bytes.NewBufferString(body))
	c.Params = gin.Params{{Key: "id", Value: sessionID}}
	h.steerGeneration(c)
	return &httptestResponse{code: rec.Code, body: rec.Body.String()}
}

type httptestResponse struct {
	code int
	body string
}

// A steer sent while the model is still thinking must keep the CLI the
// conversation runs on. It used to be filtered through the CodeBuddy catalog
// only, so a "dsh:<model>" pin silently turned into the CodeBuddy default
// model and the follow-up turn ran on the CodeBuddy key.
func TestSteerKeepsTheAgentCLIModel(t *testing.T) {
	h, u := steerFixture(t)
	session := assistantConversation{
		ID:       "steer-cli",
		Model:    "dsh:deepseek-v4",
		Executor: "dsh",
		Messages: []assistantMessage{{Role: "user", Content: "先看代码", AuthorID: u.ID, AuthorName: u.LoginName}},
	}
	startSteerGeneration(t, h, u, session)
	if response := postSteer(t, h, u, session.ID, `{"content":"换个方向","model":"dsh:deepseek-v4"}`); response.code != 200 {
		t.Fatalf("steer: %d %s", response.code, response.body)
	}
	stored, ok := h.repo.AssistantGeneration(u.ID, session.ID)
	if !ok || stored.Steer == nil {
		t.Fatalf("steer was not recorded: %+v", stored)
	}
	if stored.Steer.Model != "dsh:deepseek-v4" {
		t.Fatalf("steer model = %q, want dsh:deepseek-v4 (CodeBuddy fallback)", stored.Steer.Model)
	}
}

// A steer that carries no model (an older page, an API client) continues on
// the conversation's own model instead of the CodeBuddy default.
func TestSteerWithoutModelKeepsTheConversationModel(t *testing.T) {
	h, u := steerFixture(t)
	session := assistantConversation{
		ID:       "steer-cli-empty",
		Model:    "dsh:deepseek-v4",
		Executor: "dsh",
		Messages: []assistantMessage{{Role: "user", Content: "先看代码", AuthorID: u.ID, AuthorName: u.LoginName}},
	}
	startSteerGeneration(t, h, u, session)
	if response := postSteer(t, h, u, session.ID, `{"content":"换个方向"}`); response.code != 200 {
		t.Fatalf("steer: %d %s", response.code, response.body)
	}
	stored, _ := h.repo.AssistantGeneration(u.ID, session.ID)
	if stored.Steer == nil || stored.Steer.Model != "dsh:deepseek-v4" {
		t.Fatalf("steer model = %+v, want dsh:deepseek-v4", stored.Steer)
	}
}

// Steering cannot move the conversation onto another CLI: the native session
// it resumes holds tool calls and reasoning no other CLI can continue.
func TestSteerRefusesAnEngineSwitch(t *testing.T) {
	h, u := steerFixture(t)
	session := assistantConversation{
		ID:       "steer-switch",
		Model:    "dsh:deepseek-v4",
		Executor: "dsh",
		Messages: []assistantMessage{
			{Role: "user", Content: "先看代码", AuthorID: u.ID, AuthorName: u.LoginName},
			{Role: "assistant", Content: `{"message":"看完了"}`, Model: "dsh:deepseek-v4"},
		},
	}
	startSteerGeneration(t, h, u, session)
	response := postSteer(t, h, u, session.ID, `{"content":"换个方向","model":"deepseek-v4.1"}`)
	if response.code != 409 {
		t.Fatalf("steer onto another CLI: %d %s", response.code, response.body)
	}
	if stored, _ := h.repo.AssistantGeneration(u.ID, session.ID); stored.Steer != nil {
		t.Fatalf("refused steer was recorded: %+v", stored.Steer)
	}
}
