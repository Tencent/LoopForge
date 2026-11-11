package repo

import (
	"fmt"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func governanceStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestKnowledgeDocumentMetadataIsParsedAtWriteTime(t *testing.T) {
	s := governanceStore(t)
	body := "---\ntitle: 部署手册\nowner: 张三\nstatus: 已废弃\nupdated: 2024-02-03\n---\n# 部署手册\n\n正文\n"
	if err := s.PutKnowledgeDocument(domain.KnowledgeDocument{
		SourceID: "s1", Path: "guides/deploy.md", Title: "部署手册", Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	doc, ok := s.KnowledgeDocument("s1", "guides/deploy.md")
	if !ok {
		t.Fatal("document missing")
	}
	if doc.Owner != "张三" || doc.Status != "deprecated" {
		t.Fatalf("metadata = owner %q status %q", doc.Owner, doc.Status)
	}
	if doc.FrontmatterUpdated == nil || doc.FrontmatterUpdated.Format("2006-01-02") != "2024-02-03" {
		t.Fatalf("frontmatter updated = %v", doc.FrontmatterUpdated)
	}
	metas := s.KnowledgeDocumentMetas([]string{"s1"})
	if len(metas) != 1 || metas[0].Owner != "张三" || metas[0].Status != "deprecated" || metas[0].Path != "guides/deploy.md" {
		t.Fatalf("metas = %+v", metas)
	}

	// Overwriting with a document that has no frontmatter clears the parsed
	// metadata instead of keeping the previous document's owner.
	if err := s.PutKnowledgeDocument(domain.KnowledgeDocument{
		SourceID: "s1", Path: "guides/deploy.md", Title: "部署手册", Body: "# 部署手册\n\n没有头\n",
	}); err != nil {
		t.Fatal(err)
	}
	doc, _ = s.KnowledgeDocument("s1", "guides/deploy.md")
	if doc.Owner != "" || doc.Status != "" || doc.FrontmatterUpdated != nil {
		t.Fatalf("metadata was not cleared: %+v", doc)
	}
}

func TestKnowledgeDocumentMetadataFallsBackToBodyForLegacyRows(t *testing.T) {
	s := governanceStore(t)
	// Simulate a row written before metadata columns existed.
	body := "---\nowner: legacy\nstatus: verified\nupdated: 2023-05-06\n---\n# Legacy\n"
	if err := s.DB.Exec("INSERT INTO knowledge_documents (source_id, path, title, body, line_count, sha256, `commit`, revision) VALUES (?,?,?,?,?,?,?,?)",
		"s-legacy", "legacy.md", "Legacy", body, 5, "x", "1", 1).Error; err != nil {
		t.Fatal(err)
	}
	doc, ok := s.KnowledgeDocument("s-legacy", "legacy.md")
	if !ok || doc.Owner != "legacy" || doc.Status != "verified" || doc.FrontmatterUpdated == nil {
		t.Fatalf("legacy document metadata = %+v", doc)
	}
	metas := s.KnowledgeDocumentMetas([]string{"s-legacy"})
	if len(metas) != 1 || metas[0].Owner != "legacy" || metas[0].Status != "verified" {
		t.Fatalf("legacy metas = %+v", metas)
	}
}

func TestKnowledgeFeedbackAndGapsPersist(t *testing.T) {
	s := governanceStore(t)
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	item := domain.KnowledgeFeedback{
		ID: "fb-1", ProjectID: "p1", BaseID: "b1", SourceID: "s1", Path: "a.md",
		Title: "A", Kind: domain.KnowledgeFeedbackHelpful, Surface: "knowledge_center",
		Actor: "alice", UserID: 7, CreatedAt: now,
	}
	if err := s.PutKnowledgeFeedback(item); err != nil {
		t.Fatal(err)
	}
	got := s.KnowledgeFeedbacks("p1", 10)
	if len(got) != 1 || got[0].ID != "fb-1" || got[0].Kind != domain.KnowledgeFeedbackHelpful || got[0].Path != "a.md" {
		t.Fatalf("feedbacks = %+v", got)
	}
	if other := s.KnowledgeFeedbacks("p2", 10); len(other) != 0 {
		t.Fatalf("project isolation broken: %+v", other)
	}

	if err := s.PutKnowledgeGap(domain.KnowledgeGap{ProjectID: "p1", Query: "怎么回滚", Normalized: "怎么回滚", FirstSeen: now, LastSeen: now}); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Hour)
	if err := s.PutKnowledgeGap(domain.KnowledgeGap{ProjectID: "p1", Query: "怎么回滚?", Normalized: "怎么回滚", FirstSeen: later, LastSeen: later}); err != nil {
		t.Fatal(err)
	}
	gaps := s.KnowledgeGaps("p1", 10)
	if len(gaps) != 1 || gaps[0].Count != 2 || gaps[0].Query != "怎么回滚?" {
		t.Fatalf("gaps = %+v", gaps)
	}
	if other := s.KnowledgeGaps("p2", 10); len(other) != 0 {
		t.Fatalf("gap project isolation broken: %+v", other)
	}

	repeatAt := now.Add(time.Minute)
	put := func(item domain.KnowledgeGap) {
		t.Helper()
		if err := s.PutKnowledgeGap(item); err != nil {
			t.Fatal(err)
		}
	}
	put(domain.KnowledgeGap{ProjectID: "p1", Query: "怎么部署", Normalized: "怎么部署", UserID: 7, FirstSeen: repeatAt, LastSeen: repeatAt})
	put(domain.KnowledgeGap{ProjectID: "p1", Query: "怎么部署", Normalized: "怎么部署", UserID: 7, FirstSeen: repeatAt.Add(time.Minute), LastSeen: repeatAt.Add(time.Minute)})
	put(domain.KnowledgeGap{ProjectID: "p1", Query: "会话重复", Normalized: "会话重复", SessionID: "sess-1", FirstSeen: repeatAt, LastSeen: repeatAt})
	put(domain.KnowledgeGap{ProjectID: "p1", Query: "会话重复", Normalized: "会话重复", SessionID: "sess-1", FirstSeen: repeatAt.Add(time.Minute), LastSeen: repeatAt.Add(time.Minute)})
	counts := map[string]int{}
	for _, gap := range s.KnowledgeGaps("p1", 10) {
		counts[gap.Normalized] = gap.Count
	}
	if counts["怎么部署"] != 1 || counts["会话重复"] != 1 {
		t.Fatalf("short repeats should stay at 1: %+v", counts)
	}
	laterUser := repeatAt.Add(3 * time.Minute)
	put(domain.KnowledgeGap{ProjectID: "p1", Query: "怎么部署", Normalized: "怎么部署", UserID: 8, FirstSeen: laterUser, LastSeen: laterUser})
	counts = map[string]int{}
	for _, gap := range s.KnowledgeGaps("p1", 10) {
		counts[gap.Normalized] = gap.Count
	}
	if counts["怎么部署"] != 2 {
		t.Fatalf("a different user should count: %+v", counts)
	}
	if err := s.SetKnowledgeGapStatus("p1", "怎么部署", domain.KnowledgeGapIgnored); err != nil {
		t.Fatal(err)
	}
	for _, gap := range s.KnowledgeGaps("p1", 10) {
		if gap.Normalized == "怎么部署" {
			t.Fatalf("ignored gap still listed: %+v", gap)
		}
	}
	if s.CountHiddenKnowledgeGaps("p1") != 1 {
		t.Fatalf("hidden = %d", s.CountHiddenKnowledgeGaps("p1"))
	}
}

func TestKnowledgeSourceMetadataSwitchAndSyncStage(t *testing.T) {
	s := governanceStore(t)
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{ID: "src-meta", BaseID: "b", Kind: "git", Name: "文档", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	got, ok := s.KnowledgeSource("src-meta")
	if !ok || !got.MetadataChecks {
		t.Fatalf("new source should check metadata by default: %+v", got)
	}
	if err := s.SetKnowledgeSourceMetadataChecks("src-meta", false); err != nil {
		t.Fatal(err)
	}
	got, _ = s.KnowledgeSource("src-meta")
	if got.MetadataChecks {
		t.Fatal("metadata checks should be off")
	}
	if err := s.DB.Model(&knowledgeSourceRow{}).Where("id = ?", "src-meta").Updates(map[string]any{"status": "syncing", "lease_owner": "worker-1"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.SetKnowledgeSyncStage("src-meta", "worker-1", "pulling"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.KnowledgeSource("src-meta")
	if got.SyncStage != "pulling" || got.MetadataChecks {
		t.Fatalf("stage must not reset the metadata switch: %+v", got)
	}
	if err := s.SetKnowledgeSyncStage("src-meta", "other", "indexing"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.KnowledgeSource("src-meta")
	if got.SyncStage != "pulling" {
		t.Fatalf("a different lease must not change the stage: %q", got.SyncStage)
	}
}

func TestKnowledgeGovernanceReportReplacesPreviousScan(t *testing.T) {
	s := governanceStore(t)
	first := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)
	issues := []domain.KnowledgeGovernanceIssue{
		{BaseID: "b1", SourceID: "s1", Path: "a.md", Title: "A", Type: "stale", Summary: "过期", DetectedAt: first},
		{BaseID: "b1", SourceID: "s1", Path: "b.md", Title: "B", Type: "missing_owner", Summary: "缺负责人", DetectedAt: first},
	}
	if err := s.ReplaceKnowledgeGovernanceReport("b1", domain.KnowledgeGovernanceScan{BaseID: "b1", ScannedAt: first, DocCount: 2, IssueCount: 2}, issues); err != nil {
		t.Fatal(err)
	}
	if got := s.KnowledgeGovernanceIssues([]string{"b1"}); len(got) != 2 {
		t.Fatalf("issues = %+v", got)
	}
	if scans := s.KnowledgeGovernanceScans([]string{"b1"}); len(scans) != 1 || scans[0].DocCount != 2 {
		t.Fatalf("scans = %+v", scans)
	}

	if err := s.ReplaceKnowledgeGovernanceReport("b1", domain.KnowledgeGovernanceScan{BaseID: "b1", ScannedAt: second, DocCount: 3, IssueCount: 1}, []domain.KnowledgeGovernanceIssue{
		{BaseID: "b1", SourceID: "s1", Path: "c.md", Title: "C", Type: "dead_link", Summary: "死链", DetectedAt: second},
	}); err != nil {
		t.Fatal(err)
	}
	got := s.KnowledgeGovernanceIssues([]string{"b1"})
	if len(got) != 1 || got[0].Path != "c.md" || !got[0].DetectedAt.Equal(second) {
		t.Fatalf("issues after rescan = %+v", got)
	}
	scans := s.KnowledgeGovernanceScans([]string{"b1"})
	if len(scans) != 1 || !scans[0].ScannedAt.Equal(second) || scans[0].IssueCount != 1 {
		t.Fatalf("scan after rescan = %+v", scans)
	}
}

func TestKnowledgeDocumentMetasHandlesManySources(t *testing.T) {
	s := governanceStore(t)
	ids := make([]string, 1200)
	for i := range ids {
		ids[i] = fmt.Sprintf("source-%d", i)
	}
	if got := s.KnowledgeDocumentMetas(ids); len(got) != 0 {
		t.Fatalf("metas = %+v", got)
	}
}
