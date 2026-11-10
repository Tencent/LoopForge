package repo

import "testing"

func TestKnowledgeSettingsTableIsNotCreated(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.DB.Migrator().HasTable("knowledge_settings") {
		t.Fatal("knowledge_settings is still part of the test schema")
	}
}
