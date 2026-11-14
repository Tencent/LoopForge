package httpapi

import (
	"testing"

	"agentflow/internal/domain"
)

func TestStoredAssistantSessionReadsCallerSlotAndTreatsEmptyIDAsMissing(t *testing.T) {
	h, owner := workbenchFixture(t)
	if _, ok := h.storedAssistantSession("", owner.ID); ok {
		t.Fatal("empty session id must not resolve to a conversation")
	}
	if _, ok := h.storedAssistantSession("workbench-absent", owner.ID); ok {
		t.Fatal("unknown session id must not resolve to a conversation")
	}
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: "workbench-own"}); err != nil {
		t.Fatal(err)
	}
	session, ok := h.storedAssistantSession("workbench-own", owner.ID)
	if !ok || session.ID != "workbench-own" {
		t.Fatalf("expected the caller's own conversation, got %+v (found=%v)", session, ok)
	}
	other := owner
	other.ID = owner.ID + 1
	if _, ok := h.storedAssistantSession("workbench-own", other.ID); ok {
		t.Fatal("a private conversation must not be readable by another user")
	}
}

func TestHistoricalAttachmentIDsKeepsOnlyThisProjectsUnownedAttachments(t *testing.T) {
	session := domain.AssistantSession{
		ProjectID: "workbench",
		Messages: []assistantMessage{
			{Attachments: []domain.AssistantAttachment{{ID: "first"}, {ID: "duplicate"}}},
			{Attachments: []domain.AssistantAttachment{{ID: "duplicate"}, {ID: "second"}}},
		},
	}
	got := historicalAttachmentIDs(session, true, "workbench")
	if len(got) != 3 || !got["first"] || !got["second"] || !got["duplicate"] {
		t.Fatalf("expected attachments of the current project, got %+v", got)
	}
	if ids := historicalAttachmentIDs(domain.AssistantSession{}, false, "workbench"); len(ids) != 0 {
		t.Fatalf("missing conversation must not expose attachments, got %+v", ids)
	}
	shared := session
	shared.Shared = true
	if ids := historicalAttachmentIDs(shared, true, "workbench"); len(ids) != 0 {
		t.Fatalf("shared conversations must not inherit private attachments, got %+v", ids)
	}
	if ids := historicalAttachmentIDs(session, true, "other"); len(ids) != 0 {
		t.Fatalf("another project must not reuse attachments, got %+v", ids)
	}
}
