package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestKnowledgeConnectorPublicRedaction(t *testing.T) {
	conn := KnowledgeConnector{
		ID:               "conn-wk-1",
		OwnerUserID:      42,
		Name:             "My WeKnora",
		Type:             ConnectorTypeWeKnora,
		Endpoint:         "https://user:pass@weknora.example.com/api/v1?api_key=leak#frag",
		SecretEnc:        "v1:super-secret-ciphertext",
		SecretConfigured: true,
		Enabled:          true,
	}
	pub := conn.Public()
	if pub.SecretEnc != "" {
		t.Fatalf("expected SecretEnc cleared, got %q", pub.SecretEnc)
	}
	if strings.Contains(pub.Endpoint, "user") || strings.Contains(pub.Endpoint, "pass") || strings.Contains(pub.Endpoint, "leak") || strings.Contains(pub.Endpoint, "frag") {
		t.Fatalf("expected endpoint credentials/query/fragment stripped, got %q", pub.Endpoint)
	}
	if pub.Endpoint != "https://weknora.example.com/api/v1" {
		t.Fatalf("unexpected public weknora endpoint %q", pub.Endpoint)
	}

	raw, err := json.Marshal(conn)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "super-secret-ciphertext") || strings.Contains(string(raw), "SecretEnc") {
		t.Fatalf("SecretEnc must never serialize to JSON: %s", raw)
	}

	mcpConn := KnowledgeConnector{
		ID:       "conn-mcp-1",
		Type:     ConnectorTypeMCP,
		Endpoint: "https://alice:secret@mcp.example.com/mcp?tenant=t1",
	}
	if got := mcpConn.Public().Endpoint; got != "https://mcp.example.com/mcp?tenant=t1" {
		t.Fatalf("expected MCP userinfo stripped while preserving query, got %q", got)
	}
}

func TestKnowledgeConnectorSecretHasCredential(t *testing.T) {
	if (KnowledgeConnectorSecret{}).HasCredential() {
		t.Fatal("empty secret must report false")
	}
	if !(KnowledgeConnectorSecret{Token: "tok"}).HasCredential() {
		t.Fatal("token secret must report true")
	}
	if !(KnowledgeConnectorSecret{Headers: map[string]string{"X-Api-Key": "k"}}).HasCredential() {
		t.Fatal("header secret must report true")
	}
	if !(KnowledgeConnectorSecret{AuthType: ConnectorAuthOAuth, RefreshToken: "rt"}).HasCredential() {
		t.Fatal("oauth refresh token must report true")
	}
}
