package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestMCPConfigEntriesNeverWriteNullArgs(t *testing.T) {
	entries, err := mcpConfigEntries([]domain.MCPServerDefinition{{
		ID:          gateMCPServerID,
		Transport:   "stdio",
		Command:     ".agentflow/config/fixer/gate-mcp",
		Environment: map[string]string{"AGENTFLOW_GATE_RESULT_PATH": ".agentflow/gate-results/fix/attempt-1.json"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"mcpServers": entries})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"args":null`) {
		t.Fatalf("stdio entry without arguments must not write args as null: %s", raw)
	}
	if !strings.Contains(string(raw), `"args":[]`) {
		t.Fatalf("stdio entry without arguments should write an empty args array: %s", raw)
	}
}
