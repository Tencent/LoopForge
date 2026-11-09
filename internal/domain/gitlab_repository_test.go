package domain

import "testing"

func TestParseGitLabRepositoryURL(t *testing.T) {
	want := "gitlab:172.32.149.204/jsrd_chatai/weknora"
	cases := []string{
		"http://172.32.149.204/jsrd_chatai/weknora",
		"http://172.32.149.204/jsrd_chatai/weknora.git",
		"git@172.32.149.204:jsrd_chatai/weknora.git",
		"gitlab:172.32.149.204/jsrd_chatai/weknora",
	}
	for _, raw := range cases {
		got, ok := ParseGitLabRepositoryURL(raw)
		if !ok || got.Project != want {
			t.Fatalf("%s: got %+v ok=%v want %s", raw, got, ok, want)
		}
		if got.Name != "weknora" || got.CloneURL != "http://172.32.149.204/jsrd_chatai/weknora.git" {
			t.Fatalf("%s: name=%s clone=%s", raw, got.Name, got.CloneURL)
		}
	}
}

func TestParseGitLabRepositoryURLWithPort(t *testing.T) {
	got, ok := ParseGitLabRepositoryURL("http://172.32.153.183:8180/du/knowledge-base")
	if !ok {
		t.Fatalf("port URL rejected")
	}
	if got.Project != "gitlab:172.32.153.183:8180/du/knowledge-base" {
		t.Fatalf("got %s", got.Project)
	}
}

func TestParseGitLabRepositoryURLRejectsImpersonation(t *testing.T) {
	// 仿冒主机名（公网域名、github 变体）不得被当成自托管 GitLab。
	for _, raw := range []string{
		"https://github.com.attacker.invalid/owner/repo",
		"https://evil.example.com/owner/repo",
		"http://user:secret@172.32.149.204/g/r",
		"172.32.149.204",          // 无项目路径
		"gitlab:172.32.149.204",   // 无项目路径
		"gitlab:/jsrd_chatai/x",   // 缺主机
	} {
		if got, ok := ParseGitLabRepositoryURL(raw); ok {
			t.Fatalf("invalid reference accepted: %s %+v", raw, got)
		}
	}
	// 但显式 gitlab: 前缀是信任边界内的管理员操作，可指向任意主机（含公网自建）。
	if got, ok := ParseGitLabRepositoryURL("gitlab:gitlab.example.com/team/repo"); !ok ||
		got.Project != "gitlab:gitlab.example.com/team/repo" {
		t.Fatalf("explicit prefix rejected: %+v %v", got, ok)
	}
}

func TestParseRepositoryURLRoutesTrustedGitLabHost(t *testing.T) {
	// 内网 IP 走 GitLab 解析。
	got, ok := ParseRepositoryURL("http://172.32.149.204/jsrd_chatai/weknora")
	if !ok || got.Project != "gitlab:172.32.149.204/jsrd_chatai/weknora" {
		t.Fatalf("internal host not routed to gitlab: %+v %v", got, ok)
	}
	// 公网域名仍被拒（安全测试不能回归）。
	if got, ok := ParseRepositoryURL("https://github.com.attacker.invalid/owner/repo"); ok {
		t.Fatalf("impersonation accepted: %+v", got)
	}
}
