package repo

import (
	"context"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestKnowledgeUsageRepoCountsAndIsolation(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	t1 := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)

	// Record search hit for user 101.
	if err := s.RecordKnowledgeUsage(ctx, 101, domain.KnowledgeUsageDelta{
		ConnectorID: "conn-a",
		Search:      1,
		CalledAt:    t1,
		Documents: []domain.KnowledgeDocumentUsageDelta{
			{SourceID: "kb1", SourceName: "研发规范", DocumentID: "doc-1", Title: "部署指南", SearchHit: 1},
			{SourceID: "kb1", SourceName: "研发规范", DocumentID: "doc-2", Title: "排障手册", SearchHit: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Record direct read for user 101 on doc-1.
	if err := s.RecordKnowledgeUsage(ctx, 101, domain.KnowledgeUsageDelta{
		ConnectorID: "conn-a",
		Read:        1,
		CalledAt:    t2,
		Documents: []domain.KnowledgeDocumentUsageDelta{
			{SourceID: "kb1", SourceName: "研发规范", DocumentID: "doc-1", Title: "部署指南", Read: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Record error call for user 101 on conn-b.
	if err := s.RecordKnowledgeUsage(ctx, 101, domain.KnowledgeUsageDelta{
		ConnectorID: "conn-b",
		Error:       1,
		CalledAt:    t2,
	}); err != nil {
		t.Fatal(err)
	}

	// Record call for user 202 on conn-a (must remain isolated).
	if err := s.RecordKnowledgeUsage(ctx, 202, domain.KnowledgeUsageDelta{
		ConnectorID: "conn-a",
		Search:      1,
		CalledAt:    t2,
		Documents: []domain.KnowledgeDocumentUsageDelta{
			{SourceID: "kb1", DocumentID: "doc-1", Title: "Other User Doc", SearchHit: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}

	conns101 := s.ListKnowledgeConnectorUsage(101, "")
	if len(conns101) != 2 {
		t.Fatalf("expected 2 connectors for user 101, got %+v", conns101)
	}
	if conns101[0].ConnectorID != "conn-a" || conns101[0].CallCount != 2 || conns101[0].SearchCount != 1 || conns101[0].ReadCount != 1 || conns101[0].DocumentHitCount != 3 || conns101[0].ErrorCount != 0 {
		t.Fatalf("unexpected conn-a usage: %+v", conns101[0])
	}
	if conns101[1].ConnectorID != "conn-b" || conns101[1].CallCount != 1 || conns101[1].ErrorCount != 1 {
		t.Fatalf("unexpected conn-b usage: %+v", conns101[1])
	}

	docs101 := s.ListKnowledgeDocumentUsage(101, "", 10)
	if len(docs101) != 2 {
		t.Fatalf("expected 2 documents for user 101, got %+v", docs101)
	}
	if docs101[0].DocumentID != "doc-1" || docs101[0].CallCount != 2 || docs101[0].SearchHitCount != 1 || docs101[0].ReadCount != 1 {
		t.Fatalf("expected doc-1 ranked first with 2 calls, got %+v", docs101[0])
	}

	conns202 := s.ListKnowledgeConnectorUsage(202, "")
	if len(conns202) != 1 || conns202[0].CallCount != 1 {
		t.Fatalf("user 202 counts leaked or missing: %+v", conns202)
	}

	// Reset only conn-b for user 101.
	if err := s.ResetKnowledgeUsage(101, "conn-b"); err != nil {
		t.Fatal(err)
	}
	if got := s.ListKnowledgeConnectorUsage(101, ""); len(got) != 1 || got[0].ConnectorID != "conn-a" {
		t.Fatalf("expected only conn-a after scoped reset, got %+v", got)
	}

	// Reset all for user 101; user 202 must stay intact.
	if err := s.ResetKnowledgeUsage(101, ""); err != nil {
		t.Fatal(err)
	}
	if got := s.ListKnowledgeConnectorUsage(101, ""); len(got) != 0 {
		t.Fatalf("expected 0 connector usage after full reset, got %+v", got)
	}
	if got := s.ListKnowledgeDocumentUsage(101, "", 10); len(got) != 0 {
		t.Fatalf("expected 0 document usage after full reset, got %+v", got)
	}
	if got := s.ListKnowledgeConnectorUsage(202, ""); len(got) != 1 || got[0].CallCount != 1 {
		t.Fatalf("user 202 usage must not be affected by user 101 reset: %+v", got)
	}
}

func TestKnowledgeUsageEventsCallersCitedAndFilters(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// 1. SOP caller search hit on 2 docs.
	if err := s.RecordKnowledgeUsage(ctx, 11, domain.KnowledgeUsageDelta{
		ReadID:      "read-sop-1",
		ConnectorID: "conn-feishu",
		Tool:        "search_knowledge",
		Query:       "灰度发布",
		Search:      1,
		CalledAt:    now.Add(-2 * time.Minute),
		Caller: domain.KnowledgeUsageCaller{
			Kind:         domain.KnowledgeCallerSOP,
			WorkflowID:   "wf-release",
			WorkflowName: "发布交付 SOP",
			RunID:        "run-100",
			NodeID:       "node-dev",
			NodeName:     "研发实现",
			AgentID:      "agent-dev",
			AgentName:    "后端架构师",
			ProjectID:    "proj-1",
		},
		Documents: []domain.KnowledgeDocumentUsageDelta{
			{SourceID: "space-1", SourceName: "研发空间", DocumentID: "doc-a", Title: "灰度发布规范", SearchHit: 1},
			{SourceID: "space-1", SourceName: "研发空间", DocumentID: "doc-b", Title: "回滚手册", SearchHit: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 2. Chat caller full read on doc-a.
	if err := s.RecordKnowledgeUsage(ctx, 11, domain.KnowledgeUsageDelta{
		ReadID:      "read-chat-1",
		ConnectorID: "conn-feishu",
		Tool:        "read_document",
		Query:       "doc-a",
		Read:        1,
		CalledAt:    now.Add(-time.Minute),
		Caller: domain.KnowledgeUsageCaller{
			Kind:         domain.KnowledgeCallerChat,
			SessionID:    "chat-1",
			SessionTitle: "线上灰度排障",
			ProjectID:    "proj-1",
		},
		Documents: []domain.KnowledgeDocumentUsageDelta{
			{SourceID: "space-1", SourceName: "研发空间", DocumentID: "doc-a", Title: "灰度发布规范", Read: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Mark doc-a cited for both calls; repeat call must be idempotent.
	marks := []domain.KnowledgeCitedDocMark{
		{ReadID: "read-sop-1", ConnectorID: "conn-feishu", SourceID: "space-1", DocumentID: "doc-a"},
		{ReadID: "read-chat-1", ConnectorID: "conn-feishu", SourceID: "space-1", DocumentID: "doc-a"},
	}
	if err := s.MarkKnowledgeUsageCited(ctx, 11, marks); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkKnowledgeUsageCited(ctx, 11, marks); err != nil {
		t.Fatal(err)
	}

	// Document summary must include CitedCount=2 and TopCallers (both SOP & Chat).
	docs := s.ListKnowledgeDocumentUsage(11, "", 10)
	if len(docs) != 2 {
		t.Fatalf("expected 2 docs, got %+v", docs)
	}
	if docs[0].DocumentID != "doc-a" || docs[0].CitedCount != 2 || len(docs[0].TopCallers) != 2 {
		t.Fatalf("expected doc-a with citedCount=2 and 2 topCallers, got %+v", docs[0])
	}

	// Filter by callerKind=sop -> 2 events (doc-a & doc-b from SOP search).
	sopPage := s.ListKnowledgeUsageEvents(11, domain.KnowledgeUsageEventFilter{CallerKind: "sop"})
	if sopPage.Total != 2 || len(sopPage.Items) != 2 {
		t.Fatalf("expected 2 SOP events, got %+v", sopPage)
	}
	// Filter by callerKind=role -> 2 events (agent_id present).
	rolePage := s.ListKnowledgeUsageEvents(11, domain.KnowledgeUsageEventFilter{CallerKind: "role"})
	if rolePage.Total != 2 {
		t.Fatalf("expected 2 role events, got %+v", rolePage)
	}
	// Filter by callerKind=chat -> 1 event (cited=true, action=read).
	chatPage := s.ListKnowledgeUsageEvents(11, domain.KnowledgeUsageEventFilter{CallerKind: "chat"})
	if chatPage.Total != 1 || len(chatPage.Items) != 1 || !chatPage.Items[0].Cited || chatPage.Items[0].Action != domain.KnowledgeUsageActionRead {
		t.Fatalf("unexpected chat events: %+v", chatPage)
	}
	if chatPage.Items[0].Caller.SessionTitle != "线上灰度排障" {
		t.Fatalf("expected session title preserved, got %+v", chatPage.Items[0].Caller)
	}
	// Filter by documentId + query.
	docPage := s.ListKnowledgeUsageEvents(11, domain.KnowledgeUsageEventFilter{DocumentID: "doc-a", Query: "架构师"})
	if docPage.Total != 1 || docPage.Items[0].Caller.WorkflowName != "发布交付 SOP" || !docPage.Items[0].Cited {
		t.Fatalf("unexpected doc+query filter result: %+v", docPage)
	}

	// Reset must also clear events.
	if err := s.ResetKnowledgeUsage(11, ""); err != nil {
		t.Fatal(err)
	}
	afterReset := s.ListKnowledgeUsageEvents(11, domain.KnowledgeUsageEventFilter{})
	if afterReset.Total != 0 || len(afterReset.Items) != 0 {
		t.Fatalf("expected 0 events after reset, got %+v", afterReset)
	}
}
