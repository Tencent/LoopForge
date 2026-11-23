package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

// ssoTestInstance 起一个只实现登录接口的 WeKnora，返回 jwt-token。
func ssoTestInstance(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/login" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"token":         "jwt-token",
				"refresh_token": "refresh-token",
				"user":          map[string]any{"id": 1, "email": body.Email, "tenant_id": 10000},
				"tenant":        map[string]any{"id": 10000, "name": "tenant"},
				"active_tenant": map[string]any{"id": 10000, "name": "tenant"},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func ssoTestSettings(url string) domain.KnowledgeSettings {
	return domain.KnowledgeSettings{
		Provider: domain.KnowledgeProviderWeKnora,
		Enabled:  true,
		URL:      url,
		Connections: []domain.KnowledgeConnection{{
			ID:               "wk-1",
			Provider:         domain.KnowledgeProviderWeKnora,
			URL:              url,
			Account:          "admin@loopforge.local",
		}},
	}
}

func TestIssueConsoleSSORevealsOnlyOncePerOrigin(t *testing.T) {
	srv, calls := ssoTestInstance(t)
	t.Setenv("LOOPFORGE_WEKNORA_URL", srv.URL)
	t.Setenv("LOOPFORGE_WEKNORA_ADMIN_PASSWORD", "env-pass")

	store := newFakeStore()
	store.settings = ssoTestSettings(srv.URL)
	svc := newTestService(store, &fakeUpstream{})

	res, err := svc.IssueWeKnoraConsoleSSO(context.Background(), "wk-1", "http://21.6.124.90:8091")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if !res.Supported || res.Ticket == "" || res.Email != "admin@loopforge.local" || res.ExpiresIn <= 0 {
		t.Fatalf("unexpected issue result: %+v", res)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected one upstream login, got %d", calls.Load())
	}

	payload, err := RedeemWeKnoraConsoleSSO(res.Ticket, "http://21.6.124.90:8091")
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	var session struct {
		Token        string `json:"token"`
		RefreshToken string `json:"refresh_token"`
		User         struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.Unmarshal(payload, &session); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if session.Token != "jwt-token" || session.RefreshToken != "refresh-token" || session.User.Email != "admin@loopforge.local" {
		t.Fatalf("unexpected payload: %s", payload)
	}

	// 一次性：同一张票据不能再兑一次。
	if _, err := RedeemWeKnoraConsoleSSO(res.Ticket, "http://21.6.124.90:8091"); err == nil {
		t.Fatal("ticket must not be reusable")
	} else {
		wantKind(t, err, apperr.Forbidden)
	}
}

func TestRedeemConsoleSSOBindsOriginAndBurnsTicket(t *testing.T) {
	srv, _ := ssoTestInstance(t)
	t.Setenv("LOOPFORGE_WEKNORA_URL", srv.URL)
	t.Setenv("LOOPFORGE_WEKNORA_ADMIN_PASSWORD", "env-pass")

	store := newFakeStore()
	store.settings = ssoTestSettings(srv.URL)
	svc := newTestService(store, &fakeUpstream{})

	res, err := svc.IssueWeKnoraConsoleSSO(context.Background(), "wk-1", "https://platform.example.com")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	// 别的站点拿到票据也不能用。
	if _, err := RedeemWeKnoraConsoleSSO(res.Ticket, "https://evil.example.com"); err == nil {
		t.Fatal("ticket must be bound to the issuing origin")
	} else {
		wantKind(t, err, apperr.Forbidden)
	}
	// 失败尝试同样作废票据，避免被反复猜测来源。
	if _, err := RedeemWeKnoraConsoleSSO(res.Ticket, "https://platform.example.com"); err == nil {
		t.Fatal("a rejected redeem must burn the ticket")
	}
}

func TestIssueConsoleSSOUnsupportedFallsBack(t *testing.T) {
	srv, calls := ssoTestInstance(t)
	t.Setenv("LOOPFORGE_WEKNORA_URL", srv.URL)
	t.Setenv("LOOPFORGE_WEKNORA_ADMIN_PASSWORD", "env-pass")

	store := newFakeStore()
	store.settings = ssoTestSettings(srv.URL)
	store.settings.Connections = append(store.settings.Connections, domain.KnowledgeConnection{
		ID:      "wk-ext",
		URL:     "https://kb.example.com",
		Account: "ops@example.com",
	})
	svc := newTestService(store, &fakeUpstream{})

	// 外部实例没有同源桥接页。
	res, err := svc.IssueWeKnoraConsoleSSO(context.Background(), "wk-ext", "http://localhost:8091")
	if err != nil || res.Supported || res.Reason != "external_instance" {
		t.Fatalf("external instance must fall back: %+v %v", res, err)
	}

	// 未保存密码的账号同样只回退，不尝试登录。
	store.settings.Connections[0].Account = "other@loopforge.local"
	res, err = svc.IssueWeKnoraConsoleSSO(context.Background(), "wk-1", "http://localhost:8091")
	if err != nil || res.Supported || res.Reason != "account_not_managed" {
		t.Fatalf("unmanaged account must fall back: %+v %v", res, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("fallback must not hit the upstream, got %d calls", calls.Load())
	}

	// 非 WeKnora 引擎直接说明原因。
	store.settings.Provider = domain.KnowledgeProviderLightCloud
	res, err = svc.IssueWeKnoraConsoleSSO(context.Background(), "wk-1", "http://localhost:8091")
	if err != nil || res.Supported || res.Reason != "provider_not_weknora" {
		t.Fatalf("other provider must fall back: %+v %v", res, err)
	}
}

func TestIssueConsoleSSORejectsBadOrigin(t *testing.T) {
	store := newFakeStore()
	store.settings = ssoTestSettings("http://weknora-app:8080")
	svc := newTestService(store, &fakeUpstream{})

	for _, origin := range []string{"", "javascript:alert(1)", "http://host/path?q=1", "http://host#frag"} {
		if _, err := svc.IssueWeKnoraConsoleSSO(context.Background(), "wk-1", origin); err == nil {
			t.Fatalf("origin %q must be rejected", origin)
		} else {
			wantKind(t, err, apperr.Invalid)
		}
	}
	// 大小写与默认端口不影响绑定。
	if _, err := normalizeConsoleOrigin("HTTP://Host.Example.COM:8091/"); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got, _ := normalizeConsoleOrigin("HTTP://Host.Example.COM:8091/"); !strings.EqualFold(got, "http://host.example.com:8091") {
		t.Fatalf("unexpected normalized origin %q", got)
	}
}
