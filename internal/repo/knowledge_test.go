package repo

import (
	"testing"

	"agentflow/internal/domain"
)

func TestKnowledgeRepoLegacyTokenMigrationAndFreshInstallLock(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	// 1. Fresh install: MutateKnowledgeSettingsIfFresh claims once and rejects subsequent calls.
	claimed, err := s.MutateKnowledgeSettingsIfFresh(func(cur *domain.KnowledgeSettings) (bool, error) {
		*cur = domain.KnowledgeSettings{
			Provider: domain.KnowledgeProviderWeKnora,
			Enabled:  true,
			URL:      "http://legacy-weknora:8080",
			Token:    "sk-legacy-secret",
		}
		return true, nil
	})
	if err != nil || !claimed {
		t.Fatalf("expected first MutateKnowledgeSettingsIfFresh to claim, got claimed=%v err=%v", claimed, err)
	}

	claimedAgain, err := s.MutateKnowledgeSettingsIfFresh(func(cur *domain.KnowledgeSettings) (bool, error) {
		t.Fatal("callback must not run once knowledge_settings row exists")
		return true, nil
	})
	if err != nil || claimedAgain {
		t.Fatalf("expected second MutateKnowledgeSettingsIfFresh not to claim, got claimed=%v err=%v", claimedAgain, err)
	}

	// 2. Legacy single-connection -> multi-connection migration inside MutateKnowledgeSettings
	// must see the real legacy Token on current.Token and persist it into kbconn:<id>.
	if err := s.MutateKnowledgeSettings(func(cur *domain.KnowledgeSettings) bool {
		if cur.Token != "sk-legacy-secret" {
			t.Fatalf("expected legacy token to be available in MutateKnowledgeSettings callback, got %q", cur.Token)
		}
		cur.Connections = []domain.KnowledgeConnection{
			{
				ID:       "conn-legacy",
				Name:     "默认连接",
				Provider: domain.KnowledgeProviderWeKnora,
				URL:      cur.URL,
				Token:    cur.Token,
				Enabled:  true,
			},
			{
				ID:       "wk-managed",
				Name:     "内置 WeKnora",
				Provider: domain.KnowledgeProviderWeKnora,
				URL:      "http://weknora-app:8080",
				Token:    "sk-managed-secret",
				Enabled:  true,
			},
		}
		return true
	}); err != nil {
		t.Fatalf("MutateKnowledgeSettings(migrate legacy) error = %v", err)
	}

	got := s.KnowledgeSettings()
	if len(got.Connections) != 2 {
		t.Fatalf("expected 2 connections after migration, got %+v", got.Connections)
	}
	if got.Connections[0].ID != "conn-legacy" || got.Connections[0].Token != "sk-legacy-secret" || !got.Connections[0].TokenConfigured {
		t.Fatalf("legacy connection token lost during migration: %+v", got.Connections[0])
	}
	if got.Connections[1].ID != "wk-managed" || got.Connections[1].Token != "sk-managed-secret" || !got.Connections[1].TokenConfigured {
		t.Fatalf("managed connection token not persisted: %+v", got.Connections[1])
	}

	// 3. Even if an administrator clears all connections (with empty UpdatedBy in local mode),
	// MutateKnowledgeSettingsIfFresh must still refuse to overwrite the existing row.
	if err := s.PutKnowledgeSettings(domain.KnowledgeSettings{
		Provider:    domain.KnowledgeProviderWeKnora,
		Enabled:     false,
		Connections: []domain.KnowledgeConnection{},
		UpdatedBy:   "",
	}); err != nil {
		t.Fatalf("PutKnowledgeSettings(clear) error = %v", err)
	}
	claimedAfterClear, err := s.MutateKnowledgeSettingsIfFresh(func(cur *domain.KnowledgeSettings) (bool, error) {
		t.Fatal("callback must not run after admin cleared connections")
		return true, nil
	})
	if err != nil || claimedAfterClear {
		t.Fatalf("expected MutateKnowledgeSettingsIfFresh to refuse after admin clear, got claimed=%v err=%v", claimedAfterClear, err)
	}
}
