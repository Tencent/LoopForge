package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func waitForCompletedGeneration(t *testing.T, h *assistantHandlers, owner int64, sessionID string) domain.AssistantSession {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		g, _ := h.repo.AssistantGeneration(owner, sessionID)
		if g.Status == "completed" {
			s, ok := h.repo.AssistantSession(owner, sessionID)
			if !ok {
				t.Fatal("completed transcript missing")
			}
			return s
		}
		if g.Status != "running" && g.Status != "" {
			t.Fatalf("generation failed: %+v", g)
		}
		if time.Now().After(deadline) {
			t.Fatal("completion timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPersonalChatStampsEachQuestionWithTheServerClock(t *testing.T) {
	h, u := workbenchFixture(t)
	h.generate = func(context.Context, string, domain.User) (string, error) {
		return `{"message":"好的","drafts":[]}`, nil
	}
	before := time.Now().UTC()
	// A browser clock (or a forged value) must not date the question.
	c, rec := workbenchContext(u, "POST", "/api/assistant/generations", bytes.NewBufferString(`{"sessionId":"personal-time","messages":[{"role":"user","content":"第一问","createdAt":"2001-01-01T00:00:00Z"}]}`))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	s := waitForCompletedGeneration(t, h, u.ID, "personal-time")
	if len(s.Messages) != 2 {
		t.Fatalf("messages = %+v", s.Messages)
	}
	first := s.Messages[0]
	if first.CreatedAt.Before(before) || first.CreatedAt.After(time.Now().UTC()) {
		t.Fatalf("question time = %v, want server time after %v", first.CreatedAt, before)
	}
	if s.Messages[1].CreatedAt.IsZero() {
		t.Fatal("reply has no time")
	}

	// The next turn resends the stored transcript; the stamped question must
	// still match byte for byte, and the new question gets its own time.
	next := append(append([]domain.AssistantMessage{}, s.Messages...), domain.AssistantMessage{Role: "user", Content: "第二问"})
	body, _ := json.Marshal(map[string]any{"sessionId": "personal-time", "messages": next})
	c, rec = workbenchContext(u, "POST", "/api/assistant/generations", bytes.NewBuffer(body))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("second turn: %d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, _ = h.repo.AssistantSession(u.ID, "personal-time")
		if len(s.Messages) == 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("second reply missing: %+v", s.Messages)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !s.Messages[0].CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("first question re-dated: %v -> %v", first.CreatedAt, s.Messages[0].CreatedAt)
	}
	if s.Messages[2].CreatedAt.Before(first.CreatedAt) {
		t.Fatalf("second question time = %v", s.Messages[2].CreatedAt)
	}
}

func TestBeijingTimestampsKeepTheInstant(t *testing.T) {
	in := `{"startedAt":"2026-11-01T06:16:29.123456789Z","finishedAt":"2026-11-01T06:18:47Z","note":"retry at 2026-10-31T23:30:00Z","local":"2026-11-01T14:00:00+08:00","id":"run-2026-11-01T06"}`
	out := beijingTimestamps(in)
	for _, want := range []string{
		`"startedAt":"2026-11-01T14:16:29.123456789+08:00"`,
		`"finishedAt":"2026-11-01T14:18:47+08:00"`,
		`retry at 2026-11-01T07:30:00+08:00`,
		`"local":"2026-11-01T14:00:00+08:00"`,
		`"id":"run-2026-11-01T06"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	if strings.Contains(out, "Z\"") {
		t.Errorf("UTC timestamp left in %s", out)
	}
	var a, b struct {
		StartedAt time.Time `json:"startedAt"`
	}
	if err := json.Unmarshal([]byte(in), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &b); err != nil {
		t.Fatal(err)
	}
	if !a.StartedAt.Equal(b.StartedAt) {
		t.Fatalf("instant changed: %v -> %v", a.StartedAt, b.StartedAt)
	}
	artifact := `{"content":"generated at 2026-11-01T06:16:29Z"}`
	if got := assistantPlatformResultForModel("runs_artifact_get", artifact); got != artifact {
		t.Fatalf("artifact body rewritten: %s", got)
	}
	if got := assistantPlatformResultForModel("runs_get", artifact); !strings.Contains(got, "2026-11-01T14:16:29+08:00") {
		t.Fatalf("run result not converted: %s", got)
	}
}

func TestAssistantTurnContextStatesBeijingTimeAndProjectSwitching(t *testing.T) {
	old := assistantNow
	assistantNow = func() time.Time { return time.Date(2026, 11, 1, 6, 16, 0, 0, time.UTC) }
	t.Cleanup(func() { assistantNow = old })
	inProject := assistantTurnContext(assistantRequest{ProjectID: "skillhub"})
	for _, want := range []string{"当前北京时间：2026-11-01 14:16（星期日）", "不要写 UTC", "+08:00", "右上角的项目名称", "不要让用户去输入框上方", "generic-backend-delivery", "不要说「暂不绑定」"} {
		if !strings.Contains(inProject, want) {
			t.Errorf("project context missing %q", want)
		}
	}
	personal := assistantTurnContext(assistantRequest{})
	for _, want := range []string{"个人对话", "右上角的项目名称", "直接绑定", "当前北京时间"} {
		if !strings.Contains(personal, want) {
			t.Errorf("personal context missing %q", want)
		}
	}

}

func TestSystemPromptRecommendsTheGenericSOPs(t *testing.T) {
	prompt := assistantSystemPromptText()
	for _, want := range []string{"generic-product-delivery", "generic-frontend-delivery", "generic-backend-delivery", "主动推荐", "右上角的项目名称"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, "请用户在输入框上方选择项目") {
		t.Error("system prompt still points at the hidden composer picker")
	}
}
