package domain

import "testing"

func TestGitLabMRIdentity(t *testing.T) {
	u := "http://gitlab.example.com:8443/team/sub/repo/-/merge_requests/42"
	project, iid, got, ok := ParseGongfengMR(u)
	if !ok || project != "gitlab:http://gitlab.example.com:8443/team/sub/repo" || iid != 42 || got != u {
		t.Fatalf("bad GitLab MR parse: %q %d %q %v", project, iid, got, ok)
	}
	if canonical := GongfengMRURL(project, iid); canonical != u {
		t.Fatalf("bad URL: %s", canonical)
	}
}
