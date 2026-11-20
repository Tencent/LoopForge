package domain

import "testing"

func TestGitBranchIdentityPreservesCaseAndExplicitNamespaces(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{"main", "Main", false},
		{"refs/heads/Release", "origin/release", false},
		{"refs/heads/release", "origin/release", true},
		{"refs/remotes/origin/release", "release", true},
		{"refs/heads/origin/release", "release", false},
		{"refs/heads/origin/release", "refs/remotes/origin/origin/release", true},
	} {
		if got := SameGitBranch(tc.a, tc.b); got != tc.same {
			t.Errorf("SameGitBranch(%q, %q)=%v, want %v", tc.a, tc.b, got, tc.same)
		}
	}
	if got := ClassifyMRDeliveryState("merged", "Main", "main", "integration"); got != "integrated" {
		t.Errorf("merge to a different case-sensitive branch was labelled %q", got)
	}
	if BranchListed([]string{"refs/heads/Release"}, "release") {
		t.Error("case-only different ref claimed branch containment")
	}
}
