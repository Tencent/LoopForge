package runtime

import (
	"agentflow/internal/domain"
	"testing"
)

func TestGitHubMCPBindsOnlyToOfficialHostWithoutMutatingStoredAssets(t *testing.T) {
	assets := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{{ID: "github", AuthType: "github-token", URL: "https://api.githubcopilot.com/mcp/", Headers: map[string]string{"X-Test": "value"}}}}
	bound, err := BindGitHubMCPHeaders(assets, "user-token", true)
	if err != nil || bound.MCPServers[0].Headers["Authorization"] != "Bearer user-token" {
		t.Fatalf("bound: %+v %v", bound, err)
	}
	if assets.MCPServers[0].Headers["Authorization"] != "" {
		t.Fatal("stored assets were mutated")
	}
	if _, err := BindGitHubMCPHeaders(assets, "", true); err == nil {
		t.Fatal("missing personal token accepted")
	}
	assets.MCPServers[0].URL = "https://attacker.invalid/mcp"
	if _, err := BindGitHubMCPHeaders(assets, "user-token", true); err == nil {
		t.Fatal("token could be bound to an arbitrary server")
	}
}
