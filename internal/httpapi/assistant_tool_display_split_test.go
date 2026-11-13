package httpapi

import (
	"encoding/json"
	"testing"
)

func TestAssistantWrappedToolParams(t *testing.T) {
	input := json.RawMessage(`{"toolName":"x"}`)
	cases := []struct {
		name string
		m    map[string]any
		want string
	}{
		{"missing params falls back", map[string]any{"toolName": "x"}, `{"toolName":"x"}`},
		{"string params verbatim", map[string]any{"params": `{"a":1}`}, `{"a":1}`},
		{"object params re-encoded", map[string]any{"params": map[string]any{"b": "c"}}, `{"b":"c"}`},
		{"nil map falls back", nil, `{"toolName":"x"}`},
	}
	for _, tc := range cases {
		if got := string(assistantWrappedToolParams(input, tc.m)); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestAssistantToolSearchNames(t *testing.T) {
	cases := []struct {
		name string
		m    map[string]any
		want string
	}{
		{"tool_names trimmed and filtered", map[string]any{"tool_names": []any{" Read ", "", 3, "Edit"}}, "Read, Edit"},
		{"queries used when tool_names blank", map[string]any{"tool_names": []any{" "}, "queries": []any{"slack"}}, "slack"},
		{"tool_names preferred", map[string]any{"tool_names": []any{"A"}, "queries": []any{"B"}}, "A"},
		{"no lists", map[string]any{"other": "x"}, ""},
		{"nil map", nil, ""},
	}
	for _, tc := range cases {
		if got := assistantToolSearchNames(tc.m); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestAssistantToolDisplayToolSearchAndWrapper(t *testing.T) {
	if title, detail := assistantToolDisplay("ToolSearch", json.RawMessage(`{"tool_names":["Read","Edit"]}`)); title != "ToolSearch" || detail != "Read, Edit" {
		t.Fatalf("ToolSearch names = %q %q", title, detail)
	}
	if title, detail := assistantToolDisplay("ToolSearch", json.RawMessage(`{"query":"q1","tool_names":["Read"]}`)); title != "ToolSearch" || detail != "q1" {
		t.Fatalf("ToolSearch query = %q %q", title, detail)
	}
	if title, detail := assistantToolDisplay("ToolSearch", json.RawMessage(`{}`)); title != "ToolSearch" || detail != "" {
		t.Fatalf("ToolSearch empty = %q %q", title, detail)
	}
	if title, detail := assistantToolDisplay("MCP", json.RawMessage(`{"toolName":"Bash","params":"{\"command\":\"ls -la\"}"}`)); title != "Bash" || detail != "ls -la" {
		t.Fatalf("MCP string params = %q %q", title, detail)
	}
	if title, detail := assistantToolDisplay("DeferExecuteTool", json.RawMessage(`{"toolName":"Bash","params":{"command":"pwd"}}`)); title != "Bash" || detail != "pwd" {
		t.Fatalf("DeferExecuteTool object params = %q %q", title, detail)
	}
}
