package repo

import (
	"strings"
	"testing"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

// A message or rename that lands while the install runs (between finding the
// stale failure and rewriting it) must survive the rewrite.
func TestReplaceInstalledCLIFailureKeepsConcurrentEdits(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const (
		owner   = int64(11)
		missing = "Codex CLI 未安装"
		generic = "生成失败"
		next    = "Codex 已安装，请重新发送。"
	)
	session := domain.AssistantSession{ID: "chat", Title: "original", Executor: "codex", Messages: []domain.AssistantMessage{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Unfinished: missing},
	}}
	if err := s.PutAssistantSession(owner, session); err != nil {
		t.Fatal(err)
	}
	g := domain.AssistantGeneration{ID: "gen", SessionID: "chat", UserID: owner, Status: "running"}
	if err := s.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	g.Status, g.Error = "failed", generic
	if err := s.CheckpointAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}

	// Fire the user's edit right after the failing conversations are looked
	// up: that is the moment a stale snapshot used to be held.
	// fired is only touched on this goroutine; the edit's own queries re-enter
	// the callback and must not trigger it again.
	fired := false
	concurrent := func(tx *gorm.DB) {
		// The lookup of conversations that still show the failure.
		if fired || tx.Statement.Table != "assistant_sessions" || !strings.Contains(strings.ToLower(tx.Statement.SQL.String()), "instr(") {
			return
		}
		fired = true
		edited, ok := s.AssistantSession(owner, "chat")
		if !ok {
			t.Error("session missing")
			return
		}
		edited.Title = "renamed by user"
		edited.TitleEdited = true
		edited.Messages = append(edited.Messages, domain.AssistantMessage{Role: "user", Content: "still there?"})
		if err := s.PutAssistantSession(owner, edited); err != nil {
			t.Error(err)
		}
	}
	if err := s.DB.Callback().Query().After("gorm:query").Register("test:concurrent_edit", concurrent); err != nil {
		t.Fatal(err)
	}
	defer s.DB.Callback().Query().Remove("test:concurrent_edit")

	if err := s.ReplaceInstalledCLIFailure("codex", missing, generic, next); err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("the concurrent edit never ran")
	}
	s.DB.Callback().Query().Remove("test:concurrent_edit")

	got, ok := s.AssistantSession(owner, "chat")
	if !ok {
		t.Fatal("session missing")
	}
	if got.Title != "renamed by user" || len(got.Messages) != 3 || got.Messages[2].Content != "still there?" {
		t.Fatalf("concurrent edit lost: title=%q messages=%d", got.Title, len(got.Messages))
	}
	if got.Messages[1].Unfinished != next {
		t.Fatalf("failure not rewritten: %q", got.Messages[1].Unfinished)
	}
	var row assistantGenerationRow
	if err := s.DB.Where("session_id = ?", "chat").Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if gen := decodeGeneration(row); gen.Error != next {
		t.Fatalf("generation failure not rewritten: %q", gen.Error)
	}
}
