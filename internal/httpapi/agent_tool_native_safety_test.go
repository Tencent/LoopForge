package httpapi

import (
	"slices"
	"strings"
	"testing"
)

func TestApplyNativeToolSafety(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "drops tools and appends safety flags before delimiter",
			in:   []string{"cli", "--tools", "Read,Grep", "--tools=Glob", "-p", "--", "prompt"},
			want: []string{"cli", "-p", "--disallowedTools", nativeDisallowedTools, "--strict-mcp-config", "--", "prompt"},
		},
		{
			name: "appends at end without delimiter",
			in:   []string{"cli"},
			want: []string{"cli", "--disallowedTools", nativeDisallowedTools, "--strict-mcp-config"},
		},
		{
			// Regression test for sandbox escape: a pre-existing
			// --disallowedTools with a subset value MUST be overridden
			// with the full canonical safety list. Previously, the
			// function skipped enforcement when the flag already existed,
			// allowing bypass of Write, Edit, PowerShell, etc.
			name: "overrides pre-existing subset disallowedTools with full safety list",
			in:   []string{"cli", "--disallowedTools", "Bash", "--strict-mcp-config", "--", "prompt"},
			want: []string{"cli", "--disallowedTools", nativeDisallowedTools, "--strict-mcp-config", "--", "prompt"},
		},
		{
			// Regression: --disallowedTools=value syntax must also be
			// replaced (replaceArgValue handles both forms).
			name: "overrides pre-existing disallowedTools with equals syntax",
			in:   []string{"cli", "--disallowedTools", "Bash,PowerShell", "--", "prompt"},
			want: []string{"cli", "--disallowedTools", nativeDisallowedTools, "--", "prompt"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := applyNativeToolSafety(slices.Clone(tc.in), AgentToolAssistant)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("applyNativeToolSafety(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestDisallowedToolsListIsComplete verifies the canonical disallowed list
// contains all critical dangerous tools that must never be available.
func TestDisallowedToolsListIsComplete(t *testing.T) {
	critical := []string{
		"Bash", "PowerShell", "Monitor", "Write", "Edit", "MultiEdit",
		"NotebookEdit", "WebFetch", "WebSearch", "Task",
	}
	for _, tool := range critical {
		if !strings.Contains(nativeDisallowedTools, tool) {
			t.Errorf("nativeDisallowedTools missing critical tool %q", tool)
		}
	}
}
