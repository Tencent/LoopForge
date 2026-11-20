package httpapi

import (
	"agentflow/internal/domain"
	"bytes"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"testing"
)

func TestSharedConversationForkPreservesOriginalAndVisibility(t *testing.T) {
	h, a, b, out, s := sharedWorkbenchFixture(t)
	s.Model, s.Effort, s.Executor = "gpt-5", "high", assistantEngineCodex
	s.Messages = append(s.Messages, domain.AssistantMessage{Role: "assistant", Model: s.Model, Content: `{"message":"草稿","drafts":[{"tool":"issues_save","body":{"title":"旧"}}]}`}, domain.AssistantMessage{Role: "user", Content: "原消息"})
	if err := h.repo.PutAssistantSession(a.ID, s); err != nil {
		t.Fatal(err)
	}
	s, _ = h.repo.AssistantSession(a.ID, s.ID)
	body, _ := json.Marshal(gin.H{"messageIndex": 2, "content": "修改后\n继续", "updatedAt": s.UpdatedAt})
	c, w := workbenchContext(b, "POST", "/", bytes.NewBuffer(body))
	c.Params = gin.Params{{Key: "id", Value: s.ID}}
	h.forkSession(c)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var branch domain.AssistantSession
	if err := json.Unmarshal(w.Body.Bytes(), &branch); err != nil {
		t.Fatal(err)
	}
	if !branch.Shared || branch.ParentSessionID != s.ID || branch.UserID != b.ID || len(branch.Messages) != 3 || branch.Messages[2].AuthorID != b.ID || branch.Messages[2].Content != "修改后\n继续" || branch.Effort != "high" || branch.Executor != assistantEngineCodex {
		t.Fatal("invalid branch", branch)
	}
	var draft map[string]any
	json.Unmarshal([]byte(branch.Messages[1].Content), &draft)
	if draft["drafts"].([]any)[0].(map[string]any)["inherited"] != true {
		t.Fatal("inherited draft not marked")
	}
	original, _ := h.repo.AssistantSession(a.ID, s.ID)
	if original.Messages[2].Content != "原消息" || original.Messages[1].Content != s.Messages[1].Content {
		t.Fatal("original changed")
	}
	for _, tc := range []struct {
		user   domain.User
		status int
	}{{a, 200}, {b, 200}, {out, 403}} {
		c, w := workbenchContext(tc.user, "GET", "/", bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: branch.ID}}
		h.session(c)
		if w.Code != tc.status {
			t.Fatal(tc.status, w.Code, w.Body.String())
		}
	}
	for _, tc := range []struct {
		index   int
		updated any
		status  int
	}{{-1, s.UpdatedAt, 400}, {1, s.UpdatedAt, 400}, {2, "2020-01-01T00:00:00Z", 409}} {
		body, _ := json.Marshal(gin.H{"messageIndex": tc.index, "content": "changed", "updatedAt": tc.updated})
		c, w := workbenchContext(b, "POST", "/", bytes.NewBuffer(body))
		c.Params = gin.Params{{Key: "id", Value: s.ID}}
		h.forkSession(c)
		if w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}

	// Reopening a branch stored by the old fork endpoint still presents the
	// executor from the shared parent, without requiring a data migration.
	legacy := branch
	legacy.Executor = ""
	if err := h.repo.PutAssistantSession(b.ID, legacy); err != nil {
		t.Fatal(err)
	}
	c, w = workbenchContext(b, "GET", "/", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: branch.ID}}
	h.session(c)
	var reopened domain.AssistantSession
	if err := json.Unmarshal(w.Body.Bytes(), &reopened); err != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	if reopened.Executor != assistantEngineCodex || reopened.Effort != "high" ||
		assistantConversationEngine(reopened.Messages, reopened.Executor) != assistantEngineCodex {
		t.Fatal("legacy branch lost its executor or effort", reopened)
	}
	if persisted, _ := h.repo.AssistantSession(b.ID, legacy.ID); persisted.Executor != "" {
		t.Fatal("GET changed the stored executor", persisted.Executor)
	}
}

func TestEditSharedMessageReplacesSameConversationAndRejectsStale(t *testing.T) {
	h, a, b, out, s := sharedWorkbenchFixture(t)
	s.Messages = append(s.Messages, domain.AssistantMessage{Role: "assistant", Content: "旧回复"}, domain.AssistantMessage{Role: "user", Content: "原问题"}, domain.AssistantMessage{Role: "assistant", Content: "随后回复"})
	if err := h.repo.PutAssistantSession(a.ID, s); err != nil {
		t.Fatal(err)
	}
	s, _ = h.repo.AssistantSession(a.ID, s.ID)
	body, _ := json.Marshal(gin.H{"messageIndex": 2, "content": "更新问题", "updatedAt": s.UpdatedAt})
	for _, u := range []domain.User{out, b} {
		c, w := workbenchContext(u, "POST", "/", bytes.NewBuffer(body))
		c.Params = gin.Params{{Key: "id", Value: s.ID}}
		h.editSession(c)
		want := 200
		if u.ID == out.ID {
			want = 403
		}
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	saved, _ := h.repo.AssistantSession(a.ID, s.ID)
	if saved.ID != s.ID || saved.UserID != a.ID || len(saved.Messages) != 3 || saved.Messages[2].Content != "更新问题" || saved.Messages[2].AuthorID != b.ID || saved.ParentSessionID != "" {
		t.Fatal("edit branched or lost attribution", saved)
	}
	c, w := workbenchContext(b, "POST", "/", bytes.NewBuffer(body))
	c.Params = gin.Params{{Key: "id", Value: s.ID}}
	h.editSession(c)
	if w.Code != 409 {
		t.Fatal("stale overwrite accepted", w.Code)
	}
}
