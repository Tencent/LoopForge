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

func TestGeneratedTitleSurvivesPromptRewrite(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	const owner = int64(93)
	id := fmt.Sprintf("generated-title-%d", time.Now().UnixNano())
	prompt := strings.Repeat("需", 20)
	if err := st.PutAssistantSession(owner, domain.AssistantSession{ID: id, Title: prompt}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAssistantSessionGeneratedTitle(owner, id, "积分商城梳理"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutAssistantSession(owner, domain.AssistantSession{ID: id, Title: prompt, Messages: []domain.AssistantMessage{{Role: "user", Content: prompt + "，请按模块列出缺口"}}}); err != nil {
		t.Fatal(err)
	}
	got, ok := st.AssistantSession(owner, id)
	if !ok || got.Title != "积分商城梳理" || !got.TitleGenerated || got.TitleEdited {
		t.Fatalf("summary lost: %+v ok=%v", got, ok)
	}
	edited := got
	edited.Title = "我的标题"
	edited.TitleEdited = true
	if err := st.PutAssistantSession(owner, edited); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAssistantSessionGeneratedTitle(owner, id, "另一个摘要"); err != nil {
		t.Fatal(err)
	}
	got, _ = st.AssistantSession(owner, id)
	if got.Title != "我的标题" || !got.TitleEdited || got.TitleGenerated {
		t.Fatalf("rename lost: %+v", got)
	}
}

func TestGenerationWritesKeepTheLatestManualTitle(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	const owner = int64(93)
	const id = "title-racing-with-turn"
	if err := st.PutAssistantSession(owner, domain.AssistantSession{ID: id, Title: "问题"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAssistantSessionGeneratedTitle(owner, id, "自动生成的名称"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAssistantSessionTitle(owner, id, "", "第一个手动名称"); err != nil {
		t.Fatal(err)
	}
	pending, _ := st.AssistantSession(owner, id)
	if !pending.TitleEdited || pending.TitleGenerated {
		t.Fatalf("title flags after rename: edited=%v generated=%v", pending.TitleEdited, pending.TitleGenerated)
	}
	pending.Messages = []domain.AssistantMessage{{Role: "user", Content: "问题"}}

	if err := st.SetAssistantSessionTitle(owner, id, "", "第二个手动名称"); err != nil {
		t.Fatal(err)
	}
	turn := domain.AssistantGeneration{ID: "turn", SessionID: id, UserID: owner, Status: "running"}
	if err := st.BeginAssistantGeneration(turn, pending); err != nil {
		t.Fatal(err)
	}
	reply, _ := st.AssistantSession(owner, id)
	if reply.Title != "第二个手动名称" {
		t.Fatalf("starting the turn restored an older title: %q", reply.Title)
	}
	reply.Messages = append(reply.Messages, domain.AssistantMessage{Role: "assistant", Content: "回答"})

	if err := st.SetAssistantSessionTitle(owner, id, "", "第三个手动名称"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutAssistantGenerationReply(owner, turn.ID, reply); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAssistantSessionGeneratedTitle(owner, id, "迟到的自动标题"); err != nil {
		t.Fatal(err)
	}
	stored, _ := st.AssistantSession(owner, id)
	if stored.Title != "第三个手动名称" || len(stored.Messages) != 2 {
		t.Fatalf("generation did not preserve the latest title and reply: %+v", stored)
	}
}

func TestRenameAssistantSessionKeepsLegacyScheduledSource(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	const owner = int64(93)
	const id = "legacy-scheduled-title"
	title := scheduledAssistantTitlePrefix + " 定时巡检"
	if err := st.DB.Create(&assistantSessionRow{
		ID: id, UserID: owner, Title: title, Payload: mustJSON(domain.AssistantSession{Title: title}),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.SetAssistantSessionTitle(owner, id, "", "新的巡检名称"); err != nil {
		t.Fatal(err)
	}
	scheduled := st.ListAssistantSessionsBySource(domain.AssistantSourceScheduled)
	if len(scheduled) != 1 || scheduled[0].ID != id || scheduled[0].Title != "新的巡检名称" {
		t.Fatalf("renaming changed the conversation's legacy origin: %+v", scheduled)
	}
}

// A rename keeps updated_at, so a proposal write read before it still passes
// the unchanged check; it must not put the earlier manual title back.
func TestProposalWriteKeepsConcurrentRename(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	const owner = int64(94)
	const id = "proposal-title-race"
	if err := st.PutAssistantSession(owner, domain.AssistantSession{ID: id, Title: "问题", Messages: []domain.AssistantMessage{
		{Role: "user", Content: "问题"}, {Role: "assistant", Content: `{"message":"建议","drafts":[]}`},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAssistantSessionTitle(owner, id, "", "第一个手动名称"); err != nil {
		t.Fatal(err)
	}
	read, _ := st.AssistantSession(owner, id)
	renamed := strings.Repeat("新名称", 20)
	if err := st.SetAssistantSessionTitle(owner, id, "", renamed); err != nil {
		t.Fatal(err)
	}
	next := read
	next.Messages = append([]domain.AssistantMessage(nil), read.Messages...)
	next.Messages[1].Content = `{"message":"建议","drafts":[{"decision":"accepted"}]}`
	if err := st.PutAssistantSessionIfUnchanged(owner, next, read); err != nil {
		t.Fatal(err)
	}
	stored, _ := st.AssistantSession(owner, id)
	if stored.Title != renamed || !stored.TitleEdited || stored.Messages[1].Content != next.Messages[1].Content {
		t.Fatalf("proposal write restored an earlier title: %q edited=%v", stored.Title, stored.TitleEdited)
	}
}
