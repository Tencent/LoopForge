package runtime

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestPrepareMCPServersRebasesForExecutionMachines(t *testing.T) {
	servers := []domain.MCPServerDefinition{
		{ID: domain.KnowledgeMCPServerID, URL: "http://127.0.0.1:8080/runners/knowledge/mcp"},
		{ID: domain.PlatformMCPServerID, URL: "/mcp"},
		{ID: "third-party", URL: "http://kb.example/mcp"},
	}
	got, warnings, err := PrepareMCPServers(domain.AgentAssetBundle{MCPServers: servers}, MCPTarget{ServerURL: "https://loop.example/"})
	if err != nil || len(warnings) != 0 {
		t.Fatalf("err = %v, warnings = %v", err, warnings)
	}
	want := []string{"https://loop.example/runners/knowledge/mcp", "https://loop.example/mcp", "http://kb.example/mcp"}
	for i := range want {
		if got.MCPServers[i].URL != want[i] {
			t.Fatalf("server %d URL = %q, want %q", i, got.MCPServers[i].URL, want[i])
		}
	}
	if servers[0].URL != "http://127.0.0.1:8080/runners/knowledge/mcp" {
		t.Fatal("prepare mutated the caller's slice")
	}
}

func TestPrepareMCPServersDropsUnaddressableForCloud(t *testing.T) {
	got, warnings, err := PrepareMCPServers(domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{
		{ID: domain.KnowledgeMCPServerID, URL: "/runners/knowledge/mcp"},
		{ID: domain.PlatformMCPServerID, URL: "http://10.0.0.1:8080/mcp"},
		{ID: "relative-third-party", URL: "/not-ours"},
	}}, MCPTarget{})
	if err != nil || len(got.MCPServers) != 2 || got.MCPServers[0].ID != domain.PlatformMCPServerID {
		t.Fatalf("servers = %#v, err = %v", got.MCPServers, err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], domain.KnowledgeMCPServerID) || !strings.Contains(warnings[0], "AGENTFLOW_PLATFORM_MCP_URL") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestPrepareMCPServersTaihuPolicy(t *testing.T) {
	assets := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{{ID: "tapd", Name: "TAPD", AuthType: MCPAuthTaihuPAT, URL: "http://tapd/mcp"}}}
	if _, _, err := PrepareMCPServers(assets, MCPTarget{RequireTaihuPAT: true}); err == nil {
		t.Fatal("agent invocation without a Taihu PAT must fail")
	}
	got, _, err := PrepareMCPServers(assets, MCPTarget{})
	if err != nil || got.MCPServers[0].Headers["Authorization"] != "" {
		t.Fatalf("optional bind without token = %#v, %v", got.MCPServers, err)
	}
	got, _, err = PrepareMCPServers(assets, MCPTarget{TaihuPAT: "pat", RequireTaihuPAT: true})
	if err != nil || got.MCPServers[0].Headers["Authorization"] != "Bearer pat" {
		t.Fatalf("strict bind = %#v, %v", got.MCPServers, err)
	}
}
