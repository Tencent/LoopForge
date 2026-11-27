package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

// Message positions name things the client keeps across the switch from the
// tail to the full transcript (draft artifacts are draft-<index>-<n>). The
// tail therefore reports where it starts in the stored transcript.
func TestRecentSessionViewReportsWhereTheTailStarts(t *testing.T) {
	h, owner := workbenchFixture(t)
	read := func(id string) map[string]any {
		t.Helper()
		c, rec := workbenchContext(owner, "GET", "/api/assistant/sessions/"+id+"?view=recent", bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: id}}
		h.session(c)
		if rec.Code != 200 {
			t.Fatalf("%s recent status %d: %s", id, rec.Code, rec.Body.String())
		}
		var view map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	for _, tc := range []struct {
		total  int
		offset float64
	}{{46, 6}, {41, 1}, {40, 0}, {6, 0}} {
		id := fmt.Sprintf("chat-%d", tc.total)
		messages := make([]domain.AssistantMessage, tc.total)
		for i := range messages {
			messages[i] = domain.AssistantMessage{Role: "user", Content: fmt.Sprintf("MSG-%03d", i)}
		}
		if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: id, Title: id, Messages: messages}); err != nil {
			t.Fatal(err)
		}
		view := read(id)
		offset, _ := view["messagesOffset"].(float64)
		if offset != tc.offset {
			t.Fatalf("%d messages: offset %v, want %v", tc.total, view["messagesOffset"], tc.offset)
		}
		tail, _ := view["messages"].([]any)
		first, _ := tail[0].(map[string]any)
		if want := fmt.Sprintf("MSG-%03d", int(tc.offset)); first["content"] != want {
			t.Fatalf("%d messages: tail starts at %v, want %s", tc.total, first["content"], want)
		}
	}
}

func TestCompactTranscriptActivitiesAndCompletedGeneration(t *testing.T) {
	h, owner := workbenchFixture(t)
	const id = "chat-compact"
	rawOutput := strings.Repeat("large tool output ", 16000)
	events := []domain.AssistantActivityEvent{
		{Kind: "text", Text: "I'll inspect it."},
		{Kind: "tool", ID: "tool", Tool: "Read"},
		{Kind: "toolDone", ID: "tool", Output: rawOutput},
		{Kind: "text", Text: "It works."},
	}
	item := domain.AssistantSession{ID: id, Title: "Sample", TitleGenerated: true,
		Messages: []domain.AssistantMessage{{Role: "user", Content: "Question"}, {Role: "assistant", Content: "It works.", Activity: events, GenerationID: "gen-saved"}},
	}
	if err := h.repo.PutAssistantSession(owner.ID, item); err != nil {
		t.Fatal(err)
	}
	g := domain.AssistantGeneration{ID: "gen-saved", UserID: owner.ID, SessionID: id, Status: "running",
		Stream: &domain.AssistantStreamSnapshot{Sequence: 1, Activity: events},
		Events: []domain.AssistantGenerationEvent{{Sequence: 1, Data: json.RawMessage(`{"kind":"status","text":"working"}`)}},
	}
	if err := h.repo.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	g.Status = "completed"
	if err := h.repo.CheckpointAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	read := func(query string, handler gin.HandlerFunc) (int, []byte) {
		c, rec := workbenchContext(owner, "GET", "/api/assistant/sessions/"+id+query, bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: id}}
		handler(c)
		return rec.Code, rec.Body.Bytes()
	}
	var compact domain.AssistantSession
	for _, view := range []string{"recent", "transcript"} {
		code, body := read("?view="+view, h.session)
		if code != 200 || json.Unmarshal(body, &compact) != nil || len(compact.Messages) != 2 || compact.Messages[1].ActivityRef == "" || len(body) >= 4096 {
			t.Fatalf("%s status=%d bytes=%d", view, code, len(body))
		}
	}
	reply := compact.Messages[1]
	ref := reply.ActivityRef
	code, detail := read("?view=activity&index=1&ref="+ref, h.session)
	want, _ := json.Marshal(events)
	if code != 200 || !bytes.Equal(detail, want) {
		t.Fatal("requested trace differs from the saved trace")
	}
	code, _ = read("?view=activity&index=0&ref="+ref, h.session)
	if code != 409 {
		t.Fatalf("wrong activity index status=%d", code)
	}
	code, detail = read("/generation?after=0&savedReply=gen-saved", h.generation)
	var status struct {
		domain.AssistantGeneration
		SessionUpdatedAt time.Time `json:"sessionUpdatedAt"`
	}
	if code != 200 || json.Unmarshal(detail, &status) != nil || status.ID != g.ID || status.Status != "completed" || !status.SessionUpdatedAt.Equal(compact.UpdatedAt) || status.Stream != nil || len(status.Events) != 0 || len(detail) >= 2048 {
		t.Fatalf("saved generation status=%d bytes=%d revision=%v", code, len(detail), status.SessionUpdatedAt)
	}
	code, detail = read("/generation?after=0&savedReply=old-generation", h.generation)
	if code != 200 || !bytes.Contains(detail, []byte("large tool output")) {
		t.Fatal("a different generation must still return the recovery snapshot")
	}
	// A metadata save or follow-up must use the complete canonical history.
	body, _ := json.Marshal(compact)
	c, rec := workbenchContext(owner, "PUT", "/api/assistant/sessions/"+id, bytes.NewBuffer(body))
	c.Params = gin.Params{{Key: "id", Value: id}}
	h.session(c)
	stored, _ := h.repo.AssistantSession(owner.ID, id)
	if rec.Code != 200 || !reflect.DeepEqual(stored.Messages[1].Activity, events) || stored.Messages[1].ActivityRef != "" {
		t.Fatalf("round-trip status=%d", rec.Code)
	}
	compact.Messages[1].ActivityRef = "stale"
	if restoreAssistantActivities(compact.Messages, stored.Messages) {
		t.Fatal("accepted a stale trace")
	}
}

