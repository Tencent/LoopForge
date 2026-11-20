package httpapi

import (
	"slices"
	"sort"
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
			// Hardening: a pre-existing --disallowedTools with a subset
			// value is merged (union) with the canonical safety list so
			// that no tool from the canonical list can be omitted.
			name: "merges pre-existing subset with full safety list",
			in:   []string{"cli", "--disallowedTools", "Bash", "--strict-mcp-config", "--", "prompt"},
			want: []string{"cli", "--strict-mcp-config", "--disallowedTools", nativeDisallowedTools, "--", "prompt"},
		},
		{
			// Hardening: equals syntax (--disallowedTools=Bash) is also
			// handled by dropNativeArg, so the merged list replaces it.
			name: "handles equals syntax",
			in:   []string{"cli", "--disallowedTools=Bash", "--", "prompt"},
			want: []string{"cli", "--disallowedTools", nativeDisallowedTools, "--strict-mcp-config", "--", "prompt"},
		},
		{
			// Hardening: a caller-supplied list that is stricter (includes
			// tools beyond the canonical set) must be preserved via union.
			name: "preserves extra tools from caller-supplied stricter list",
			in:   []string{"cli", "--disallowedTools", "Bash,CustomDangerousTool", "--", "prompt"},
			want: []string{"cli", "--disallowedTools", nativeDisallowedTools + ",CustomDangerousTool", "--strict-mcp-config", "--", "prompt"},
		},
		{
			// Hardening: equals syntax with extra tools beyond the
			// canonical list must also preserve them via union.
			name: "equals syntax keeps extra tools",
			in:   []string{"cli", "--disallowedTools=Bash,CustomDangerousTool", "--", "prompt"},
			want: []string{"cli", "--disallowedTools", nativeDisallowedTools + ",CustomDangerousTool", "--strict-mcp-config", "--", "prompt"},
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
// It splits on commas to avoid substring false positives.
func TestDisallowedToolsListIsComplete(t *testing.T) {
	actual := make(map[string]struct{})
	for _, s := range strings.Split(nativeDisallowedTools, ",") {
		actual[strings.TrimSpace(s)] = struct{}{}
	}
	critical := []string{
		"Bash", "PowerShell", "Monitor", "Write", "Edit", "MultiEdit",
		"NotebookEdit", "WebFetch", "WebSearch", "Task",
	}
	for _, tool := range critical {
		if _, ok := actual[tool]; !ok {
			t.Errorf("nativeDisallowedTools missing critical tool %q", tool)
		}
	}
}

func TestMergeDisallowedTools(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want []string // sorted for comparison
	}{
		{"empty a returns b", "", "Bash,Write", []string{"Bash", "Write"}},
		{"subset a adds nothing", "Bash", "Bash,Write,Edit", []string{"Bash", "Edit", "Write"}},
		{"superset a adds extras", "Bash,CustomTool", "Bash,Write", []string{"Bash", "CustomTool", "Write"}},
		{"disjoint sets get unioned", "Alpha,Beta", "Gamma,Delta", []string{"Alpha", "Beta", "Delta", "Gamma"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := mergeDisallowedTools(tc.a, tc.b)
			got := strings.Split(result, ",")
			sort.Strings(got)
			want := tc.want
			sort.Strings(want)
			if !slices.Equal(got, want) {
				t.Fatalf("mergeDisallowedTools(%q, %q) = %q, want %q", tc.a, tc.b, got, want)
			}
		})
	}
}
