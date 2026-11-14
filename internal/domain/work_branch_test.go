package domain

import (
	"testing"
	"time"
)

func TestDefaultWorkBranch(t *testing.T) {
	now := time.Date(2026, 8, 31, 15, 30, 0, 0, time.Local)

	tests := []struct {
		name  string
		issue Issue
		want  string
	}{
		{
			name: "gongfeng issue uses external iid",
			issue: Issue{
				ID: "gf-repo-18", Source: "gongfeng", Kind: "issue", Number: 7,
				ExternalRef: &ExternalRef{ExternalIID: 18},
			},
			want: "feature/fix-issue-18-20260831-1530",
		},
		{
			name: "tapd story uses tapd id not local number",
			issue: Issue{
				ID: "tapd-1000123456", Source: "tapd", Number: 42,
				ExternalRef: &ExternalRef{Source: "tapd", ExternalID: "1000123456", ExternalType: "story"},
			},
			want: "feature/story-1000123456-20260831-1530",
		},
		{
			name: "tapd without external ref falls back to issue id",
			issue: Issue{
				ID: "tapd-999", Source: "tapd", Number: 7,
			},
			want: "feature/story-999-20260831-1530",
		},
		{
			name: "local requirement uses requirement prefix",
			issue: Issue{
				ID: "issue-1", ProjectID: "platform", Kind: "requirement", Number: 5,
			},
			want: "feature/requirement-5-20260831-1530",
		},
		{
			name: "unknown source with number keeps legacy fix-issue prefix",
			issue: Issue{
				ID: "custom-1", Source: "custom", Number: 3,
			},
			want: "feature/fix-issue-3-20260831-1530",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DefaultWorkBranch(tc.issue, now); got != tc.want {
				t.Fatalf("DefaultWorkBranch() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSafeGitRefSegment(t *testing.T) {
	long := ""
	for len(long) < 80 {
		long += "a"
	}
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "trims surrounding blank and slash", value: " feature/hello world ", want: "feature-hello-world"},
		{name: "strips leading dots and lock suffix", value: "..bad.lock", want: "bad-lock"},
		{name: "collapses each separator rune to a dash", value: "a /  /b", want: "a-----b"},
		{name: "keeps upper case as lower case", value: "Feature-ABC_9", want: "feature-abc_9"},
		{name: "empty falls back to run", value: "", want: "run"},
		{name: "separators only fall back to run", value: " / . ", want: "run"},
		{name: "caps the segment at 64 characters", value: long, want: long[:64]},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SafeGitRefSegment(tc.value); got != tc.want {
				t.Fatalf("SafeGitRefSegment(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}
