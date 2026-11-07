package knowledge

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestProxyTokenRoundTripAndRevocation(t *testing.T) {
	settings := domain.KnowledgeSettings{Enabled: true, URL: "http://kb/mcp", Token: "upstream-a"}
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
	rotated.Token = "upstream-b"
	if _, err := VerifyProxyToken(rotated, token, now); err == nil {
		t.Fatal("rotating the upstream token must revoke outstanding proxy tokens")
	}
	tampered := strings.Replace(token, token[len(proxyTokenPrefix):len(proxyTokenPrefix)+4], "AAAA", 1)
	if _, err := VerifyProxyToken(settings, tampered, now); err == nil {
		t.Fatal("tampered token accepted")
	}
	if _, err := SignProxyToken(domain.KnowledgeSettings{}, ProxyClaims{}, now); err == nil {
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
		{"unrestricted keeps request", nil, []string{"a"}, []string{"a"}, false},
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
		"guides/a.md":         "guides/a.md",
		"/guides/../b.md":     "b.md",
		"../../etc/passwd.md": "etc/passwd.md",
		"排障/帕鲁 断联.md":         "排障/帕鲁-断联.md",
	} {
		got, ok := NormalizeDocPath(in)
		if !ok || got != want {
			t.Fatalf("NormalizeDocPath(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "guides/a.txt", ".."} {
		if _, ok := NormalizeDocPath(bad); ok {
			t.Fatalf("NormalizeDocPath(%q) accepted", bad)
		}
	}
}
