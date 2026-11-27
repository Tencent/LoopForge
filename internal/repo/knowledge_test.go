package repo

import (
	"testing"

	"agentflow/internal/domain"
)

func TestKnowledgeConnectorRepoOwnerIsolation(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	u1 := domain.KnowledgeConnector{
		ID:          "conn-u1",
		OwnerUserID: 101,
		Name:        "Alice WeKnora",
		Type:        domain.ConnectorTypeWeKnora,
		Endpoint:    "http://weknora-a:8080",
		Config:      domain.KnowledgeConnectorConfig{TimeoutSec: 20},
		SecretEnc:   "v1:sealed-alice",
		Enabled:     true,
	}
	u2 := domain.KnowledgeConnector{
		ID:          "conn-u2",
		OwnerUserID: 202,
		Name:        "Bob MCP",
		Type:        domain.ConnectorTypeMCP,
		Endpoint:    "http://mcp-b:9090/mcp",
		SecretEnc:   "v1:sealed-bob",
		Enabled:     true,
	}

	if err := s.PutKnowledgeConnector(u1); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeConnector(u2); err != nil {
		t.Fatal(err)
	}

	list1 := s.ListKnowledgeConnectors(101)
	if len(list1) != 1 || list1[0].ID != "conn-u1" || !list1[0].SecretConfigured || list1[0].SecretEnc != "v1:sealed-alice" || list1[0].Config.TimeoutSec != 20 {
		t.Fatalf("unexpected user 1 connectors: %+v", list1)
	}
	if got := s.ListKnowledgeConnectors(999); len(got) != 0 {
		t.Fatalf("expected 0 connectors for unknown user, got %+v", got)
	}

	if _, ok := s.GetKnowledgeConnector(101, "conn-u2"); ok {
		t.Fatal("user 1 must not read user 2 connector")
	}
	if deleted, err := s.DeleteKnowledgeConnector(101, "conn-u2"); err != nil || deleted {
		t.Fatalf("user 1 must not delete user 2 connector, got deleted=%v err=%v", deleted, err)
	}

	// Cross-user ID collision overwrite attempt must fail.
	hijack := u1
	hijack.OwnerUserID = 202
	if err := s.PutKnowledgeConnector(hijack); err == nil {
		t.Fatal("expected PutKnowledgeConnector to reject overwriting another user's connector ID")
	}

	// Same owner update preserves created_at and updates enabled/config/secret.
	u1.Enabled = false
	u1.SecretEnc = ""
	if err := s.PutKnowledgeConnector(u1); err != nil {
		t.Fatal(err)
	}
	updated, ok := s.GetKnowledgeConnector(101, "conn-u1")
	if !ok || updated.Enabled || updated.SecretConfigured || updated.SecretEnc != "" {
		t.Fatalf("expected updated connector to clear enabled and secret, got %+v %v", updated, ok)
	}

	disabledConn := domain.KnowledgeConnector{
		ID:          "conn-u1-disabled",
		OwnerUserID: 101,
		Name:        "Disabled Initially",
		Type:        domain.ConnectorTypeMCP,
		Endpoint:    "http://disabled.local/mcp",
		Enabled:     false,
	}
	if err := s.PutKnowledgeConnector(disabledConn); err != nil {
		t.Fatal(err)
	}
	gotDisabled, ok := s.GetKnowledgeConnector(101, "conn-u1-disabled")
	if !ok || gotDisabled.Enabled {
		t.Fatalf("expected initial Enabled=false persisted, got %+v %v", gotDisabled, ok)
	}
	_, _ = s.DeleteKnowledgeConnector(101, "conn-u1-disabled")

	deleted, err := s.DeleteKnowledgeConnector(101, "conn-u1")
	if err != nil || !deleted {
		t.Fatalf("expected user 1 delete to succeed, got deleted=%v err=%v", deleted, err)
	}
	if _, ok := s.GetKnowledgeConnector(101, "conn-u1"); ok {
		t.Fatal("expected deleted connector to disappear")
	}
}
