package domain

import "testing"

func TestGongfengMRKey(t *testing.T) {
	t.Parallel()
	if got := gongfengMRKey("Team/Frontend", 7); got != "team/frontend#7" {
		t.Fatalf("key=%q", got)
	}
}

func TestAnalyticsMRKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		mr   DeliveryMR
		want string
	}{
		{"url wins over fields", DeliveryMR{Project: "other/repo", IID: 9, URL: "http://git.example.com/Team/Frontend/merge_requests/1"}, "team/frontend#1"},
		{"fields when url unparseable", DeliveryMR{Project: "Team/Backend", IID: 2, URL: "local"}, "team/backend#2"},
		{"trimmed url fallback", DeliveryMR{URL: "  local-mr  "}, "local-mr"},
		{"invalid iid falls back to url", DeliveryMR{Project: "team/x", URL: "a"}, "a"},
		{"empty", DeliveryMR{}, ""},
	}
	for _, tc := range cases {
		if got := analyticsMRKey(tc.mr); got != tc.want {
			t.Errorf("%s: key=%q want %q", tc.name, got, tc.want)
		}
	}
}

func TestParseDeliveryMRURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, project, url string
		iid              int
	}{
		{"https://github.com/acme/web/pull/9", "github:acme/web", "https://github.com/acme/web/pull/9", 9},
		{"http://git.example.com/team/app/merge_requests/2", "team/app", "https://git.example.com/team/app/-/merge_requests/2", 2},
		{" https://GHE.Example.com:443/team/svc/pull/4/ ", "ghe:ghe.example.com/team/svc", "https://ghe.example.com/team/svc/pull/4", 4},
		{"https://gitlab.example.org/group/sub/app/-/merge_requests/3", "gitlab:gitlab.example.org/group/sub/app", "https://gitlab.example.org/group/sub/app/-/merge_requests/3", 3},
		{"https://gitlab.example.org:8443/g/r/-/merge_requests/1", "gitlab:gitlab.example.org:8443/g/r", "https://gitlab.example.org:8443/g/r/-/merge_requests/1", 1},
	}
	for _, tc := range cases {
		project, iid, url, ok := ParseDeliveryMRURL(tc.in)
		if !ok || project != tc.project || iid != tc.iid || url != tc.url {
			t.Errorf("%q = %q %d %q %v", tc.in, project, iid, url, ok)
		}
		if got := GongfengMRURL(project, iid); got != tc.url {
			t.Errorf("GongfengMRURL(%q, %d) = %q", project, iid, got)
		}
	}
	for _, in := range []string{
		"http://ghe.example.com/team/svc/pull/4",
		"https://user:tok@ghe.example.com/team/svc/pull/4",
		"https://ghe.example.com/a/b/c/pull/4",
		"https://ghe.example.com/team/svc/pull/0",
		"see https://ghe.example.com/team/svc/pull/4",
		"https://gitlab.example.org/g/../r/-/merge_requests/1",
		"https://gitlab.example.org/g%2Fr/-/merge_requests/1",
		"https://localhost/g/r/-/merge_requests/1",
		"https://github.com/g/r/-/merge_requests/1",
	} {
		if project, iid, _, ok := ParseDeliveryMRURL(in); ok {
			t.Errorf("%q parsed as %q %d", in, project, iid)
		}
	}
}

func TestExternalMRProject(t *testing.T) {
	t.Parallel()
	if platform, host, path, ok := ExternalMRProject("gitlab:gitlab.example.org/g/s/r"); !ok || platform != GitPlatformGitLab || host != "gitlab.example.org" || path != "g/s/r" {
		t.Fatalf("gitlab = %q %q %q %v", platform, host, path, ok)
	}
	for _, project := range []string{"github:acme/web", "team/app", "ghe:github.com/a/b", "ghe:ghe.example.com/a/b/c", "gitlab:gitlab.example.org/r", "gitlab:gitlab.example.org/../r"} {
		if _, _, _, ok := ExternalMRProject(project); ok {
			t.Errorf("%q accepted", project)
		}
	}
}
