package httpapi

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestPersonalNativeSessionRebuildsEditedHistoryOnceAndRetainsFailedAttempt(t *testing.T) {
	h, actor := workbenchFixture(t)
	recorder, calls := nativeCLIRecorder(t)
	const id = "personal-native-history"
	if err := h.repo.PutAssistantSession(actor.ID, domain.AssistantSession{ID: id}); err != nil {
		t.Fatal(err)
	}
	send := func(message string, success bool) domain.AssistantSession {
		t.Helper()
		session, _ := h.repo.AssistantSession(actor.ID, id)
		messages := append([]assistantMessage(nil), session.Messages...)
		if message != "" {
			messages = append(messages, assistantMessage{Role: "user", Content: message})
		}
		body, _ := json.Marshal(assistantRequest{SessionID: id, Messages: messages})
		ctx, response := workbenchContext(actor, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
		h.chat(ctx)
		if (success && response.Code != 200) || (!success && response.Code < 400) {
			t.Fatalf("unexpected chat status %d: %s", response.Code, response.Body.String())
		}
		session, _ = h.repo.AssistantSession(actor.ID, id)
		return session
	}
	first := send("discarded personal question", true)
	oldID := calls()[0].flag("--session-id")
	if oldID == "" {
		t.Fatal("first native session was not created")
	}
	edited := first.Messages[0]
	edited.Content = "replacement personal question"
	if err := h.repo.ReplaceAssistantTurn(first, 0, edited); err != nil {
		t.Fatal(err)
	}
	send("", true)
	got := calls()
	newID := got[1].flag("--session-id")
	if newID == "" || newID == oldID || got[1].flag("--resume") != "" {
		t.Fatal("edited personal history resumed the discarded native journal")
	}
	if !strings.Contains(got[1].prompt(), edited.Content) || strings.Contains(got[1].prompt(), first.Messages[0].Content) {
		t.Fatal("rebuilt personal prompt retained discarded history")
	}
	send("continue edited personal session", true)
	if got = calls(); got[2].flag("--resume") != newID || strings.Contains(got[2].prompt(), edited.Content) {
		t.Fatal("edited personal history was rebuilt on every turn")
	}
	if err := os.WriteFile(filepath.Join(recorder, "fail"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	send("interrupted personal attempt", false)
	send("recover the failed personal attempt", true)
	got = calls()
	if len(got) != 5 || got[3].flag("--resume") != newID || got[4].flag("--resume") != newID {
		t.Fatal("failed personal attempt lost the current revision's native identity")
	}
}
