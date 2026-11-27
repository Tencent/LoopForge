package repo

import (
	"context"
	"encoding/json"
	"testing"

	"agentflow/internal/domain"
)

func TestKnowledgeConnectorSecretCAS(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	conn := domain.KnowledgeConnector{
		ID:          "cas-conn",
		OwnerUserID: 77,
		Name:        "CAS",
		Type:        domain.ConnectorTypeMCP,
		Endpoint:    "http://mcp.example.com/mcp",
		SecretEnc:   "v1:old",
		Enabled:     true,
	}
	if err := s.PutKnowledgeConnector(conn); err != nil {
		t.Fatal(err)
	}

	updated, err := s.UpdateKnowledgeConnectorSecret(context.Background(), 77, "cas-conn", "v1:stale", "v1:new")
	if err != nil {
		t.Fatalf("CAS mismatch error: %v", err)
	}
	if updated {
		t.Fatal("CAS with stale expected ciphertext must not update")
	}
	if got, _ := s.GetKnowledgeConnector(77, "cas-conn"); got.SecretEnc != "v1:old" {
		t.Fatalf("row changed on failed CAS: %q", got.SecretEnc)
	}

	updated, err = s.UpdateKnowledgeConnectorSecret(context.Background(), 77, "cas-conn", "v1:old", "v1:new")
	if err != nil || !updated {
		t.Fatalf("CAS success = %v, %v", updated, err)
	}
	got, ok := s.GetKnowledgeConnector(77, "cas-conn")
	if !ok || got.SecretEnc != "v1:new" || !got.SecretConfigured {
		t.Fatalf("CAS did not persist: %+v", got)
	}
	if got.Name != "CAS" || got.Endpoint != conn.Endpoint {
		t.Fatalf("CAS touched non-secret fields: %+v", got)
	}

	// Wrong owner never updates.
	updated, err = s.UpdateKnowledgeConnectorSecret(context.Background(), 88, "cas-conn", "v1:new", "v1:hijack")
	if err != nil || updated {
		t.Fatalf("cross-user CAS = %v, %v", updated, err)
	}
	if latest, _ := s.GetKnowledgeConnector(77, "cas-conn"); latest.SecretEnc != "v1:new" {
		t.Fatalf("cross-user CAS wrote: %q", latest.SecretEnc)
	}
}

func TestKnowledgeConnectorConfigCAS(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	conn := domain.KnowledgeConnector{
		ID:          "cas-config",
		OwnerUserID: 77,
		Name:        "Config CAS",
		Type:        domain.ConnectorTypeMCP,
		Endpoint:    "http://mcp.example.com/mcp",
		Config:      domain.KnowledgeConnectorConfig{TimeoutSec: 20, Extra: map[string]string{"oauthMode": "custom"}},
		SecretEnc:   "v1:old",
		Enabled:     true,
	}
	if err := s.PutKnowledgeConnector(conn); err != nil {
		t.Fatal(err)
	}
	current, ok := s.GetKnowledgeConnector(77, "cas-config")
	if !ok {
		t.Fatal("connector missing")
	}
	expected, err := json.Marshal(current.Config)
	if err != nil {
		t.Fatal(err)
	}

	updated, err := s.UpdateKnowledgeConnectorConfig(context.Background(), 77, "cas-config", `{"timeoutSec":1}`, `{"timeoutSec":30}`)
	if err != nil {
		t.Fatalf("config CAS mismatch error: %v", err)
	}
	if updated {
		t.Fatal("CAS with stale config must not update")
	}
	if got, _ := s.GetKnowledgeConnector(77, "cas-config"); got.Config.TimeoutSec != 20 {
		t.Fatalf("row changed on failed CAS: %+v", got.Config)
	}

	next, err := json.Marshal(domain.KnowledgeConnectorConfig{TimeoutSec: 45, Extra: map[string]string{"oauthMode": "custom", "oauthTokenUrl": "http://mcp.example.com/token"}})
	if err != nil {
		t.Fatal(err)
	}
	updated, err = s.UpdateKnowledgeConnectorConfig(context.Background(), 77, "cas-config", string(expected), string(next))
	if err != nil || !updated {
		t.Fatalf("config CAS success = %v, %v", updated, err)
	}
	got, ok := s.GetKnowledgeConnector(77, "cas-config")
	if !ok {
		t.Fatal("connector missing after CAS")
	}
	if got.Config.TimeoutSec != 45 || got.Config.Extra["oauthTokenUrl"] != "http://mcp.example.com/token" {
		t.Fatalf("config CAS did not persist: %+v", got.Config)
	}
	if got.Name != conn.Name || got.Endpoint != conn.Endpoint || got.SecretEnc != "v1:old" {
		t.Fatalf("config CAS touched non-config fields: %+v", got)
	}
}

func TestConnectorConfigEqualNormalization(t *testing.T) {
	cases := []struct {
		name     string
		stored   string
		expected string
		want     bool
	}{
		{"key order", `{"timeoutSec":20,"extra":{"oauthMode":"custom"}}`, `{"extra":{"oauthMode":"custom"},"timeoutSec":20}`, true},
		{"null and empty object", `null`, `{}`, true},
		{"missing body", ``, `{}`, true},
		{"empty collections", `{"extra":{},"defaultSources":[],"headers":{}}`, `{}`, true},
		{"value mismatch", `{"timeoutSec":20}`, `{"timeoutSec":21}`, false},
		{"type mismatch", `{"timeoutSec":20}`, `{"timeoutSec":"20"}`, false},
		{"extra value mismatch", `{"extra":{"oauthMode":"custom"}}`, `{"extra":{"oauthMode":"auto"}}`, false},
	}
	for _, tc := range cases {
		if got := connectorConfigEqual([]byte(tc.stored), tc.expected); got != tc.want {
			t.Errorf("%s: connectorConfigEqual(%q, %q) = %v, want %v", tc.name, tc.stored, tc.expected, got, tc.want)
		}
	}
}
