package httpapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestAssistantTitlePromptOnlyForANewLongMessage(t *testing.T) {
	long := strings.Repeat("需", 20)
	messages := []domain.AssistantMessage{{Role: "user", Content: long}}
	if got, ok := assistantTitlePrompt(domain.AssistantSession{}, messages); !ok || got != long {
		t.Fatalf("new session prompt = %q %v", got, ok)
	}
	short := "这是一个新测试对话"
	if got, ok := assistantTitlePrompt(domain.AssistantSession{}, []domain.AssistantMessage{{Role: "user", Content: short}}); !ok || got != short {
		t.Fatalf("short new prompt should be summarized, got %q %v", got, ok)
	}
	if _, ok := assistantTitlePrompt(domain.AssistantSession{}, []domain.AssistantMessage{{Role: "user", Content: "  "}}); ok {
		t.Fatal("blank prompt")
	}
	if _, ok := assistantTitlePrompt(domain.AssistantSession{TitleEdited: true}, messages); ok {
		t.Fatal("renamed session")
	}
	if _, ok := assistantTitlePrompt(domain.AssistantSession{TitleGenerated: true}, messages); ok {
		t.Fatal("already summarized")
	}
	if _, ok := assistantTitlePrompt(domain.AssistantSession{ParentSessionID: "parent"}, messages); ok {
		t.Fatal("fork")
	}
	if _, ok := assistantTitlePrompt(domain.AssistantSession{Messages: messages}, append(messages, domain.AssistantMessage{Role: "user", Content: "继续"})); ok {
		t.Fatal("existing session")
	}
}

func TestDecodeAssistantTitleSummary(t *testing.T) {
	got, err := decodeAssistantTitleSummary("```json\n{\"title\":\"梳理积分商城需求。\"}\n```")
	if err != nil || got != "梳理积分商城需求" {
		t.Fatalf("decoded %q %v", got, err)
	}
	long, err := decodeAssistantTitleSummary(`{"title":"` + strings.Repeat("题", 30) + `"}`)
	if err != nil || long != strings.Repeat("题", 16) {
		t.Fatalf("truncated %q %v", long, err)
	}
	if _, err := decodeAssistantTitleSummary(`{"title":""}`); err == nil {
		t.Fatal("empty title")
	}
}

func TestAssistantPromptTitleMatchesTruncation(t *testing.T) {
	text := strings.Repeat("需", 40)
	session := domain.AssistantSession{Messages: []domain.AssistantMessage{{Role: "user", Content: text}}}
	if !assistantPromptTitleMatches(session, string([]rune(text)[:36])) || !assistantPromptTitleMatches(session, string([]rune(text)[:36])+"…") {
		t.Fatal("truncated prompt title should match")
	}
	if assistantPromptTitleMatches(session, "我的标题") {
		t.Fatal("a real rename must not match")
	}
}

func TestGeneratedTitleReplacesSOPDisplayTitle(t *testing.T) {
	view := runSessionView{assistantConversation: domain.AssistantSession{Title: "梳理积分商城", TitleGenerated: true, Shared: true, RunID: "run-1"}}
	applyRunSessionMeta(&view, repo.SidebarRunMeta{WorkflowID: "wf-1", WorkflowName: "发布检查 SOP", Status: "running"}, repo.SidebarIssue{}, 1, true)
	if view.DisplayTitle != "" || view.SOPName == "" {
		t.Fatalf("summary should be the visible title, SOP stays available: %+v", view)
	}
}

func TestScheduleAssistantTitleWritesSummary(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	h := &assistantHandlers{deps: &deps{repo: st}, titleSummary: func(context.Context, domain.User, string) (string, error) {
		return "积分商城梳理", nil
	}}
	id := "schedule-title"
	prompt := strings.Repeat("需", 20)
	if err := st.PutAssistantSession(1, domain.AssistantSession{ID: id, Title: prompt}); err != nil {
		t.Fatal(err)
	}
	old, _ := st.AssistantSession(1, id)
	h.scheduleAssistantTitle(context.Background(), 1, domain.User{ID: 1}, old, id, []domain.AssistantMessage{{Role: "user", Content: prompt + "，请按模块列出缺口"}}, nil)
	deadline := time.Now().Add(2 * time.Second)
	var got domain.AssistantSession
	for time.Now().Before(deadline) {
		got, _ = st.AssistantSession(1, id)
		if got.TitleGenerated {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if got.Title != "积分商城梳理" || !got.TitleGenerated {
		t.Fatalf("summary not stored: %+v", got)
	}
}
