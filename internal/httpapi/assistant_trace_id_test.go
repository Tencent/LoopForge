package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

func TestChatTurnSharesItsGenerationIDAsTraceID(t *testing.T) {
	h, u := workbenchFixture(t)
	h.generate = func(context.Context, string, domain.User) (string, error) {
		return `{"message":"好的","drafts":[]}`, nil
	}
	// A browser-supplied trace ID must not be trusted.
	c, rec := workbenchContext(u, "POST", "/api/assistant/generations", bytes.NewBufferString(`{"sessionId":"trace-turn","messages":[{"role":"user","content":"第一问","traceId":"forged"}]}`))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	var started struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil || started.ID == "" {
		t.Fatalf("start body = %s", rec.Body.String())
	}
	s := waitForCompletedGeneration(t, h, u.ID, "trace-turn")
	if len(s.Messages) != 2 {
		t.Fatalf("messages = %+v", s.Messages)
	}
	if s.Messages[0].TraceID != started.ID || s.Messages[1].TraceID != started.ID {
		t.Fatalf("trace ids = %q/%q, want generation %q", s.Messages[0].TraceID, s.Messages[1].TraceID, started.ID)
	}

	// The next turn resends the stored transcript unchanged and gets a new ID.
	next := append(append([]domain.AssistantMessage{}, s.Messages...), domain.AssistantMessage{Role: "user", Content: "第二问"})
	body, _ := json.Marshal(map[string]any{"sessionId": "trace-turn", "messages": next})
	c, rec = workbenchContext(u, "POST", "/api/assistant/generations", bytes.NewBuffer(body))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("second turn: %d %s", rec.Code, rec.Body.String())
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &started)
	s = waitForCompletedGeneration(t, h, u.ID, "trace-turn")
	for len(s.Messages) < 4 {
		s = waitForCompletedGeneration(t, h, u.ID, "trace-turn")
	}
	if s.Messages[0].TraceID == started.ID {
		t.Fatal("first question re-stamped with the second generation")
	}
	if s.Messages[2].TraceID != started.ID || s.Messages[3].TraceID != started.ID {
		t.Fatalf("second turn trace ids = %q/%q, want %q", s.Messages[2].TraceID, s.Messages[3].TraceID, started.ID)
	}
}

func TestSteerGenerationIDReusesTheRedirectingQuestion(t *testing.T) {
	prev := domain.AssistantGeneration{ID: "af-prev"}
	if got := steerGenerationID(prev, []domain.AssistantMessage{{Role: "user", TraceID: "af-next"}}); got != "af-next" {
		t.Fatalf("got %q, want the stamped question id", got)
	}
	for name, messages := range map[string][]domain.AssistantMessage{
		"empty":     nil,
		"unstamped": {{Role: "user"}},
		"same":      {{Role: "user", TraceID: "af-prev"}},
		"assistant": {{Role: "assistant", TraceID: "af-next"}},
	} {
		if got := steerGenerationID(prev, messages); got == "" || got == "af-prev" || got == "af-next" {
			t.Fatalf("%s: got %q, want a fresh id", name, got)
		}
	}
}

func TestInterruptedTurnKeepsItsTraceID(t *testing.T) {
	g := domain.AssistantGeneration{ID: "af-stopped", Stream: &domain.AssistantStreamSnapshot{Answer: "写到一半"}}
	message, ok := turnTranscriptMessage(g, "", true)
	if !ok || message.TraceID != "af-stopped" {
		t.Fatalf("message = %+v, kept = %v", message, ok)
	}
}

func TestHealthReportsShortBuildCommit(t *testing.T) {
	t.Setenv("LOOPFORGE_COMMIT", "60FC455aabbccddeeff001122334455667788990")
	s := &Server{}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/health", nil)
	s.health(c)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["commit"] != "60fc455" {
		t.Fatalf("health = %s", rec.Body.String())
	}
	t.Setenv("LOOPFORGE_COMMIT", "not-a-sha")
	t.Setenv("LOOPFORGE_COMMIT_FILE", t.TempDir()+"/missing")
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/health", nil)
	s.health(c)
	body = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if _, ok := body["commit"]; ok {
		t.Fatalf("unsafe commit leaked: %s", rec.Body.String())
	}
}
