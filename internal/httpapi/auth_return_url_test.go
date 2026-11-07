package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"agentflow/internal/auth"
)

func TestGetReturnURLRejectsForeignHosts(t *testing.T) {
	h := &authHandlers{deps: &deps{authClient: auth.NewClient(auth.Config{AppDomain: "loopforge.example.com"})}}

	cases := []struct {
		returnURL string
		want      string
	}{
		{"https://lf.test/#/dashboard", "https://lf.test/#/dashboard"},
		{"https://LF.test:8443/x?y=1", "https://LF.test:8443/x?y=1"},
		{"/#/settings", "http://lf.test/#/settings"},
		{"https://loopforge.example.com/a", "https://loopforge.example.com/a"},
		{"https://studio.loopforge.example.com/a", "https://studio.loopforge.example.com/a"},
		{"https://attacker.example/sso-landing", "http://lf.test/"},
		{"http://www.qq.com_521_qq_diao_yu_wangzhan_789.com", "http://lf.test/"},
		{"https://lf.test.attacker.example/", "http://lf.test/"},
		{"https://evilloopforge.example.com/", "http://lf.test/"},
		{"https://lf.test@attacker.example/", "http://lf.test/"},
		{"//attacker.example/", "http://lf.test/"},
		{"/\\attacker.example/", "http://lf.test/"},
		{"https:\\\\attacker.example", "http://lf.test/"},
		{"https:attacker.example", "http://lf.test/"},
		{"javascript:alert(1)", "http://lf.test/"},
		{"data:text/html,x", "http://lf.test/"},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, "http://lf.test/api/auth/current?return_url="+url.QueryEscape(tc.returnURL), nil)
		if got := h.getReturnURL(r); got != tc.want {
			t.Errorf("return_url=%q: got %q, want %q", tc.returnURL, got, tc.want)
		}
	}

	r := httptest.NewRequest(http.MethodGet, "http://lf.test/api/auth/current", nil)
	r.Header.Set("Referer", "https://attacker.example/")
	if got := h.getReturnURL(r); got != "http://lf.test/" {
		t.Errorf("foreign Referer must be ignored, got %q", got)
	}
}

func TestAuthCurrentLoginURLDoesNotEmbedForeignReturnURL(t *testing.T) {
	authClient := auth.NewClient(auth.Config{
		PaaSID:    "app",
		AppToken:  "token",
		LoginURL:  "https://passport.example.com/signin",
		AppDomain: "loopforge.example.com",
	})
	handler, _ := newTestAuthServer(t, authClient)

	req := httptest.NewRequest(http.MethodGet, "http://loopforge.example.com/api/auth/current?return_url=https://attacker.example/sso-landing", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "attacker.example") {
		t.Fatalf("loginUrl must not carry a foreign return URL: %s", body)
	}
}
