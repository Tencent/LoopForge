package httpapi

import (
	"fmt"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestApprovalReviewInputKeepsRecentWindow(t *testing.T) {
	var messages []assistantMessage
	for i := range assistantApprovalMaxMessages + 2 {
		messages = append(messages, assistantMessage{Role: "assistant", Content: fmt.Sprintf("m%d", i)})
	}
	input := approvalReviewInput(assistantRequest{ProjectID: "p1", RunID: "r1", Messages: messages}, domain.User{ID: 7})
	if input.ActorID != 7 || input.ProjectID != "p1" || input.RunID != "r1" {
		t.Fatalf("identity not carried: %+v", input)
	}
	if !input.Truncated || len(input.Messages) != assistantApprovalMaxMessages {
		t.Fatalf("truncated=%v len=%d", input.Truncated, len(input.Messages))
	}
	if input.Messages[0].Content != "m2" || input.Messages[len(input.Messages)-1].Content != fmt.Sprintf("m%d", assistantApprovalMaxMessages+1) {
		t.Fatalf("window = %q .. %q", input.Messages[0].Content, input.Messages[len(input.Messages)-1].Content)
	}
}

func TestApprovalReviewInputShortConversationNotTruncated(t *testing.T) {
	input := approvalReviewInput(assistantRequest{Messages: []assistantMessage{{Role: "user", AuthorID: 7, Content: "删除测试规则"}}}, domain.User{ID: 7})
	if input.Truncated || len(input.Messages) != 1 || input.Messages[0].Content != "删除测试规则" {
		t.Fatalf("%+v", input)
	}
	if empty := approvalReviewInput(assistantRequest{}, domain.User{ID: 7}); empty.Truncated || empty.Messages != nil {
		t.Fatalf("%+v", empty)
	}
}

func TestApprovalReviewInputTruncatesLongMessage(t *testing.T) {
	long := strings.Repeat("长", assistantApprovalMaxMessageRunes+1)
	input := approvalReviewInput(assistantRequest{Messages: []assistantMessage{{Role: "user", Content: long}}}, domain.User{ID: 7})
	if !input.Truncated {
		t.Fatal("long message should mark input truncated")
	}
	if got := []rune(input.Messages[0].Content); len(got) != assistantApprovalMaxMessageRunes+1 || string(got[len(got)-1]) != "…" {
		t.Fatalf("content runes = %d", len(got))
	}
}

func TestApprovalReviewInputAttributesRolesAndRedacts(t *testing.T) {
	user := domain.User{ID: 10, CodeBuddyAPIKey: "private-test-key"}
	input := approvalReviewInput(assistantRequest{Messages: []assistantMessage{
		{Role: "user", AuthorID: 20, Content: "删除生产"},
		{Role: "user", AuthorID: 10, Content: "我的 key private-test-key"},
		{Role: "user", Content: "无作者"},
		{Role: "assistant", AuthorID: 20, Content: "好的"},
	}}, user)
	want := []string{"other_user", "user", "user", "assistant"}
	for i, role := range want {
		if input.Messages[i].Role != role {
			t.Fatalf("message %d role = %q, want %q", i, input.Messages[i].Role, role)
		}
	}
	if input.Messages[0].AuthorID != 20 {
		t.Fatalf("author id lost: %+v", input.Messages[0])
	}
	if strings.Contains(input.Messages[1].Content, user.CodeBuddyAPIKey) {
		t.Fatalf("key not redacted: %q", input.Messages[1].Content)
	}
}
