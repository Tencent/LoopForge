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
	s.Messages = append(s.Messages, domain.AssistantMessage{Role: "assistant", Model: s.Model, GenerationID: "forked-reply", Content: `{"message":"草稿","drafts":[{"tool":"issues_save","body":{"title":"旧"}}]}`}, domain.AssistantMessage{Role: "user", Content: "原消息"})
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

	// An old branch that inherited a legacy reply keeps its executor even
	// after the parent continues with a different model on the same CLI.
	legacy := branch
	legacy.Executor = ""
	if err := h.repo.PutAssistantSession(b.ID, legacy); err != nil {
		t.Fatal(err)
	}
	original.Model = "codex:gpt-5.4"
	if err := h.repo.PutAssistantSession(a.ID, original); err != nil {
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

func TestSharedConversationLegacyForkIgnoresLaterParentCLISwitch(t *testing.T) {
	h, a, b, _, main := sharedWorkbenchFixture(t)
	main.Model, main.Executor = "claude-opus-5.5", assistantEngineCodeBuddy
	main.Messages = append(main.Messages, domain.AssistantMessage{
		Role: "assistant", Model: main.Model, GenerationID: "old-codebuddy-turn", Content: "旧回复",
	}, domain.AssistantMessage{Role: "user", Content: "准备分支的问题"})
	if err := h.repo.PutAssistantSession(a.ID, main); err != nil {
		t.Fatal(err)
	}
	main, _ = h.repo.AssistantSession(a.ID, main.ID)
	branch := domain.AssistantSession{
		ID: "sop-branch-legacy-codebuddy", UserID: b.ID, ProjectID: main.ProjectID,
		RunID: main.RunID, Shared: true, ParentSessionID: main.ID, RootSessionID: main.ID,
		ForkMessageIndex: 2, Model: main.Model, Messages: append([]domain.AssistantMessage(nil), main.Messages...),
	}
	if err := h.repo.CreateAssistantSessionBranch(main, branch); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.ReplaceAssistantTurn(main, 0, domain.AssistantMessage{Role: "user", Content: "重新开始主讨论"}); err != nil {
		t.Fatal(err)
	}
	main, _ = h.repo.AssistantSession(a.ID, main.ID)
	main.Model, main.Executor = "codex:gpt-5", assistantEngineCodex
	main.Messages = append(main.Messages, domain.AssistantMessage{
		Role: "assistant", Model: main.Model, GenerationID: "new-codex-turn", Content: "新回复",
	})
	if err := h.repo.PutAssistantSession(a.ID, main); err != nil {
		t.Fatal(err)
	}
	c, w := workbenchContext(b, "GET", "/", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: branch.ID}}
	h.session(c)
	var reopened domain.AssistantSession
	if err := json.Unmarshal(w.Body.Bytes(), &reopened); err != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	if reopened.Model != branch.Model || assistantConversationEngine(reopened.Messages, reopened.Executor) != assistantEngineCodeBuddy {
		t.Fatal("legacy branch inherited the parent's later CLI", reopened)
	}
}

