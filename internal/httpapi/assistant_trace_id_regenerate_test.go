package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"agentflow/internal/domain"
)

// Regenerating a stopped turn sends the stored transcript unchanged; its
// trailing question still carries the stopped generation's trace ID and must
// move to the new generation together with the reply.
func TestRegenerateStoppedTurnRestampsTheTrailingQuestion(t *testing.T) {
	h, u := workbenchFixture(t)
	h.generate = func(context.Context, string, domain.User) (string, error) {
		return `{"message":"好的","drafts":[]}`, nil
	}
	stopped := domain.AssistantSession{ID: "trace-stopped", Messages: []domain.AssistantMessage{{Role: "user", Content: "停掉的问题", TraceID: "stopped-generation"}}}
	if err := h.repo.PutAssistantSession(u.ID, stopped); err != nil {
		t.Fatal(err)
	}
	stored, _ := h.repo.AssistantSession(u.ID, stopped.ID)
	regenerated := startTraceGeneration(t, h, u, stored.ID, stored.Messages)
	if regenerated == "stopped-generation" {
		t.Fatal("regenerate reused the stopped generation id")
	}
	s := waitForGeneration(t, h, u.ID, stored.ID, regenerated)
	assertTurnTrace(t, "stopped then regenerate", s, regenerated)
}

// The re-stamp happens only after the stored transcript matched; a request
// that changed the trailing question is still rejected as stale.
func TestRegenerateWithChangedTrailingQuestionConflicts(t *testing.T) {
	h, u := workbenchFixture(t)
	stopped := domain.AssistantSession{ID: "trace-stale", Messages: []domain.AssistantMessage{{Role: "user", Content: "原问题", TraceID: "stopped-generation"}}}
	if err := h.repo.PutAssistantSession(u.ID, stopped); err != nil {
		t.Fatal(err)
	}
	stored, _ := h.repo.AssistantSession(u.ID, stopped.ID)
	for name, edit := range map[string]func(*domain.AssistantMessage){
		"content":  func(m *domain.AssistantMessage) { m.Content = "偷改的问题" },
		"trace id": func(m *domain.AssistantMessage) { m.TraceID = "forged" },
	} {
		messages := append([]domain.AssistantMessage(nil), stored.Messages...)
		edit(&messages[0])
		body, _ := json.Marshal(map[string]any{"sessionId": stored.ID, "messages": messages})
		c, rec := workbenchContext(u, "POST", "/api/assistant/generations", bytes.NewBuffer(body))
		h.startGeneration(c)
		if rec.Code != 409 {
			t.Fatalf("%s: status = %d %s, want 409", name, rec.Code, rec.Body.String())
		}
	}
	if s, _ := h.repo.AssistantSession(u.ID, stored.ID); len(s.Messages) != 1 || s.Messages[0].Content != "原问题" || s.Messages[0].TraceID != "stopped-generation" {
		t.Fatalf("conflicting regenerate changed the transcript: %+v", s.Messages)
	}
	if g, ok := h.repo.AssistantGeneration(u.ID, stored.ID); ok && g.ID != "" {
		t.Fatalf("conflicting regenerate started a generation: %+v", g)
	}
}
