package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// Claude Code skips a stdio MCP server whose args is null, which hid the gate
// MCP (submit_gate_result) from SOP nodes. Empty args must not render as null.
func TestMCPConfigEntriesOmitsEmptyArgs(t *testing.T) {
	entries, err := mcpConfigEntries([]domain.MCPServerDefinition{
		{ID: "gate", Command: ".agentflow/gate-mcp"},
		{ID: "tool", Command: "node", Args: []string{"server.js", "--stdio"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "null") {
		t.Fatalf("MCP config must not contain null values: %s", data)
	}
	gate, _ := entries["gate"].(map[string]any)
	if _, ok := gate["args"]; ok {
		t.Fatalf("server without args must omit args: %+v", gate)
	}
	tool, _ := entries["tool"].(map[string]any)
	if args, _ := tool["args"].([]string); len(args) != 2 || args[0] != "server.js" || args[1] != "--stdio" {
		t.Fatalf("server args lost: %+v", tool)
	}
}
