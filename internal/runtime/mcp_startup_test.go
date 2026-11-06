package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestMCPConfigEntriesOmitsEmptyArgs(t *testing.T) {
	entries, err := mcpConfigEntries([]domain.MCPServerDefinition{
		{ID: "agentflow-gate", Transport: "stdio", Command: ".agentflow/config/a/gate-mcp"},
		{ID: "tool", Transport: "stdio", Command: "python3", Args: []string{"server.py"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(entries)
	if strings.Contains(string(raw), "null") {
		t.Fatalf("Claude Code drops servers with null fields: %s", raw)
	}
	if _, ok := entries["agentflow-gate"].(map[string]any)["args"]; ok {
		t.Fatalf("argless server must omit args: %s", raw)
	}
	if args := entries["tool"].(map[string]any)["args"]; len(args.([]string)) != 1 {
		t.Fatalf("args lost: %s", raw)
	}
}

func TestClaudeInitReportsUnconnectedMCPServers(t *testing.T) {
	var events []domain.TelemetryEvent
	adapter := newProtocolAdapter(domain.ExecutorDefinition{Kind: "claude-code", Protocol: "stream-json"}, func(event domain.TelemetryEvent) {
		events = append(events, event)
	})
	adapter.Handle(domain.TelemetryEvent{Name: "process.stdout", Message: `{"type":"system","subtype":"init","session_id":"s","mcp_servers":[{"name":"agentflow-gate","status":"connected"},{"name":"browser","status":"failed"}]}`})
	var warned []string
	for _, event := range events {
		if event.Name == "mcp.unavailable" {
			warned = append(warned, event.Message)
		}
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "browser(failed)") || strings.Contains(warned[0], "agentflow-gate") {
		t.Fatalf("mcp warnings = %q", warned)
	}
}
