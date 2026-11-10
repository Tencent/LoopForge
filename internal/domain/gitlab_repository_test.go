package domain

import "testing"

// 测试主机名全部使用占位符（RFC2606 保留域 example.test），
// 不出现任何真实内网地址或项目路由。ParseGitLabRepositoryURL 的裸 URL 路径
// 要求可信内网主机，因此示例统一使用显式 gitlab: 前缀形态（管理员声明，任意主机）。

func TestParseGitLabRepositoryURL(t *testing.T) {
	want := "gitlab:gitlab.example.test/group/repository"
	cases := []string{
		"gitlab:gitlab.example.test/group/repository",
		"gitlab:gitlab.example.test/group/repository.git",
	}
	for _, raw := range cases {
		got, ok := ParseGitLabRepositoryURL(raw)
		if !ok || got.Project != want {
			t.Fatalf("%s: got %+v ok=%v want %s", raw, got, ok, want)
		}
		if got.Name != "repository" || got.CloneURL != "http://gitlab.example.test/group/repository.git" {
			t.Fatalf("%s: name=%s clone=%s", raw, got.Name, got.CloneURL)
		}
	}
}

func TestParseGitLabRepositoryURLWithPort(t *testing.T) {
	got, ok := ParseGitLabRepositoryURL("gitlab:gitlab.example.test:8443/team/service")
	if !ok {
		t.Fatalf("port URL rejected")
	}
	if got.Project != "gitlab:gitlab.example.test:8443/team/service" {
		t.Fatalf("got %s", got.Project)
	}
}

func TestParseGitLabRepositoryURLRejectsImpersonation(t *testing.T) {
	for _, raw := range []string{
		"https://github.com.attacker.example/owner/repo",
		"https://evil.example/owner/repo",
		"http://user:secret@gitlab.example.test/g/r",
		"gitlab.example.test",        // 无项目路径
		"gitlab:gitlab.example.test", // 无项目路径
		"gitlab:/group/repo",         // 缺主机
	} {
		if got, ok := ParseGitLabRepositoryURL(raw); ok {
			t.Fatalf("invalid reference accepted: %s %+v", raw, got)
		}
	}
	// 显式 gitlab: 前缀是信任边界内的管理员声明，可指向任意主机（含公网自建）。
	if got, ok := ParseGitLabRepositoryURL("gitlab:gitlab.self-hosted.example/team/repo"); !ok ||
		got.Project != "gitlab:gitlab.self-hosted.example/team/repo" {
		t.Fatalf("explicit prefix rejected: %+v %v", got, ok)
	}
}

func TestIsTrustedGitLabHost(t *testing.T) {
	// 私有/回环网段与 172.16/12 规划段应受信任（只断言网段类别，不引用真实主机）。
	for _, host := range []string{"127.0.0.1", "10.0.0.1", "192.168.0.1", "172.16.0.1", "172.31.255.254"} {
		if !isTrustedGitLabHost(host) {
			t.Errorf("internal range host %s should be trusted", host)
		}
	}
	// 公网地址、仿冒主机名、空值不受信任（192.0.2.x/198.51.100.x/203.0.113.x 为文档保留段）。
	for _, host := range []string{"203.0.113.1", "198.51.100.2", "192.0.2.3", "github.com.attacker.example", "gitlab.public.example", ""} {
		if isTrustedGitLabHost(host) {
			t.Errorf("external host %s must not be trusted", host)
		}
	}
}

func TestParseRepositoryURLKeepsGitHubAndGitLabApart(t *testing.T) {
	if got, ok := ParseRepositoryURL("https://github.com/Tencent/LoopForge"); !ok ||
		got.Project != "github:Tencent/LoopForge" {
		t.Fatalf("github routing broken: %+v %v", got, ok)
	}
	if got, ok := ParseRepositoryURL("gitlab:gitlab.example.test/group/repo"); !ok ||
		got.Project != "gitlab:gitlab.example.test/group/repo" {
		t.Fatalf("gitlab prefix routing broken: %+v %v", got, ok)
	}
	if got, ok := ParseRepositoryURL("https://github.com.attacker.example/owner/repo"); ok {
		t.Fatalf("impersonation accepted: %+v", got)
	}
}
