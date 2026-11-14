package repo

import (
	"agentflow/internal/domain"
	"testing"
)

// BeginAssistantGeneration lets a regenerate re-stamp the unanswered trailing
// question with the new generation's trace ID. That relaxation must stay
// limited to that one field on that one message; every other difference is
// still a stale transcript.
func TestBeginAssistantGenerationIgnoresOnlyTheTrailingQuestionTraceID(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const owner = int64(51)
	stored := domain.AssistantSession{ID: "trace-guard", UserID: owner, Messages: []domain.AssistantMessage{
		{Role: "user", Content: "第一问", TraceID: "g1"},
		{Role: "assistant", Content: "第一答", TraceID: "g1"},
		{Role: "user", Content: "第二问", TraceID: "stopped"},
	}}
	if err = st.PutAssistantSession(owner, stored); err != nil {
		t.Fatal(err)
	}
	stored, _ = st.AssistantSession(owner, stored.ID)
	pending := func(edit func([]domain.AssistantMessage) []domain.AssistantMessage) domain.AssistantSession {
		next := stored
		next.Messages = edit(append([]domain.AssistantMessage(nil), stored.Messages...))
		return next
	}
	begin := func(id string, next domain.AssistantSession) error {
		return st.BeginAssistantGeneration(domain.AssistantGeneration{ID: id, SessionID: stored.ID, UserID: owner, Status: "running"}, next)
	}

	rejected := map[string]func([]domain.AssistantMessage) []domain.AssistantMessage{
		"trailing question content": func(m []domain.AssistantMessage) []domain.AssistantMessage {
			m[2].TraceID, m[2].Content = "g2", "改过的第二问"
			return m
		},
		"earlier question trace id": func(m []domain.AssistantMessage) []domain.AssistantMessage {
			m[0].TraceID = "g2"
			return m
		},
		"earlier reply trace id": func(m []domain.AssistantMessage) []domain.AssistantMessage {
			m[1].TraceID = "g2"
			return m
		},
		"trailing question trace id when a new message is appended": func(m []domain.AssistantMessage) []domain.AssistantMessage {
			m[2].TraceID = "g2"
			return append(m, domain.AssistantMessage{Role: "user", Content: "第三问", TraceID: "g2"})
		},
	}
	for name, edit := range rejected {
		if err := begin("rejected", pending(edit)); err == nil {
			t.Fatalf("%s: stale transcript accepted", name)
		}
	}
	if saved, _ := st.AssistantSession(owner, stored.ID); saved.Messages[2].TraceID != "stopped" || len(saved.Messages) != 3 {
		t.Fatalf("rejected generation changed the transcript: %+v", saved.Messages)
	}

	restamped := pending(func(m []domain.AssistantMessage) []domain.AssistantMessage {
		m[2].TraceID = "g2"
		return m
	})
	if err := begin("g2", restamped); err != nil {
		t.Fatalf("re-stamping the trailing question: %v", err)
	}
	saved, _ := st.AssistantSession(owner, stored.ID)
	if got := []string{saved.Messages[0].TraceID, saved.Messages[1].TraceID, saved.Messages[2].TraceID}; got[0] != "g1" || got[1] != "g1" || got[2] != "g2" {
		t.Fatalf("trace ids after regenerate = %v", got)
	}
}

// A transcript that ends with a reply has no unanswered question, so a
// changed trace ID on that trailing reply is still a conflict.
func TestBeginAssistantGenerationRejectsChangedTrailingReplyTraceID(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const owner = int64(52)
	stored := domain.AssistantSession{ID: "trace-reply", UserID: owner, Messages: []domain.AssistantMessage{
		{Role: "user", Content: "问", TraceID: "g1"},
		{Role: "assistant", Content: "答", TraceID: "g1"},
	}}
	if err = st.PutAssistantSession(owner, stored); err != nil {
		t.Fatal(err)
	}
	stored, _ = st.AssistantSession(owner, stored.ID)
	next := stored
	next.Messages = append([]domain.AssistantMessage(nil), stored.Messages...)
	next.Messages[1].TraceID = "g2"
	if err = st.BeginAssistantGeneration(domain.AssistantGeneration{ID: "g2", SessionID: stored.ID, UserID: owner, Status: "running"}, next); err == nil {
		t.Fatal("changed trailing reply trace id accepted")
	}
}
