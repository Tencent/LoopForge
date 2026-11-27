package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

func TestRecentSessionViewReturnsOnlyTheTail(t *testing.T) {
	h, owner := workbenchFixture(t)
	messages := make([]domain.AssistantMessage, 45)
	for i := range messages {
		messages[i] = domain.AssistantMessage{Role: "user", Content: fmt.Sprintf("turn-%d", i)}
	}
	messages[0].Content = `OLDEST-TURN {"role":"user","content":"not-a-boundary"}`
	messages[len(messages)-1].Content = "LATEST-TURN"
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{
		ID: "long-chat", Title: "长会话", ParticipantsVersion: 1,
		Participants: []domain.AssistantParticipant{{ID: owner.ID, Name: "workbench-owner"}},
		Messages:     messages,
	}); err != nil {
		t.Fatal(err)
	}

	c, rec := workbenchContext(owner, "GET", "/api/assistant/sessions/long-chat?view=recent", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: "long-chat"}}
	h.session(c)
	if rec.Code != 200 {
		t.Fatalf("recent status %d: %s", rec.Code, rec.Body.String())
	}
	var view struct {
		Messages        []domain.AssistantMessage     `json:"messages"`
		MessagesPartial bool                          `json:"messagesPartial"`
		Participants    []domain.AssistantParticipant `json:"participants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.MessagesPartial || len(view.Messages) != assistantRecentMessages {
		t.Fatalf("partial=%v len=%d", view.MessagesPartial, len(view.Messages))
	}
	body := rec.Body.String()
	if strings.Contains(body, "OLDEST-TURN") || !strings.Contains(body, "LATEST-TURN") {
		t.Fatalf("recent view did not keep only the tail")
	}
	if view.Messages[0].Content != "turn-5" || view.Messages[len(view.Messages)-1].Content != "LATEST-TURN" {
		t.Fatalf("tail bounds = %q .. %q", view.Messages[0].Content, view.Messages[len(view.Messages)-1].Content)
	}
	if len(view.Participants) != 1 || view.Participants[0].ID != owner.ID {
		t.Fatalf("cached participants = %+v", view.Participants)
	}

	c, rec = workbenchContext(owner, "GET", "/api/assistant/sessions/long-chat", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: "long-chat"}}
	h.session(c)
	if rec.Code != 200 {
		t.Fatalf("full status %d: %s", rec.Code, rec.Body.String())
	}
	full := rec.Body.String()
	if strings.Contains(full, `"messagesPartial":true`) || !strings.Contains(full, "OLDEST-TURN") {
		t.Fatalf("full session view changed shape")
	}

	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{
		ID: "short-chat", Title: "短会话",
		Messages: []domain.AssistantMessage{{Role: "user", Content: "only-turn"}},
	}); err != nil {
		t.Fatal(err)
	}
	c, rec = workbenchContext(owner, "GET", "/api/assistant/sessions/short-chat?view=recent", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: "short-chat"}}
	h.session(c)
	if rec.Code != 200 {
		t.Fatalf("short status %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"messagesPartial":true`) || !strings.Contains(rec.Body.String(), "only-turn") {
		t.Fatalf("short transcript should be complete: %s", rec.Body.String())
	}
}

// The chat paints view=recent before the full read returns. Neither that tail
// nor a metadata-only update may replace the stored transcript.
func TestRecentSessionViewCannotOverwriteTheTranscript(t *testing.T) {
	h, owner := workbenchFixture(t)
	const id = "long-bind"
	messages := make([]domain.AssistantMessage, 46)
	for i := range messages {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages[i] = domain.AssistantMessage{Role: role, Content: fmt.Sprintf("MSG-%03d", i)}
	}
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: id, Title: "长会话", Messages: messages}); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) (int, string) {
		c, rec := workbenchContext(owner, method, path, bytes.NewBufferString(body))
		c.Params = gin.Params{{Key: "id", Value: id}}
		h.session(c)
		return rec.Code, rec.Body.String()
	}
	stored := func() domain.AssistantSession {
		t.Helper()
		item, ok := h.repo.AssistantSession(owner.ID, id)
		if !ok {
			t.Fatal("session disappeared")
		}
		return item
	}

	code, recent := call("GET", "/api/assistant/sessions/"+id+"?view=recent", "")
	if code != 200 || !strings.Contains(recent, `"messagesPartial":true`) {
		t.Fatalf("recent view: %d %s", code, recent)
	}
	var view map[string]any
	if err := json.Unmarshal([]byte(recent), &view); err != nil {
		t.Fatal(err)
	}
	view["projectId"] = "workbench"
	echoed, _ := json.Marshal(view)
	if code, body := call("PUT", "/api/assistant/sessions/"+id, string(echoed)); code != 409 {
		t.Fatalf("saving the recent tail back: %d %s", code, body)
	}
	if item := stored(); len(item.Messages) != 46 || item.ProjectID != "" {
		t.Fatalf("tail write changed the session: %d messages, project %q", len(item.Messages), item.ProjectID)
	}

	code, body := call("PUT", "/api/assistant/sessions/"+id, `{"id":"long-bind","projectId":"workbench"}`)
	if code != 200 {
		t.Fatalf("bind without messages: %d %s", code, body)
	}
	item := stored()
	if item.ProjectID != "workbench" || len(item.Messages) != 46 || item.Messages[0].Content != "MSG-000" || item.Messages[45].Content != "MSG-045" {
		t.Fatalf("bound session: project %q, %d messages", item.ProjectID, len(item.Messages))
	}
	var saved domain.AssistantSession
	if err := json.Unmarshal([]byte(body), &saved); err != nil || len(saved.Messages) != 46 {
		t.Fatalf("bind response should carry the full transcript: %v %d", err, len(saved.Messages))
	}
}
