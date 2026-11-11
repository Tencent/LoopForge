package repo

import (
	"os"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestKnowledgeCitationIsProjectScopedAndDropsExpiredRows(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	recent := time.Now().UTC()
	if err := s.PutKnowledgeCitation(domain.KnowledgeCitation{
		At: recent, ProjectID: "p1", AgentID: "dev", UserID: 7, RunID: "run-1", SessionID: "chat-1",
		Tool: "kb_grep", SourceID: "docs", Path: "a.md", Commit: "1", StartLine: 4, EndLine: 4, BaseID: "local", Layer: "project",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeCitation(domain.KnowledgeCitation{
		At: recent.Add(-domain.KnowledgeCitationWindow - time.Hour), ProjectID: "p1", Tool: "kb_search", SourceID: "docs", Path: "old.md", Commit: "1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeCitation(domain.KnowledgeCitation{
		At: recent, ProjectID: "p2", Tool: "kb_fetch", SourceID: "other", Path: "b.md", Commit: "9", StartLine: 1, EndLine: 3,
	}); err != nil {
		t.Fatal(err)
	}

	got := s.KnowledgeCitations("p1", 10)
	if len(got) != 1 || got[0].Path != "a.md" || got[0].AgentID != "dev" || got[0].UserID != 7 || got[0].RunID != "run-1" ||
		got[0].SessionID != "chat-1" || got[0].Tool != "kb_grep" || got[0].StartLine != 4 || got[0].Layer != "project" {
		t.Fatalf("p1 citations = %+v", got)
	}
	if len(s.KnowledgeCitations("p2", 10)) != 1 || len(s.KnowledgeCitations("", 10)) != 0 {
		t.Fatal("citation reads leaked across projects")
	}
	counts := s.KnowledgeCitationCounts("p1", recent.Add(-time.Hour))
	if len(counts) != 1 || counts[0].Path != "a.md" || counts[0].Count != 1 {
		t.Fatalf("counts = %+v", counts)
	}
	if err := s.PutKnowledgeCitation(domain.KnowledgeCitation{}); err == nil {
		t.Fatal("citation without a project must fail")
	}
}

func TestKnowledgeCitationCapKeepsNewest(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC()
	for i, path := range []string{"a.md", "b.md", "c.md"} {
		if err := s.PutKnowledgeCitation(domain.KnowledgeCitation{
			At: now.Add(time.Duration(i) * time.Second), ProjectID: "p1", Tool: "kb_search", SourceID: "docs", Path: path, Commit: "1",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutKnowledgeCitation(domain.KnowledgeCitation{At: now, ProjectID: "p2", Tool: "kb_search", SourceID: "docs", Path: "keep.md", Commit: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.trimKnowledgeCitations("p1", 2, time.Time{}); err != nil {
		t.Fatal(err)
	}
	got := s.KnowledgeCitations("p1", 10)
	if len(got) != 2 || got[0].Path != "c.md" || got[1].Path != "b.md" {
		t.Fatalf("capped citations = %+v", got)
	}
	if other := s.KnowledgeCitations("p2", 10); len(other) != 1 || other[0].Path != "keep.md" {
		t.Fatalf("other project was trimmed: %+v", other)
	}
}

func TestCitationMigrationIsRepeatable(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/034_knowledge_citation.sql")
	if err != nil {
		t.Fatal(err)
	}
	var sql strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		sql.WriteString(line)
		sql.WriteByte('\n')
	}
	text := sql.String()
	if !strings.Contains(text, "CREATE TABLE IF NOT EXISTS knowledge_citations") {
		t.Fatal("citation migration must create the table idempotently")
	}
	if strings.Contains(text, "ADD COLUMN") || strings.Contains(text, "CREATE INDEX") || strings.Contains(text, "ALTER TABLE") {
		t.Fatal("citation migration must not add columns or indexes outside CREATE TABLE")
	}
}
