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
		// 前缀不带 scheme 信息：按安全默认 https，绝不静默降级成明文 http。
		if got.Name != "repository" || got.CloneURL != "https://gitlab.example.test/group/repository.git" {
			t.Fatalf("%s: name=%s clone=%s", raw, got.Name, got.CloneURL)
		}
	}
}

func TestParseGitLabRepositoryURLPreservesScheme(t *testing.T) {
	// 显式 https 保留；显式 http（内网实例常见）也保留，来源 URL 是权威。
	if got, ok := ParseGitLabRepositoryURL("https://gitlab.example.internal/g/r"); !ok ||
		got.WebURL != "https://gitlab.example.internal/g/r" || got.CloneURL != "https://gitlab.example.internal/g/r.git" {
		t.Fatalf("https must be preserved: %+v %v", got, ok)
	}
	if got, ok := ParseGitLabRepositoryURL("http://gitlab.example.internal/g/r"); !ok ||
		got.WebURL != "http://gitlab.example.internal/g/r" || got.CloneURL != "http://gitlab.example.internal/g/r.git" {
		t.Fatalf("explicit http must be kept for internal instances: %+v %v", got, ok)
	}
}

func TestGitLabInstanceScheme(t *testing.T) {
	// host[:port] 与标识一致时取 URL 的 scheme；不匹配或缺失时默认 https。
	if s := GitLabInstanceScheme("gitlab.example.test:8443", "https://gitlab.example.test:8443/g/r.git", "http://other/g/r"); s != "https" {
		t.Fatalf("matched url scheme = %s, want https", s)
	}
	if s := GitLabInstanceScheme("gitlab.example.test:8443", "http://gitlab.example.test:8443/g/r.git"); s != "http" {
		t.Fatalf("explicit http record = %s, want http", s)
	}
	if s := GitLabInstanceScheme("gitlab.example.test", "https://other.example.test/g/r", ""); s != "https" {
		t.Fatalf("unmatched urls must fall back to https, got %s", s)
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
	// 私有/回环网段应受信任（只断言网段类别，不引用真实主机）。
	for _, host := range []string{"127.0.0.1", "10.0.0.1", "192.168.0.1", "172.16.0.1", "172.31.255.254"} {
		if !isTrustedGitLabHost(host) {
			t.Errorf("internal range host %s should be trusted", host)
		}
	}
	// 公网地址、仿冒主机名、空值不受信任（192.0.2.x/198.51.100.x/203.0.113.x 为文档保留段）。
	// 172.0.0.0/8 内 RFC1918 之外的公网段（172.217/16 Google、172.64/13 Cloudflare）
	// 必须拒绝：整段放行等于让公网主机冒充内网实例。
	for _, host := range []string{"203.0.113.1", "198.51.100.2", "192.0.2.3", "172.217.0.1", "172.64.1.1", "172.32.0.1", "172.149.0.1", "172.153.0.1", "github.com.attacker.example", "gitlab.public.example", ""} {
		if isTrustedGitLabHost(host) {
			t.Errorf("external host %s must not be trusted", host)
		}
	}
	// 非常规网段走显式 gitlab: 前缀声明（管理员行为，任意主机含公网自建）。
	if got, ok := ParseGitLabRepositoryURL("gitlab:gitlab.self-hosted.example/team/repo"); !ok {
		t.Fatalf("explicit prefix must remain the escape hatch for non-RFC1918 instances: %+v", got)
	}
	if got, ok := ParseGitLabRepositoryURL("http://172.217.0.1/group/repo"); ok {
		t.Fatalf("bare public 172.x URL must be rejected without the prefix: %+v", got)
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
