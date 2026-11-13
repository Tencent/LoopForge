package httpapi

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestGitHubPRFilePatchKeepsShortPatchAndChargesBudget(t *testing.T) {
	kept, omitted, remaining := githubPRFilePatch("@@ -1 +1 @@\n+改动", 100)
	if kept != "@@ -1 +1 @@\n+改动" || omitted || remaining != 100-utf8.RuneCountInString(kept) {
		t.Fatalf("short patch = %q omitted=%t remaining=%d", kept, omitted, remaining)
	}
}

func TestGitHubPRFilePatchTruncatesLongPatchByRunes(t *testing.T) {
	long := strings.Repeat("改", githubPRFilePatchRunes+10)
	kept, omitted, remaining := githubPRFilePatch(long, githubPRFilesPatchBudget)
	want := strings.Repeat("改", githubPRFilePatchRunes) + "\n…（该文件 diff 过长已截断）"
	if kept != want || omitted {
		t.Fatalf("long patch kept %d runes omitted=%t", utf8.RuneCountInString(kept), omitted)
	}
	if remaining != githubPRFilesPatchBudget-utf8.RuneCountInString(want) {
		t.Fatalf("remaining = %d", remaining)
	}
}

func TestGitHubPRFilePatchBudgetBoundary(t *testing.T) {
	for _, tc := range []struct {
		name          string
		patch         string
		budget        int
		wantKept      string
		wantOmitted   bool
		wantRemaining int
	}{
		{name: "exactly fits", patch: "abcd", budget: 4, wantKept: "abcd", wantRemaining: 0},
		{name: "over budget", patch: "abcde", budget: 4, wantOmitted: true, wantRemaining: 4},
		{name: "budget exhausted", patch: "a", budget: 0, wantOmitted: true, wantRemaining: 0},
		{name: "empty patch", patch: "", budget: 0, wantRemaining: 0},
		{name: "empty patch keeps budget", patch: "", budget: 7, wantRemaining: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kept, omitted, remaining := githubPRFilePatch(tc.patch, tc.budget)
			if kept != tc.wantKept || omitted != tc.wantOmitted || remaining != tc.wantRemaining {
				t.Fatalf("githubPRFilePatch(%q, %d) = %q, %t, %d", tc.patch, tc.budget, kept, omitted, remaining)
			}
		})
	}
}
