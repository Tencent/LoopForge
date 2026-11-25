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
