package domain

import "testing"

func TestParseRepositoryURLCNB(t *testing.T) {
	cases := []struct {
		in      string
		project string
		name    string
	}{
		{"https://cnb.cool/org/repo", "cnb:org/repo", "repo"},
		{"https://cnb.cool/org/repo.git", "cnb:org/repo", "repo"},
		{"https://cnb.cool/org/team/repo", "cnb:org/team/repo", "repo"},
		{"https://cnb.cool/org/team/sub/repo.git", "cnb:org/team/sub/repo", "repo"},
		{"https://cnb.cool/org/repo/-/tree/main", "cnb:org/repo", "repo"},
		{"https://cnb.cool/org/team/repo/-/pulls/12", "cnb:org/team/repo", "repo"},
		{"https://cnb.cool/org/repo/-/compare/main...dev?x=1#y", "cnb:org/repo", "repo"},
		{"git clone https://cnb.cool/org/repo.git", "cnb:org/repo", "repo"},
		{"cnb.cool/org/repo", "cnb:org/repo", "repo"},
		{"cnb:org/team/repo", "cnb:org/team/repo", "repo"},
	}
	for _, tc := range cases {
		info, ok := ParseRepositoryURL(tc.in)
		if !ok || info.Project != tc.project || info.Name != tc.name {
			t.Errorf("ParseRepositoryURL(%q) = %+v, %v", tc.in, info, ok)
			continue
		}
		if info.WebURL != "https://cnb.cool/"+tc.project[len("cnb:"):] || info.CloneURL != info.WebURL+".git" {
			t.Errorf("ParseRepositoryURL(%q) urls = %q %q", tc.in, info.WebURL, info.CloneURL)
		}
	}
}

func TestParseRepositoryURLRejectsInvalidCNB(t *testing.T) {
	for _, in := range []string{
		"https://cnb.cool/org",
		"https://cnb.cool/",
		"https://cnb.cool/org/../repo",
		"https://cnb.cool/./repo",
		"https://user:pw@cnb.cool/org/repo.git",
		"https://token@cnb.cool/org/repo.git",
		"http://cnb.cool/org/repo.git",
		"https://cnb.cool.evil.example/org/repo.git",
		"https://notcnb.cool/org/repo.git",
		"https://cnb.cool/org/re po",
		"cnb:org",
		"cnb:org/../x",
	} {
		if info, ok := ParseRepositoryURL(in); ok {
			t.Errorf("ParseRepositoryURL(%q) accepted as %+v", in, info)
		}
	}
}

func TestCNBPullURLRoundTrip(t *testing.T) {
	if got := GongfengMRURL("cnb:org/team/repo", 7); got != "https://cnb.cool/org/team/repo/-/pulls/7" {
		t.Fatalf("GongfengMRURL = %q", got)
	}
	refs := ParseGongfengMRs("see https://cnb.cool/org/team/repo/-/pulls/7 and https://cnb.cool/org/repo/-/pulls/8.")
	if len(refs) != 2 || refs[0].Project != "cnb:org/team/repo" || refs[0].IID != 7 || refs[0].URL != "https://cnb.cool/org/team/repo/-/pulls/7" || refs[1].Project != "cnb:org/repo" || refs[1].IID != 8 {
		t.Fatalf("ParseGongfengMRs = %+v", refs)
	}
	if !SameGongfengMR("https://cnb.cool/org/repo/-/pulls/8", "http://cnb.cool/org/repo/-/pulls/8") {
		t.Fatal("scheme must not distinguish a pull request")
	}
}
