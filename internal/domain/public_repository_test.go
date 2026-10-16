package domain

import "testing"

func TestPublicRepositoryAndPullRequestIdentity(t *testing.T) {
	for _, raw := range []string{"https://github.com/Tencent/LoopForge", "git@github.com:Tencent/LoopForge.git", "github:Tencent/LoopForge", "https://github.com/Tencent/LoopForge/tree/main"} {
		got, ok := ParseRepositoryURL(raw)
		if !ok || got.Project != "github:Tencent/LoopForge" || got.CloneURL != "https://github.com/Tencent/LoopForge.git" {
			t.Fatalf("%s: %+v %v", raw, got, ok)
		}
	}
	for _, raw := range []string{"https://github.com.attacker.invalid/owner/repo", "https://user:secret@github.com/owner/repo", "https://github.com/../repo", "https://github.com/owner"} {
		if got, ok := ParseRepositoryURL(raw); ok {
			t.Fatalf("invalid reference accepted: %s %+v", raw, got)
		}
	}
	p, n, u, ok := ParseGongfengMR("Delivered https://github.com/Tencent/LoopForge/pull/123")
	if !ok || p != "github:Tencent/LoopForge" || n != 123 || u != "https://github.com/Tencent/LoopForge/pull/123" {
		t.Fatalf("PR: %s %d %s %v", p, n, u, ok)
	}
}
