package knowledge

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestProxyTokenRoundTripAndRevocation(t *testing.T) {
	settings := "session-a"
	now := time.Unix(1_800_000_000, 0)
	token, err := SignProxyToken(settings, ProxyClaims{ProjectID: "p1", UserID: 7, AgentID: "dev"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(token, "upstream-a") {
		t.Fatal("proxy token must not embed the upstream credential")
	}
	claims, err := VerifyProxyToken(settings, token, now.Add(time.Hour))
	if err != nil || claims.ProjectID != "p1" || claims.UserID != 7 || claims.AgentID != "dev" {
		t.Fatalf("verify = %#v, %v", claims, err)
	}
	if _, err := VerifyProxyToken(settings, token, now.Add(ProxyTokenTTL+time.Second)); err == nil {
		t.Fatal("expired token accepted")
	}
	rotated := settings
	rotated = "session-b"
	if _, err := VerifyProxyToken(rotated, token, now); err == nil {
		t.Fatal("rotating the upstream token must revoke outstanding proxy tokens")
	}
	tampered := strings.Replace(token, token[len(proxyTokenPrefix):len(proxyTokenPrefix)+4], "AAAA", 1)
	if _, err := VerifyProxyToken(settings, tampered, now); err == nil {
		t.Fatal("tampered token accepted")
	}
	if _, err := SignProxyToken("", ProxyClaims{}, now); err == nil {
		t.Fatal("signing without an upstream token must fail")
	}
}

func TestScopeSources(t *testing.T) {
	cases := []struct {
		name               string
		allowed, requested []string
		want               []string
		wantErr            bool
	}{
		{"unbound rejects request", nil, []string{"a"}, nil, true},
		{"unrestricted empty stays empty", nil, nil, []string{}, false},
		{"empty request expands", []string{"a", "b"}, nil, []string{"a", "b"}, false},
		{"subset accepted", []string{"a", "b"}, []string{" b "}, []string{"b"}, false},
		{"outside rejected", []string{"a"}, []string{"a", "c"}, nil, true},
	}
	for _, tc := range cases {
		got, err := ScopeSources(tc.allowed, tc.requested)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: err = %v", tc.name, err)
		}
		if !tc.wantErr && !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: got %#v want %#v", tc.name, got, tc.want)
		}
	}
}

func TestParseDistilled(t *testing.T) {
	got := ParseDistilled("---\ntitle: \"帕鲁断联排查\"\nsource: lighthouse-kb\npath: playbooks/palu.md\n---\n\n## 现象\n正文")
	if got.Title != "帕鲁断联排查" || got.Source != "lighthouse-kb" || got.Path != "playbooks/palu.md" || got.Body != "## 现象\n正文" {
		t.Fatalf("front matter parse = %#v", got)
	}
	plain := ParseDistilled("intro\n# 标题一\nbody")
	if plain.Title != "标题一" || plain.Body != "intro\n# 标题一\nbody" {
		t.Fatalf("heading fallback = %#v", plain)
	}
}

func TestNormalizeDocPath(t *testing.T) {
	for in, want := range map[string]string{
		"guides/a.md":   "guides/a.md",
		" guides/a.md ": "guides/a.md",
		"排障/帕鲁 断联.md":   "排障/帕鲁-断联.md",
	} {
		got, ok := NormalizeDocPath(in)
		if !ok || got != want {
			t.Fatalf("NormalizeDocPath(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	// 越界路径直接拒绝，不改写后保存。后缀校验保留。
	for _, bad := range []string{
		"", "guides/a.txt", "..",
		"../secret.md", "guides/../secret.md", "guides/..",
		"/etc/passwd.md", "/guides/a.md",
		`C:\secret.md`, "C:/secret.md",
		`guides\a.md`, `..\secret.md`,
		"guides//a.md", "./guides/a.md", "guides/./a.md",
		"guides/\x00a.md",
	} {
		if got, ok := NormalizeDocPath(bad); ok {
			t.Fatalf("NormalizeDocPath(%q) 应拒绝，实际放行为 %q", bad, got)
		}
	}
}

func TestUsageRuleOnlyPromisesReadOnlyAccess(t *testing.T) {
	rule := UsageRule()
	if !strings.Contains(rule.Content, "只读检索本项目知识库") || !strings.Contains(rule.Content, "不要尝试写入") {
		t.Fatalf("使用规范应说明只读检索本项目知识库: %q", rule.Content)
	}
	for _, stale := range []string{"knowledge.md", "沉淀审核", "写回知识库"} {
		if strings.Contains(rule.Content, stale) {
			t.Fatalf("使用规范不应再承诺写入路径 %q: %q", stale, rule.Content)
		}
	}
}
