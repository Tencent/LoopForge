package repo

import (
	"context"
	"os"
	"testing"

	"agentflow/internal/domain"
)

func TestKnowledgeNamespaceMigrationRepeatable(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
	}
	t.Setenv("AGENTFLOW_KNOWLEDGE_MYSQL_DSN", "")
	store, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const base = "kb-namespace-project"
	const oldSource = "kb-hosted-namespace-project"
	const newSource = "kb-hosted-c94092ca4209840a2ae8f231eb904506"
	// Use only this fixture's IDs. A second test invocation can reuse the DB.
	for _, id := range []string{oldSource, newSource} {
		if err := store.DeleteKnowledgeSourceContent(id); err != nil {
			t.Fatal(err)
		}
		if _, found := store.KnowledgeSource(id); found {
			if err := store.DeleteKnowledgeSource(id); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, found := store.KnowledgeSubmission("namespace-review"); found {
		if err := store.DeleteKnowledgeSubmission("namespace-review"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutKnowledgeBase(domain.KnowledgeBase{ID: base, ProjectID: "namespace-project", Layer: "project"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutKnowledgeSource(domain.KnowledgeSource{ID: oldSource, BaseID: base, Kind: "hosted"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutKnowledgeDocument(domain.KnowledgeDocument{SourceID: oldSource, Path: "private.md", Body: "Private project A", Commit: "7", Revision: 7}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutKnowledgeSubmission(domain.KnowledgeSubmission{ID: "namespace-review", ProjectID: "namespace-project", Source: oldSource, Path: "private.md", Content: "Draft", Status: domain.KnowledgeSubmissionPending}); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("../../migrations/037_knowledge_source_namespace.sql")
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for attempt := 0; attempt < 2; attempt++ {
		for _, stmt := range splitSQLStatements(string(script)) {
			if _, err := conn.ExecContext(context.Background(), stmt); err != nil {
				t.Fatalf("037 attempt %d: %v\n%s", attempt, err, stmt)
			}
		}
		doc, ok := store.KnowledgeDocument(newSource, "private.md")
		if !ok || doc.Body != "Private project A" || doc.Revision != 7 {
			t.Fatalf("document lost: %+v found=%t", doc, ok)
		}
		item, ok := store.KnowledgeSubmission("namespace-review")
		if !ok || item.Source != newSource || item.Content != "Draft" {
			t.Fatalf("review lost: %+v", item)
		}
		src, ok := store.KnowledgeSource(newSource)
		if !ok || src.BaseID != base {
			t.Fatalf("source owner lost: %+v", src)
		}
	}
}
