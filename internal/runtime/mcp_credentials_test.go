package runtime

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestBindPersonalMCPHeaders(t *testing.T) {
	original := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{
		{ID: "iwiki", Name: "iWiki", Transport: "http", URL: "https://prod.mcp.it.example.com/app_iwiki_mcp/mcp3", AuthType: MCPAuthTaihuPAT, Headers: map[string]string{"X-Trace": "asset"}},
		{ID: "public", Name: "Public", Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"X-Test": "public"}},
	}}
	bound, err := BindPersonalMCPHeaders(original, "tai_pat_secret")
	if err != nil {
		t.Fatal(err)
	}
	if got := bound.MCPServers[0].Headers["Authorization"]; got != "Bearer tai_pat_secret" {
		t.Fatalf("authorization = %q", got)
	}
	if _, ok := original.MCPServers[0].Headers["Authorization"]; ok {
		t.Fatal("stored asset headers were mutated")
	}
	if _, ok := bound.MCPServers[1].Headers["Authorization"]; ok {
		t.Fatal("credential leaked into non-Taihu MCP")
	}
	bound.MCPServers[0].Headers["X-Trace"] = "changed"
	if original.MCPServers[0].Headers["X-Trace"] != "asset" {
		t.Fatal("header maps were not cloned")
	}
}

func TestBindPersonalMCPHeadersRequiresPAT(t *testing.T) {
	assets := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{{ID: "iwiki", Name: "iWiki", Transport: "http", URL: "https://example.com", AuthType: MCPAuthTaihuPAT}}}
	if !RequiresTaihuPAT(assets) {
		t.Fatal("Taihu requirement was not detected")
	}
	_, err := BindPersonalMCPHeaders(assets, "")
	if err == nil || !strings.Contains(err.Error(), "个人凭证") {
		t.Fatalf("missing PAT error = %v", err)
	}
}