func TestCompactActivityPreservesReplyTextBoundaries(t *testing.T) {
	message := domain.AssistantMessage{Role: "assistant", Activity: []domain.AssistantActivityEvent{
		{Kind: "thinking", Text: strings.Repeat("hidden", 2000)},
		{Kind: "tool", ID: "read", Tool: "Read"},
		{Kind: "text", Text: "First"},
		{Kind: "status"},
		{Kind: "toolDone", ID: "read", Failed: true},
		{Kind: "text", Text: " paragraph."},
		{Kind: "toolDone", ID: "missing-start", Failed: true},
		{Kind: "text", Text: "Final answer."},
	}}
	previewAssistantActivity(&message)
	want := []domain.AssistantActivityEvent{
		{Kind: "text", Text: "First"}, {Kind: "text", Text: " paragraph."},
		{Kind: "thinkingStart"}, {Kind: "text", Text: "Final answer."},
	}
	if message.ActivityRef == "" || !reflect.DeepEqual(message.Activity, want) {
		t.Fatalf("compact trace changed visible text boundaries: %+v", message.Activity)
	}
}

func TestRecentShortSessionRecoversGeneratedImage(t *testing.T) {
	h, owner := workbenchFixture(t)
	now := time.Now().UTC()
	id := "legacy-image-chat"
	dir := filepath.Join(assistantCLIWorkspaceForUser(owner.ID), "generated-images")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "legacy.png"), cvAPNG(t), 0600); err != nil {
		t.Fatal(err)
	}
	session := domain.AssistantSession{ID: id, Messages: []domain.AssistantMessage{
		{Role: "user", Content: "draw an image", CreatedAt: now},
		{Role: "assistant", Content: `{"message":"Saved /tmp/generated-images/legacy.png"}`, CreatedAt: now},
	}}
	if err := h.repo.PutAssistantSession(owner.ID, session); err != nil {
		t.Fatal(err)
	}
	earlier, _ := h.repo.AssistantSession(owner.ID, id)
	c, rec := workbenchContext(owner, "GET", "/api/assistant/sessions/"+id+"?view=recent", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: id}}
	h.session(c)
	var opened domain.AssistantSession
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &opened) != nil ||
		len(opened.Messages) != 2 || len(opened.Messages[1].Attachments) != 1 {
		t.Fatalf("short recent view did not restore the attachment: status %d", rec.Code)
	}
	stored, _ := h.repo.AssistantSession(owner.ID, id)
	if !reflect.DeepEqual(stored.Messages[1].Attachments, opened.Messages[1].Attachments) {
		t.Fatal("recent view returned an attachment without persisting it")
	}
	if saved, err := h.repo.SetAssistantSessionMedia(owner.ID, id, earlier.UpdatedAt, earlier.Messages); err != nil || saved {
		t.Fatalf("stale scan replaced a newer transcript: saved=%v err=%v", saved, err)
	}
}
