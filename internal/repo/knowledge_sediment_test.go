package repo

import (
	"testing"

	"agentflow/internal/domain"
)

func TestKnowledgeSedimentConfigAndTombstoneRoundTrip(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	base := domain.KnowledgeBase{
		ID: "kb-sediment", ProjectID: "p1", Name: "项目知识库", Layer: "project",
		SedimentPublishMode: domain.KnowledgeSedimentAuto,
		SedimentRules: []domain.KnowledgeGateRule{
			{Name: "令牌", Pattern: `(?i)token\s*[:=]\s*\S+`, Enabled: true},
			{Name: "关闭的规则", Pattern: "never", Enabled: false},
		},
	}
	if err := s.PutKnowledgeBase(base); err != nil {
		t.Fatal(err)
	}
	got, ok := s.KnowledgeBase("kb-sediment")
	if !ok || got.SedimentPublishMode != domain.KnowledgeSedimentAuto || len(got.SedimentRules) != 2 || got.SedimentRules[0].Pattern == "" || got.SedimentRules[1].Enabled {
		t.Fatalf("base round trip = %+v ok=%v", got, ok)
	}

	tombstone := domain.KnowledgeTombstone{
		ProjectID: "p1", Fingerprint: "abc123", BaseID: "kb-sediment",
		SourceID: "kb-sediment-source", Path: "knowledge/a.md", SubmissionID: "kbs-1",
		Title: "撤回的知识", Reason: "内容错误", CreatedBy: "reviewer",
	}
	if err := s.PutKnowledgeTombstone(tombstone); err != nil {
		t.Fatal(err)
	}
	found, ok := s.KnowledgeTombstone("p1", "abc123")
	if !ok || found.SubmissionID != "kbs-1" || found.SourceID != "kb-sediment-source" || found.Reason != "内容错误" {
		t.Fatalf("tombstone = %+v ok=%v", found, ok)
	}
	if _, ok := s.KnowledgeTombstone("p2", "abc123"); ok {
		t.Fatal("tombstone must be scoped to its project")
	}
}

func TestDeleteKnowledgeDocumentRemovesBodyChunksAndRevisions(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	doc := domain.KnowledgeDocument{SourceID: "src", Path: "knowledge/a.md", Title: "A", Body: "# A\n\n正文", Commit: "1", Revision: 1}
	chunks := []domain.KnowledgeChunk{{SourceID: "src", Path: "knowledge/a.md", Idx: 0, Text: "正文"}}
	if err := s.WriteHostedDocument(doc, domain.KnowledgeRevision{ID: "rev-1", SourceID: "src", Path: "knowledge/a.md", Revision: 1, Body: doc.Body}, chunks); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.KnowledgeDocument("src", "knowledge/a.md"); !ok {
		t.Fatal("document missing before delete")
	}
	if err := s.DeleteKnowledgeDocument("src", "knowledge/a.md"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.KnowledgeDocument("src", "knowledge/a.md"); ok {
		t.Fatal("document body survived retraction")
	}
	if chunks := s.KnowledgeChunks("src", "knowledge/a.md"); len(chunks) != 0 {
		t.Fatalf("chunks survived retraction: %+v", chunks)
	}
	if revs := s.KnowledgeRevisions("src", "knowledge/a.md"); len(revs) != 0 {
		t.Fatalf("revisions survived retraction: %+v", revs)
	}
	// Idempotent: deleting an already absent document is not an error.
	if err := s.DeleteKnowledgeDocument("src", "knowledge/a.md"); err != nil {
		t.Fatalf("repeat delete = %v", err)
	}
}
