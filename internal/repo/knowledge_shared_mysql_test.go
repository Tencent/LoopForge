package repo

import (
	"agentflow/internal/domain"
	"os"
	"testing"
)

func TestSharedKnowledgeMigrationRepeatable(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutKnowledgeBase(domain.KnowledgeBase{ID: "migration-shared", Name: "Shared", Layer: "shared"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeBinding(domain.KnowledgeBinding{ProjectID: "migration-project", BaseID: "migration-shared", Permission: "submit"}); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("../../migrations/030_shared_knowledge.sql")
	if err != nil {
		t.Fatal(err)
	}
	for run := 0; run < 2; run++ {
		for _, stmt := range splitSQLStatements(string(script)) {
			if err := s.DB.Exec(stmt).Error; err != nil {
				t.Fatal(err)
			}
		}
		got := s.KnowledgeBindings("migration-project")
		if len(got) != 1 || got[0].Permission != "read" {
			t.Fatalf("shared permissions after migration: %+v", got)
		}
	}
}
