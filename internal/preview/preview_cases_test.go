package preview

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigAndPublicURL(t *testing.T) {
	t.Setenv("AGENTFLOW_PREVIEW_PUBLIC_URL", "http://preview.example/path?q=1")
	t.Setenv("AGENTFLOW_PREVIEW_MAX_FILES", "50001")
	t.Setenv("AGENTFLOW_PREVIEW_MAX_COMPRESSED", "bad")
	t.Setenv("AGENTFLOW_PREVIEW_MAX_EXPANDED", "1")
	t.Setenv("AGENTFLOW_PREVIEW_TTL", "15m")
	t.Setenv("AGENTFLOW_PREVIEW_FINALIZE_TIMEOUT", "12")
	t.Setenv("AGENTFLOW_PREVIEW_SESSION_TTL", "-1")
	cfg := LoadConfig()
	if cfg.PublicScheme != "http" || cfg.PublicHost != "preview.example" {
		t.Fatalf("public origin = %s://%s", cfg.PublicScheme, cfg.PublicHost)
	}
	if cfg.MaxFiles != DefaultMaxFiles || cfg.MaxCompressed != DefaultMaxCompressed {
		t.Fatalf("limits = %#v", cfg)
	}
	if cfg.DefaultRetention != 15*time.Minute || cfg.FinalizationTimeout != 12*time.Second || cfg.SessionTTL != DefaultSessionTTL {
		t.Fatalf("durations = %#v", cfg)
	}
	for _, raw := range []string{"", "ftp://host", "https://u:p@host", "://bad"} {
		if _, _, ok := SplitPublicURL(raw); ok {
			t.Fatalf("accepted URL %q", raw)
		}
	}
	if scheme, host, ok := SplitPublicURL("example.com"); !ok || scheme != "https" || host != "example.com" {
		t.Fatalf("implicit URL = %q %q %v", scheme, host, ok)
	}
}

func TestGatewayRewriteBranches(t *testing.T) {
	prefix := "/preview/token"
	html := `<html><head data-x="1"><meta name=referrer content=no-referrer></head><body>` +
		`<img src="/assets/a.png"><a href="/route">x</a><style>.a{background:url( '/img/a.png')}</style></body></html>`
	out := string(rewritePreviewHTML([]byte(html), prefix, "https://api.example/path", false))
	for _, want := range []string{`<base href="/preview/token/">`, `content="same-origin"`, `data-af-preview-backend`, `data-af-preview-history`, `/preview/token/assets/a.png`, `/preview/token/img/a.png`} {
		if !strings.Contains(out, want) {
			t.Fatalf("rewrite missing %q: %s", want, out)
		}
	}
	if got := injectBaseTag(`<base href="/">x`, prefix); got != `<base href="/">x` {
		t.Fatalf("existing base changed: %s", got)
	}
	if got := injectBaseTag("plain", ""); !strings.HasPrefix(got, `<base href="/">`) {
		t.Fatalf("base fallback = %s", got)
	}
	large := make([]byte, maxRewrittenHTML+1)
	if got := rewritePreviewHTML(large, prefix, "", false); len(got) != len(large) {
		t.Fatal("large HTML changed")
	}
	if got := rewriteCSSURLs(`url(//cdn/a) url("/already/a") url(/x)`, prefix); !strings.Contains(got, `url(/preview/token/x)`) || strings.Contains(got, `/preview/token//cdn`) {
		t.Fatalf("css = %s", got)
	}
	if got := rewriteJSRootURLs(`"/assets/a" '/api/x'`, prefix); !strings.Contains(got, `"/preview/token/assets/a"`) || strings.Contains(got, `/preview/token/api`) {
		t.Fatalf("js = %s", got)
	}
}

func TestGatewayNavigationHeadersAndCSP(t *testing.T) {
	s := &Service{cfg: Config{PublicHost: "public.example", FrameAncestors: "https://parent.example"}}
	token := strings.Repeat("a", tokenHexLen)
	tests := []struct {
		name string
		req  *http.Request
		ok   bool
	}{
		{"fetch metadata", httptest.NewRequest(http.MethodGet, "https://public.example/login?q=1", nil), true},
		{"bad mode", httptest.NewRequest(http.MethodGet, "https://public.example/login", nil), false},
		{"post fallback", httptest.NewRequest(http.MethodPost, "http://public.example/login", nil), false},
	}
	tests[0].req.Header.Set("Referer", "https://public.example/preview/"+token+"/index.html")
	tests[0].req.Header.Set("Sec-Fetch-Mode", "navigate")
	tests[0].req.Header.Set("Sec-Fetch-Site", "same-origin")
	tests[1].req.Header.Set("Referer", "https://public.example/preview/"+token+"/")
	tests[1].req.Header.Set("Sec-Fetch-Mode", "cors")
	tests[2].req.Header.Set("Referer", "http://public.example/preview/"+token+"/")
	for _, tc := range tests {
		target, ok := s.StrayNavigationTarget(tc.req)
		if ok != tc.ok {
			t.Fatalf("%s: target=%q ok=%v", tc.name, target, ok)
		}
	}
	rr := httptest.NewRecorder()
	s.writePreviewHeaders(rr, "app.abc.js", "https://api.example/a")
	if !strings.Contains(rr.Header().Get("Content-Security-Policy"), "https://api.example") || !strings.Contains(rr.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("headers = %#v", rr.Header())
	}
	if got := backendOrigin("file:///tmp/x"); got != "" {
		t.Fatalf("backend origin = %q", got)
	}
}
