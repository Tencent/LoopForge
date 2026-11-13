package runtime

import "testing"

func TestHasClosedFrontmatterMatchesSplitFrontmatter(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"empty", "", false},
		{"no frontmatter", "# Title\nbody\n", false},
		{"unclosed", "---\nname: x\nbody\n", false},
		{"closed", "---\nname: x\n---\nbody\n", true},
		{"closed crlf", "---\r\nname: x\r\n---\r\nbody\r\n", true},
		{"leading whitespace", "\n\n  ---\nname: x\n---\n", true},
		{"empty header collapses", "---\n---\n", false},
		{"dashes only", "---", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hasClosedFrontmatter([]byte(tc.raw))
			if got != tc.want {
				t.Fatalf("hasClosedFrontmatter(%q) = %v, want %v", tc.raw, got, tc.want)
			}
			if _, _, ok := splitFrontmatter([]byte(tc.raw)); ok != got {
				t.Fatalf("splitFrontmatter(%q) ok = %v, hasClosedFrontmatter = %v", tc.raw, ok, got)
			}
		})
	}
}
