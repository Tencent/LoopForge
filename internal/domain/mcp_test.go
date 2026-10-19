package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateMCPServer(t *testing.T) {
	if err := ValidateMCPServer(MCPServerDefinition{ID: "gongfeng", Name: "Gongfeng", Transport: "http", URL: "https://example.com/mcp"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMCPServer(MCPServerDefinition{ID: "local", Name: "Local", Command: "npx"}); err != nil {
		t.Fatal(err)
	}
	err := ValidateMCPServer(MCPServerDefinition{ID: "bad", Name: "Bad", Transport: "http", URL: "ftp://nope"})
	if err == nil || !strings.Contains(err.Error(), "url") {
		t.Fatalf("http without http(s) url should fail, got %v", err)
	}
	if err := ValidateMCPServer(MCPServerDefinition{ID: "taihu", Name: "Taihu", Transport: "http", URL: "https://prod.mcp.it.example.com/app/mcp", AuthType: "taihu-pat"}); err != nil {
		t.Fatalf("taihu HTTP MCP should pass: %v", err)
	}
	if err := ValidateMCPServer(MCPServerDefinition{ID: "bad-auth", Name: "Bad", Command: "npx", AuthType: "taihu-pat"}); err == nil {
		t.Fatal("stdio MCP must not accept taihu-pat auth")
	}
	if err := ValidateMCPServer(MCPServerDefinition{ID: "static-auth", Name: "Static", Transport: "http", URL: "https://example.com", AuthType: "taihu-pat", Headers: map[string]string{"authorization": "Bearer secret"}}); err == nil {
		t.Fatal("taihu-pat MCP must not store an Authorization header")
	}
}

func TestValidateMCPServerScope(t *testing.T) {
	base := MCPServerDefinition{ID: "iwiki", Name: "iWiki", Transport: "http", URL: "https://example.com/mcp", AuthType: "taihu-pat"}
	project := base
	project.Scope, project.ProjectID = "project", "p1"
	if err := ValidateMCPServer(project); err != nil {
		t.Fatalf("valid project MCP rejected: %v", err)
	}
	personal := base
	personal.Scope, personal.OwnerUserID = "personal", 2
	if err := ValidateMCPServer(personal); err != nil {
		t.Fatalf("valid personal MCP rejected: %v", err)
	}
	personal.OwnerUserID = 0
	if err := ValidateMCPServer(personal); err == nil {
		t.Fatal("personal MCP without owner should be rejected")
	}
	personal.OwnerUserID, personal.MarketID = 2, "iwiki"
	if err := ValidateMCPServer(personal); err == nil {
		t.Fatal("personal market MCP should be rejected")
	}
}

func TestParseMCPImportShapes(t *testing.T) {
	one, err := ParseMCPImport(json.RawMessage(`{"id":"a","name":"A","command":"npx"}`))
	if err != nil || len(one) != 1 || one[0].ID != "a" {
		t.Fatalf("object: %v %#v", err, one)
	}
	many, err := ParseMCPImport(json.RawMessage(`[{"id":"a","name":"A","command":"npx"},{"id":"b","name":"B","transport":"http","url":"https://x"}]`))
	if err != nil || len(many) != 2 {
		t.Fatalf("array: %v %#v", err, many)
	}
	wrapped, err := ParseMCPImport(json.RawMessage(`{"mcpServers":{"gongfeng":{"id":"gongfeng","name":"Gongfeng","transport":"http","url":"https://mcp.example/gongfeng"}}}`))
	if err != nil || len(wrapped) != 1 || wrapped[0].ID != "gongfeng" {
		t.Fatalf("map envelope: %v %#v", err, wrapped)
	}
	if _, err := ParseMCPImport(json.RawMessage(`{"foo":1}`)); err == nil {
		t.Fatal("garbage JSON should fail")
	}
}

func TestPublicMCPServerMasksCredentialEnvironmentOnly(t *testing.T) {
	got := PublicMCPServer(MCPServerDefinition{Environment: map[string]string{"KNOT_API_TOKEN": "x", "DB_PASSWORD": "y", "PATH": "/usr/bin", "LOG_LEVEL": "debug", "GH_TOKEN": "${secret:GH}"}})
	if got.Environment["KNOT_API_TOKEN"] != MaskedSecretValue || got.Environment["DB_PASSWORD"] != MaskedSecretValue {
		t.Fatalf("credentials not masked: %v", got.Environment)
	}
	if got.Environment["PATH"] != "/usr/bin" || got.Environment["LOG_LEVEL"] != "debug" || got.Environment["GH_TOKEN"] != "${secret:GH}" {
		t.Fatalf("non-secret values changed: %v", got.Environment)
	}
}
