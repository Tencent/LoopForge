package engine

import (
	"testing"

	"agentflow/internal/domain"
)

// Agent 节点的门禁只带角色绑定的 MCP；与角色无关的项目级 stdio MCP 安装失败不能拖垮门禁。
func TestGateMCPAssetsFollowTheNodeRole(t *testing.T) {
	e, st := cvEEngine(t)
	for _, server := range []domain.MCPServerDefinition{
		{ID: "mcp-bound", Name: "Bound", ProjectID: "proj", Scope: "project", Transport: "http", URL: "https://example.test/bound"},
		{ID: "mcp-unrelated", Name: "Unrelated", ProjectID: "proj", Scope: "project", Transport: "stdio", Command: "zg"},
	} {
		if err := st.PutMCPServer(server); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutAgent(domain.AgentDefinition{ID: "tester", Name: "Tester", ProjectID: "proj", MCPServers: []string{"mcp-bound"}}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{
		ID: "gate-mcp", ProjectID: "proj", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "agent", Type: "agent", AgentID: "tester"},
			{ID: "script", Type: "command", Command: "true"},
		},
	}
	run := domain.Run{ID: "gate-mcp-run", WorkflowID: wf.ID, WorkflowSnapshot: wf, ExecutionContext: domain.TelemetryContext{ProjectID: "proj"}}

	names := func(bundle domain.AgentAssetBundle) map[string]bool {
		out := map[string]bool{}
		for _, server := range bundle.MCPServers {
			out[server.ID] = true
		}
		return out
	}
	if got := names(e.gateMCPAssets(run, "agent")); !got["mcp-bound"] || got["mcp-unrelated"] {
		t.Fatalf("agent gate MCP servers = %v, want only the role's", got)
	}
	if got := names(e.gateMCPAssets(run, "script")); !got["mcp-bound"] || !got["mcp-unrelated"] {
		t.Fatalf("script gate MCP servers = %v, want every project MCP", got)
	}
	if inv := e.gateCommandInvocation(run, "agent"); len(inv.MCPCallServers) != 1 || inv.MCPCallServers[0].ID != "mcp-bound" {
		t.Fatalf("gate invocation MCP servers = %+v", inv.MCPCallServers)
	}
}
