package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// waitForGeneration waits until the given generation of a session completes.
func waitForGeneration(t *testing.T, h *assistantHandlers, owner int64, sessionID, generationID string) domain.AssistantSession {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		g, _ := h.repo.AssistantGeneration(owner, sessionID)
		if g.ID == generationID && g.Status == "completed" {
			s, ok := h.repo.AssistantSession(owner, sessionID)
			if !ok {
				t.Fatal("completed transcript missing")
			}
			return s
		}
		if g.ID == generationID && g.Status != "running" {
			t.Fatalf("generation failed: %+v", g)
		}
		if time.Now().After(deadline) {
			t.Fatalf("generation %s did not complete: %+v", generationID, g)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func startTraceGeneration(t *testing.T, h *assistantHandlers, u domain.User, sessionID string, messages []domain.AssistantMessage) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"sessionId": sessionID, "messages": messages})
	c, rec := workbenchContext(u, "POST", "/api/assistant/generations", bytes.NewBuffer(body))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start %s: %d %s", sessionID, rec.Code, rec.Body.String())
	}
	var started struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil || started.ID == "" {
		t.Fatalf("start body = %s", rec.Body.String())
	}
	return started.ID
}

func assertTurnTrace(t *testing.T, label string, s domain.AssistantSession, want string) {
	t.Helper()
	if len(s.Messages) != 2 || s.Messages[0].Role != "user" || s.Messages[1].Role != "assistant" {
		t.Fatalf("%s: messages = %+v", label, s.Messages)
	}
	if s.Messages[0].TraceID != want || s.Messages[1].TraceID != want {
		t.Fatalf("%s: question/reply trace ids = %q/%q, want %q", label, s.Messages[0].TraceID, s.Messages[1].TraceID, want)
	}
}

// Editing or branching a question and then regenerating (what the chat UI
// does right after an edit or fork) must leave the question and the new reply
// on the same trace ID, not the replaced generation's.
func TestEditAndForkRegenerateShareTheNewTraceID(t *testing.T) {
	h, u := workbenchFixture(t)
	h.generate = func(context.Context, string, domain.User) (string, error) {
		return `{"message":"好的","drafts":[]}`, nil
	}
	first := startTraceGeneration(t, h, u, "trace-edit", []domain.AssistantMessage{{Role: "user", Content: "第一问"}})
	s := waitForGeneration(t, h, u.ID, "trace-edit", first)
	assertTurnTrace(t, "first turn", s, first)

	body, _ := json.Marshal(gin.H{"messageIndex": 0, "content": "改过的第一问", "updatedAt": s.UpdatedAt})
	c, w := workbenchContext(u, "POST", "/", bytes.NewBuffer(body))
	c.Params = gin.Params{{Key: "id", Value: s.ID}}
	h.editSession(c)
	if w.Code != 200 {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	edited, _ := h.repo.AssistantSession(u.ID, s.ID)
	if len(edited.Messages) != 1 || edited.Messages[0].TraceID == first {
		t.Fatalf("edited question kept the replaced trace id: %+v", edited.Messages)
	}
	regenerated := startTraceGeneration(t, h, u, s.ID, edited.Messages)
	if regenerated == first {
		t.Fatal("regenerate reused the old generation id")
	}
	s = waitForGeneration(t, h, u.ID, s.ID, regenerated)
	assertTurnTrace(t, "edit then regenerate", s, regenerated)

	body, _ = json.Marshal(gin.H{"messageIndex": 0, "content": "分支里的第一问", "updatedAt": s.UpdatedAt})
	c, w = workbenchContext(u, "POST", "/", bytes.NewBuffer(body))
	c.Params = gin.Params{{Key: "id", Value: s.ID}}
	h.forkSession(c)
	if w.Code != 201 {
		t.Fatalf("fork: %d %s", w.Code, w.Body.String())
	}
	var branch domain.AssistantSession
	if err := json.Unmarshal(w.Body.Bytes(), &branch); err != nil {
		t.Fatal(err)
	}
	if len(branch.Messages) != 1 || branch.Messages[0].TraceID == regenerated {
		t.Fatalf("branched question kept the source trace id: %+v", branch.Messages)
	}
	forked := startTraceGeneration(t, h, u, branch.ID, branch.Messages)
	b := waitForGeneration(t, h, u.ID, branch.ID, forked)
	assertTurnTrace(t, "fork then regenerate", b, forked)

	// The source conversation is untouched by the branch.
	source, _ := h.repo.AssistantSession(u.ID, s.ID)
	assertTurnTrace(t, "source after fork", source, regenerated)
}
