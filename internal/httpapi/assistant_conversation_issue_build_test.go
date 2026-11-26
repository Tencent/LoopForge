package httpapi

import (
	"slices"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestConversationRequirementIssueDefaults(t *testing.T) {
	s := &AgentToolSession{ProjectID: "p", UserID: 7, User: domain.User{LoginName: "alice", ChineseName: " 爱丽丝 "}, ChatSessionID: "chat-1"}
	run := domain.Run{ID: "ws-1", Repositories: []domain.RepositoryContext{{RepositoryID: "repo-web"}, {Name: "no-id"}, {RepositoryID: "repo-api"}}}
	issue := conversationRequirementIssue(s, run, domain.AssistantSession{Title: "修登录页"}, conversationRequirement{Title: "修复登录页文案", Description: "按钮文案错了"})

	if !strings.HasPrefix(issue.ID, "issue-") || issue.ProjectID != "p" || issue.Title != "修复登录页文案" || issue.Description != "按钮文案错了" {
		t.Fatalf("identity fields = %+v", issue)
	}
	if issue.Status != "in_progress" || issue.Priority != "medium" || issue.Kind != "requirement" || issue.Source != "local" || issue.AutomationState != "ignored" {
		t.Fatalf("default fields = %+v", issue)
	}
	if issue.AssigneeUserID != 7 || issue.ConversationSessionID != "chat-1" {
		t.Fatalf("owner fields = %+v", issue)
	}
	if !slices.Equal(issue.RepositoryIDs, []string{"repo-web", "repo-api"}) || issue.RepositoryID != "repo-web" {
		t.Fatalf("repositories = %v / %q", issue.RepositoryIDs, issue.RepositoryID)
	}
	if len(issue.Operations) != 1 {
		t.Fatalf("operations = %+v", issue.Operations)
	}
	op := issue.Operations[0]
	if op.Actor != "爱丽丝" || op.ActorUserID != 7 || op.Action != "create" || op.At.IsZero() {
		t.Fatalf("operation = %+v", op)
	}
	if op.Detail != "对话「修登录页」中识别为需求并创建，在对话工作区 ws-1 直接改代码" {
		t.Fatalf("detail = %q", op.Detail)
	}
}

func TestConversationRequirementIssueFallbacks(t *testing.T) {
	s := &AgentToolSession{ProjectID: "p", UserID: 7, User: domain.User{LoginName: "alice", ChineseName: "  "}}
	long := strings.Repeat("长", 250)
	issue := conversationRequirementIssue(s, domain.Run{ID: "ws-2"}, domain.AssistantSession{Title: "  "}, conversationRequirement{Title: long})

	if issue.Title != truncateRunes(long, 200) {
		t.Fatalf("title must be truncated to 200 runes, got %d", len([]rune(issue.Title)))
	}
	if issue.RepositoryIDs != nil || issue.RepositoryID != "" {
		t.Fatalf("a run without repositories must not set any: %v / %q", issue.RepositoryIDs, issue.RepositoryID)
	}
	op := issue.Operations[0]
	if op.Actor != "alice" {
		t.Fatalf("actor must fall back to the login name, got %q", op.Actor)
	}
	if op.Detail != "对话中识别为需求并创建，在对话工作区 ws-2 直接改代码" {
		t.Fatalf("detail = %q", op.Detail)
	}

	titled := conversationRequirementIssue(s, domain.Run{ID: "ws-3"}, domain.AssistantSession{Title: strings.Repeat("题", 50)}, conversationRequirement{Title: "t"})
	want := "对话「" + truncateRunes(strings.Repeat("题", 50), 40) + "」中识别为需求并创建，在对话工作区 ws-3 直接改代码"
	if got := titled.Operations[0].Detail; got != want {
		t.Fatalf("detail = %q, want %q", got, want)
	}
}