func TestAssistantLegacyForkEditPreservesResolvedExecutor(t *testing.T) {
	h, user, _, _, _ := sharedWorkbenchFixture(t)
	parent := domain.AssistantSession{
		ID: "legacy-codex-parent", UserID: user.ID, Model: "gpt-5", Executor: assistantEngineCodex,
		Messages: []domain.AssistantMessage{
			{Role: "user", Content: "第一条问题"},
			{Role: "assistant", Content: "第一条回复", Model: "gpt-5", GenerationID: "legacy-codex-reply"},
			{Role: "user", Content: "后续问题"},
		},
	}
	if err := h.repo.PutAssistantSession(user.ID, parent); err != nil {
		t.Fatal(err)
	}
	parent, _ = h.repo.AssistantSession(user.ID, parent.ID)
	branch := domain.AssistantSession{
		ID: "legacy-codex-fork", UserID: user.ID, Model: parent.Model, Effort: "high",
		Messages: append([]domain.AssistantMessage(nil), parent.Messages...), ForkMessageIndex: 2,
		ParentSessionID: parent.ID, RootSessionID: parent.ID,
	}
	if err := h.repo.CreateAssistantSessionBranch(parent, branch); err != nil {
		t.Fatal(err)
	}
	parent.Model = "codex:gpt-5.4"
	if err := h.repo.PutAssistantSession(user.ID, parent); err != nil {
		t.Fatal(err)
	}
	branch, _ = h.repo.AssistantSession(user.ID, branch.ID)
	c, w := workbenchContext(user, "GET", "/", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: branch.ID}}
	h.session(c)
	var reopened domain.AssistantSession
	if err := json.Unmarshal(w.Body.Bytes(), &reopened); w.Code != 200 || err != nil || reopened.Executor != assistantEngineCodex {
		t.Fatal("GET could not resolve the old executor", w.Code, w.Body.String(), err)
	}

	body, _ := json.Marshal(gin.H{"messageIndex": 0, "content": "编辑第一条问题", "updatedAt": branch.UpdatedAt})
	c, w = workbenchContext(user, "POST", "/", bytes.NewBuffer(body))
	c.Params = gin.Params{{Key: "id", Value: branch.ID}}
	h.editSession(c)
	var edited domain.AssistantSession
	if err := json.Unmarshal(w.Body.Bytes(), &edited); w.Code != 200 || err != nil ||
		edited.Executor != assistantEngineCodex || edited.Model != branch.Model || edited.Effort != branch.Effort {
		t.Fatal("edit lost the resolved composer options", w.Code, w.Body.String(), err)
	}
	saved, _ := h.repo.AssistantSession(user.ID, branch.ID)
	if len(saved.Messages) != 1 || saved.Executor != assistantEngineCodex {
		t.Fatal("edit must retain the executor after dropping the inherited reply", saved)
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

func TestEditSharedBranchRejectsDifferentCLIWithoutReplacingMain(t *testing.T) {
	h, a, b, _, main := sharedWorkbenchFixture(t)
	main.Executor, main.Model = assistantEngineCodex, "codex:gpt-5"
	main.Messages = append(main.Messages, domain.AssistantMessage{Role: "assistant", Model: main.Model, Content: "主讨论回复"})
	if err := h.repo.PutAssistantSession(a.ID, main); err != nil {
		t.Fatal(err)
	}
	main, _ = h.repo.AssistantSession(a.ID, main.ID)
	branch := domain.AssistantSession{
		ID: "sop-branch-different-cli", UserID: b.ID, ProjectID: main.ProjectID,
		RunID: main.RunID, Shared: true, ParentSessionID: main.ID, RootSessionID: main.ID,
		Executor: assistantEngineClaude, Model: "claude-code:sonnet",
		Messages: []domain.AssistantMessage{
			main.Messages[0],
			{Role: "assistant", Model: "claude-code:sonnet", Content: "分支回复"},
			{Role: "user", Content: "分支里的后续消息"},
		},
	}
	if err := h.repo.CreateAssistantSessionBranch(main, branch); err != nil {
		t.Fatal(err)
	}
	branch, _ = h.repo.AssistantSession(b.ID, branch.ID)
	body, _ := json.Marshal(gin.H{"messageIndex": 2, "content": "编辑后", "updatedAt": branch.UpdatedAt})
	c, w := workbenchContext(b, "POST", "/", bytes.NewBuffer(body))
	c.Params = gin.Params{{Key: "id", Value: branch.ID}}
	h.editSession(c)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	reopened, _ := h.repo.AssistantSession(a.ID, main.ID)
	if !reopened.UpdatedAt.Equal(main.UpdatedAt) {
		t.Fatal("incompatible edit changed the main conversation", reopened)
	}
}
