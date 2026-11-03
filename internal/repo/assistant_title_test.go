package repo

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestProjectConversationReplyKeepsTitle(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	const owner = int64(91)
	id := fmt.Sprintf("project-title-%d", time.Now().UnixNano())
	// The browser creates the conversation with the first message as its title.
	if err := st.PutAssistantSession(owner, domain.AssistantSession{ID: id, ProjectID: "project-a", Title: "帮我梳理积分商城需求"}); err != nil {
		t.Fatal(err)
	}
	pending := domain.AssistantSession{ID: id, ProjectID: "project-a", Title: "帮我梳理积分商城需求", Messages: []domain.AssistantMessage{{Role: "user", Content: "帮我梳理积分商城需求"}}}
	g := domain.AssistantGeneration{ID: "writer", SessionID: id, UserID: owner, ActorID: owner, Status: "running"}
	if err := st.BeginAssistantGeneration(g, pending); err != nil {
		t.Fatal(err)
	}
	// Shared project replies are saved without a title.
	reply := domain.AssistantSession{ID: id, ProjectID: "project-a", Messages: append(append([]domain.AssistantMessage(nil), pending.Messages...), domain.AssistantMessage{Role: "assistant", Content: "好的"})}
	if err := st.PutAssistantGenerationReply(owner, g.ID, reply); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.AssistantSession(owner, id); got.Title != "帮我梳理积分商城需求" {
		t.Fatalf("reply blanked the title: %q", got.Title)
	}
	for _, item := range st.ListAssistantSessions(owner) {
		if item.ID == id && item.Title != "帮我梳理积分商城需求" {
			t.Fatalf("history list title = %q", item.Title)
		}
	}
}

func TestAssistantSessionListBackfillsBlankTitles(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	const owner = int64(92)
	prefix := fmt.Sprintf("blank-title-%d", time.Now().UnixNano())
	long := strings.Repeat("长", 40)
	cases := map[string]struct {
		messages []domain.AssistantMessage
		want     string
	}{
		prefix + "-first-user": {[]domain.AssistantMessage{{Role: "assistant", Content: "欢迎"}, {Role: "user", Content: "  最近有哪些需求在推进？\n"}}, "最近有哪些需求在推进？"},
		prefix + "-long":       {[]domain.AssistantMessage{{Role: "user", Content: long}}, strings.Repeat("长", 36) + "…"},
		prefix + "-empty":      {nil, "新对话"},
	}
	updated := map[string]time.Time{}
	for id, tc := range cases {
		if err := st.PutAssistantSession(owner, domain.AssistantSession{ID: id, ProjectID: "project-b", Title: "占位", Messages: tc.messages}); err != nil {
			t.Fatal(err)
		}
		// Simulate rows whose title was blanked by an earlier shared reply.
		item, _ := st.AssistantSession(owner, id)
		item.Title = ""
		if err := st.DB.Model(&assistantSessionRow{}).Where("id = ?", id).Updates(map[string]any{"title": "", "payload": mustJSON(item)}).Error; err != nil {
			t.Fatal(err)
		}
		updated[id] = item.UpdatedAt
	}
	seen := 0
	for _, item := range st.ListAssistantSessions(owner) {
		tc, ok := cases[item.ID]
		if !ok {
			continue
		}
		seen++
		if item.Title != tc.want {
			t.Fatalf("%s listed title = %q, want %q", item.ID, item.Title, tc.want)
		}
		stored, _ := st.AssistantSession(owner, item.ID)
		if stored.Title != tc.want {
			t.Fatalf("%s stored title = %q, want %q", item.ID, stored.Title, tc.want)
		}
		if !stored.UpdatedAt.Equal(updated[item.ID]) {
			t.Fatalf("%s backfill changed updated_at: %s -> %s", item.ID, updated[item.ID], stored.UpdatedAt)
		}
	}
	if seen != len(cases) {
		t.Fatalf("listed %d of %d sessions", seen, len(cases))
	}
}
