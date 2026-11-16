package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func readAgentCLIMCPDoc(t *testing.T, home, rel string) any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, rel))
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func normalizeJSON(t *testing.T, value any) any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestWriteAgentCLIMCPConfigOpenCodeServers(t *testing.T) {
	home := t.TempDir()
	entries := map[string]any{
		"http":      map[string]any{"type": "http", "url": "http://127.0.0.1:1/mcp", "headers": map[string]any{"Authorization": "Bearer x"}},
		"http-bare": map[string]any{"url": "http://127.0.0.1:2/mcp"},
		"stdio-str": map[string]any{"command": "node", "args": []string{"a.js", "--flag"}, "env": map[string]any{"K": "V"}, "cwd": "/w"},
		"stdio-any": map[string]any{"command": "py", "args": []any{"b.py"}},
		"empty":     map[string]any{},
		"invalid":   "not-a-map",
	}
	if err := WriteAgentCLIMCPConfig(AgentCLIOpenCode, HostAgentCLIHome(home), entries, AgentCLIMCPOptions{}); err != nil {
		t.Fatal(err)
	}
	got := readAgentCLIMCPDoc(t, home, ".config/opencode/opencode.json").(map[string]any)["mcp"]
	want := normalizeJSON(t, map[string]any{
		"http":      map[string]any{"type": "remote", "url": "http://127.0.0.1:1/mcp", "enabled": true, "timeout": 600000, "headers": map[string]any{"Authorization": "Bearer x"}},
		"http-bare": map[string]any{"type": "remote", "url": "http://127.0.0.1:2/mcp", "enabled": true, "timeout": 600000},
		"stdio-str": map[string]any{"type": "local", "command": []any{"node", "a.js", "--flag"}, "enabled": true, "timeout": 600000, "environment": map[string]any{"K": "V"}},
		"stdio-any": map[string]any{"type": "local", "command": []any{"py", "b.py"}, "enabled": true, "timeout": 600000},
	})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("opencode mcp:\n got %v\nwant %v", got, want)
	}
}

func TestWriteAgentCLIMCPConfigDeepSeekInserts(t *testing.T) {
	home := t.TempDir()
	longID := "agentflow.platform/" + strings.Repeat("x", 40)
	entries := map[string]any{
		"a-http":  map[string]any{"url": "http://127.0.0.1:1/mcp", "headers": map[string]any{"H": "1"}},
		"b.stdio": map[string]any{"command": "node", "args": []string{"s.js"}, "env": map[string]any{"K": "V"}, "cwd": "/w"},
		longID:    map[string]any{"command": "bin"},
		"skip":    map[string]any{},
	}
	if err := WriteAgentCLIMCPConfig(AgentCLIDeepSeek, HostAgentCLIHome(home), entries, AgentCLIMCPOptions{}); err != nil {
		t.Fatal(err)
	}
	longName := ("agentflow_platform_" + strings.Repeat("x", 40))[:32]
	want := normalizeJSON(t, []any{map[string]any{"insert": []any{
		map[string]any{"id": "agentflow-mcp-a-http", "name": "@deepseek-ai/dsh-mcp-client", "config": map[string]any{
			"serverName": "a-http", "toolCallTimeoutMs": 600000, "transport": "streamable-http", "url": "http://127.0.0.1:1/mcp", "headers": map[string]any{"H": "1"},
		}},
		map[string]any{"id": "agentflow-mcp-" + longName, "name": "@deepseek-ai/dsh-mcp-client", "config": map[string]any{
			"serverName": longName, "toolCallTimeoutMs": 600000, "transport": "stdio", "command": "bin",
		}},
		map[string]any{"id": "agentflow-mcp-b_stdio", "name": "@deepseek-ai/dsh-mcp-client", "config": map[string]any{
			"serverName": "b_stdio", "toolCallTimeoutMs": 600000, "transport": "stdio", "command": "node", "args": []any{"s.js"}, "env": map[string]any{"K": "V"}, "cwd": "/w",
		}},
	}}})
	rel := agentCLIMCPPaths[AgentCLIDeepSeek]
	if got := readAgentCLIMCPDoc(t, home, rel); !reflect.DeepEqual(got, want) {
		t.Fatalf("deepseek mcp:\n got %v\nwant %v", got, want)
	}

	empty := t.TempDir()
	if err := WriteAgentCLIMCPConfig(AgentCLIDeepSeek, HostAgentCLIHome(empty), map[string]any{"skip": map[string]any{}}, AgentCLIMCPOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := readAgentCLIMCPDoc(t, empty, rel); !reflect.DeepEqual(got, []any{}) {
		t.Fatalf("deepseek empty mcp: %v", got)
	}
}
