package httpapi

import (
	"slices"
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
			name: "keeps existing safety flags",
			in:   []string{"cli", "--disallowedTools", "Bash", "--strict-mcp-config", "--", "prompt"},
			want: []string{"cli", "--disallowedTools", "Bash", "--strict-mcp-config", "--", "prompt"},
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
