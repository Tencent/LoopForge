package domain

import "testing"

func TestGitLabRepositoryIsBoundToInstance(t *testing.T) {
	base := "http://gitlab.example.com:8443"
	for _, raw := range []string{"http://gitlab.example.com:8443/team/sub/repo.git", "git@gitlab.example.com:team/sub/repo.git"} {
		info, ok := ParseGitLabRepositoryURL(raw, base)
		if !ok || info.Project != "gitlab:http://gitlab.example.com:8443/team/sub/repo" {
			t.Fatalf("unexpected parse %q: %+v %v", raw, info, ok)
		}
	}
	for _, raw := range []string{"http://gitlab.example.com.attacker.invalid:9530/team/repo", "https://gitlab.example.com:8443/team/repo", "http://user@gitlab.example.com:8443/team/repo"} {
		if _, ok := ParseGitLabRepositoryURL(raw, base); ok {
			t.Fatalf("accepted %q", raw)
		}
	}
}
