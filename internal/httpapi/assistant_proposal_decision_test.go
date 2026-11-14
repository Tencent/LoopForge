package httpapi

import (
	"encoding/json"
	"errors"
	"testing"

	"agentflow/internal/domain"
)

func TestRecordProposalDecisionRejectsMissingOrAnsweredDrafts(t *testing.T) {
	answered := sopDraft("i1", "start")
	answered.Decision = domain.AssistantProposalDeclined
	plain := assistantDraft{Tool: "issues_save", Body: json.RawMessage(`{"title":"t"}`)}
	messages := []assistantMessage{
		{Role: "user", Content: "要做一个需求"},
		{Role: "assistant", Content: proposalReply(t, sopDraft("i1", "start"), plain, answered)},
		{Role: "assistant", Content: "不是 JSON"},
		{Role: "assistant", Content: `{"message":"x","drafts":["oops"]}`},
	}
	cases := []struct {
		name          string
		message, idx  int
		status        int
		wantErrorText string
	}{
		{"negative message", -1, 0, 400, "建议不存在"},
		{"message out of range", len(messages), 0, 400, "建议不存在"},
		{"user message", 0, 0, 400, "建议不存在"},
		{"content not JSON", 2, 0, 400, "建议不存在"},
		{"negative draft", 1, -1, 400, "建议不存在"},
		{"draft out of range", 1, 3, 400, "建议不存在"},
		{"draft not an object", 3, 0, 400, "这不是派发建议"},
		{"draft without proposal", 1, 1, 400, "这不是派发建议"},
		{"already answered", 1, 2, 409, "这条建议已经处理过"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next, _, err := recordProposalDecision(messages, tc.message, tc.idx, domain.AssistantProposalAccepted)
			var he HTTPError
			if !errors.As(err, &he) || he.Status != tc.status || he.Message != tc.wantErrorText {
				t.Fatalf("err=%v, want %d %q", err, tc.status, tc.wantErrorText)
			}
			if next != nil {
				t.Fatalf("a rejected decision returned messages: %+v", next)
			}
		})
	}
}

func TestRecordProposalDecisionWritesDecisionIntoACopy(t *testing.T) {
	original := proposalReply(t, assistantDraft{Tool: "issues_save", Body: json.RawMessage(`{"title":"t"}`)}, sopDraft("i1", "start"))
	messages := []assistantMessage{{Role: "user", Content: "要做一个需求"}, {Role: "assistant", Content: original}}

	next, draft, err := recordProposalDecision(messages, 1, 1, domain.AssistantProposalMuted)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Proposal == nil || draft.Proposal.Timing != assistantProposalStart || draft.Decision != "" {
		t.Fatalf("draft should be the unanswered proposal: %+v", draft)
	}
	if messages[1].Content != original {
		t.Fatal("the caller's messages were modified; they are needed for the compare-and-swap save")
	}
	if len(next) != len(messages) || next[0].Content != messages[0].Content {
		t.Fatalf("other messages were not copied as is: %+v", next)
	}
	var content struct {
		Message string           `json:"message"`
		Drafts  []assistantDraft `json:"drafts"`
	}
	if err := json.Unmarshal([]byte(next[1].Content), &content); err != nil {
		t.Fatal(err)
	}
	if content.Message != "建议走 SOP" || len(content.Drafts) != 2 {
		t.Fatalf("reply content was not preserved: %+v", content)
	}
	if content.Drafts[1].Decision != domain.AssistantProposalMuted || content.Drafts[0].Decision != "" {
		t.Fatalf("decision written to the wrong draft: %+v", content.Drafts)
	}
}
