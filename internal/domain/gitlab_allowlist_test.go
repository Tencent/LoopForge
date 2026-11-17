package domain

import "testing"

// 回归（评审阻塞项）：token 只能发往管理员显式声明的 GitLab 实例。
// 未配置白名单时任何主机（含回环/内网）都不得放行，避免成员绑定
// gitlab:<任意 host> 后把 owner 的 Git 令牌当 PRIVATE-TOKEN 发出去。
func TestGitLabHostAllowedRequiresExplicitAllowlist(t *testing.T) {
	t.Setenv(GitLabAllowedHostsEnv, "")
	for _, host := range []string{
		"gitlab.example.test",
		"gitlab.example.test:8443",
		"127.0.0.1:8080",
		"10.1.2.3",
		"gitlab.corp.internal",
	} {
		if GitLabHostAllowed(host) {
			t.Fatalf("host %q must be rejected without an admin allowlist", host)
		}
	}
}

func TestGitLabHostAllowedMatchesConfiguredInstances(t *testing.T) {
	t.Setenv(GitLabAllowedHostsEnv, " https://GitLab.Example.Test , gitlab.port.test:8443 ,,")
	cases := map[string]bool{
		"gitlab.example.test":      true,  // scheme 前缀与大小写都归一化
		"GitLab.Example.Test":      true,  // host 匹配大小写不敏感
		"gitlab.example.test:8443": true,  // 只写 host 的条目匹配任意端口
		"gitlab.port.test:8443":    true,  // 带端口的条目精确匹配
		"gitlab.port.test":         false, // 条目已钉死端口
		"gitlab.port.test:9443":    false,
		"sub.gitlab.example.test":  false, // 不做通配/子串匹配
		"gitlab.attacker.example":  false,
		"":                         false,
	}
	for host, want := range cases {
		if got := GitLabHostAllowed(host); got != want {
			t.Fatalf("GitLabHostAllowed(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestGitLabInstanceHostParsesPrefixedID(t *testing.T) {
	host, ok := GitLabInstanceHost("gitlab:gitlab.example.test:8443/group/repo")
	if !ok || host != "gitlab.example.test:8443" {
		t.Fatalf("host = %q ok = %v, want gitlab.example.test:8443", host, ok)
	}
	for _, bad := range []string{"", "gitlab:", "gitlab:/group/repo", "gitlab:host", "group/repo"} {
		if _, ok := GitLabInstanceHost(bad); ok {
			t.Fatalf("GitLabInstanceHost(%q) must fail", bad)
		}
	}
}
