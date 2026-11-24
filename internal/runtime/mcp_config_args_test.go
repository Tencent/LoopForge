package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// A stdio MCP server without args must not emit "args": null. pi rejects the
// whole server entry ("args must be an array of strings"), which silently
// removed the gate tool that every SOP node has to call.
func TestMCPConfigEntriesOmitEmptyArgs(t *testing.T) {
	entries, err := mcpConfigEntries([]domain.MCPServerDefinition{{
		ID: "agentflow-gate", Transport: "stdio",
		Command:     ".agentflow/config/fixer/gate-mcp",
		Environment: map[string]string{"AGENTFLOW_GATE_RESULT_PATH": "gate.json"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := entries["agentflow-gate"].(map[string]any)["args"]; ok {
		t.Fatalf("empty args must be omitted, got %v", entries["agentflow-gate"])
	}

	home := t.TempDir()
	if err := WriteAgentCLIMCPConfig(AgentCLIPi, HostAgentCLIHome(home), entries, AgentCLIMCPOptions{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".pi/agent/mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"args": null`) || strings.Contains(string(raw), `"args":null`) {
		t.Fatalf("pi mcp.json carries null args: %s", raw)
	}
}
