package httpapi

import (
	"bytes"
	"context"
	"testing"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// A steer stops the running turn and starts a new generation. The stopped
// reply keeps the first generation's ID, while the redirecting question and
// the reply it starts share the new generation's ID.
func TestSteerTurnSharesTheNextGenerationTraceID(t *testing.T) {
	h, user := workbenchFixture(t)
	started := make(chan struct{})
	var calls int
	h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		calls++
		if calls == 1 {
			sink := ctx.Value(assistantGenerationSinkKey{}).(func(string, any))
			sink("thought", assistantStreamEvent{Kind: "answer", Text: "写到一半"})
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "改好了", nil
	}
	c, rec := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBufferString(`{"projectId":"workbench","sessionId":"steer-trace","messages":[{"role":"user","content":"先查项目"}]}`))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("generation never wrote")
	}
	first, _ := h.repo.AssistantGeneration(user.ID, "steer-trace")
	c, rec = workbenchContext(user, "POST", "/api/assistant/sessions/steer-trace/generation/steer?generationId="+first.ID, bytes.NewBufferString(`{"content":"改查仓库","approvalMode":"confirm"}`))
	c.Params = gin.Params{{Key: "id", Value: "steer-trace"}}
	h.steerGeneration(c)
	if rec.Code != 200 {
		t.Fatalf("steer: %d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		saved, _ := h.repo.AssistantSession(user.ID, "steer-trace")
		current, _ := h.repo.AssistantGeneration(user.ID, "steer-trace")
		if current.Status == "completed" && len(saved.Messages) == 4 {
			if current.ID == first.ID {
				t.Fatal("steer reused the stopped generation ID")
			}
			m := saved.Messages
			if m[0].TraceID != first.ID || m[1].TraceID != first.ID {
				t.Fatalf("stopped turn trace ids = %q/%q, want %q", m[0].TraceID, m[1].TraceID, first.ID)
			}
			if m[2].Trigger != "steer" || m[2].TraceID != current.ID || m[3].TraceID != current.ID {
				t.Fatalf("steered turn trace ids = %q/%q (trigger %q), want %q", m[2].TraceID, m[3].TraceID, m[2].Trigger, current.ID)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("steer did not finish: status=%s messages=%d", current.Status, len(saved.Messages))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A scheduled run stamps its prompt with the generation ID, and the reply it
// produces carries the same ID.
func TestScheduledTaskTurnSharesTraceID(t *testing.T) {
	st := cvAStore(t)
	_ = st.PutProject(domain.Project{ID: "trace-project", Name: "Trace"})
	_ = st.PutUser(domain.User{LoginName: "trace-owner", CodexAPIKey: "codex-secret"})
	user, _ := st.UserByLoginName("trace-owner")
	h := &assistantHandlers{deps: &deps{repo: st}}
	task := assistantTask{ID: "trace-job", Name: "巡检", Prompt: "检查", ProjectID: "trace-project", Model: "codex:gpt-6-sol", IntervalSec: 60, Enabled: true}
	h.generate = func(context.Context, string, domain.User) (string, error) { return "完成", nil }
	if err := h.executeAssistantTask(context.Background(), user, &task); err != nil {
		t.Fatalf("task = %v %+v", err, task)
	}
	session, _ := st.AssistantSession(user.ID, task.SessionID)
	if len(session.Messages) != 2 {
		t.Fatalf("messages = %+v", session.Messages)
	}
	question, reply := session.Messages[0], session.Messages[1]
	if question.Role != "user" || reply.Role != "assistant" || question.TraceID == "" || question.TraceID != reply.TraceID {
		t.Fatalf("trace ids = %q/%q", question.TraceID, reply.TraceID)
	}
}
