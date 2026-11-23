package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

func consoleTestSettings() domain.KnowledgeSettings {
	return domain.KnowledgeSettings{
		Provider: domain.KnowledgeProviderWeKnora,
		Enabled:  true,
		URL:      "http://weknora-app:8080",
		Connections: []domain.KnowledgeConnection{{
			ID:       "wk-1",
			Provider: domain.KnowledgeProviderWeKnora,
			URL:      "http://weknora-app:8080/",
			Account:  "admin@loopforge.local",
		}, {
			ID:       "wk-ext",
			Provider: domain.KnowledgeProviderWeKnora,
			URL:      "https://kb.example.com",
			Account:  "ops@example.com",
		}},
	}
}

func TestManagedWeKnoraConsoleReturnsEnvironmentPassword(t *testing.T) {
	t.Setenv("LOOPFORGE_WEKNORA_URL", "http://weknora-app:8080")
	t.Setenv("LOOPFORGE_WEKNORA_ADMIN_PASSWORD", "env-pass")
	t.Setenv("LOOPFORGE_WEKNORA_UI_URL", "http://localhost:18081")

	store := newFakeStore()
	store.settings = consoleTestSettings()
	svc := newTestService(store, &fakeUpstream{})

	login := svc.ManagedWeKnoraConsole("wk-1")
	if !login.Configured || login.Email != "admin@loopforge.local" || login.Password != "env-pass" ||
		login.Source != "environment" || login.UIURL != "http://localhost:18081" {
		t.Fatalf("unexpected console login: %+v", login)
	}

	// 外部连接的地址与内置实例不同，平台不掌握它的控制台密码。
	login = svc.ManagedWeKnoraConsole("wk-ext")
	if login.Configured || login.Password != "" {
		t.Fatalf("external connection must not expose a password: %+v", login)
	}
}

func TestManagedWeKnoraConsoleFallsBackToGeneratedPassword(t *testing.T) {
	t.Setenv("LOOPFORGE_WEKNORA_URL", "http://weknora-app:8080")
	t.Setenv("LOOPFORGE_WEKNORA_ADMIN_PASSWORD", "")

	store := newFakeStore()
	store.managedPass = "generated-pass"
	store.settings = consoleTestSettings()
	svc := newTestService(store, &fakeUpstream{})

	login := svc.ManagedWeKnoraConsole("wk-1")
	if !login.Configured || login.Source != "generated" || login.Password != "generated-pass" {
		t.Fatalf("unexpected console login: %+v", login)
	}
	if login.Email != "admin@loopforge.local" {
		t.Fatalf("expected default email, got %q", login.Email)
	}
}

func TestManagedWeKnoraConsoleScopesConnection(t *testing.T) {
	t.Setenv("LOOPFORGE_WEKNORA_URL", "http://weknora-app:8080")
	t.Setenv("LOOPFORGE_WEKNORA_ADMIN_PASSWORD", "env-pass")

	store := newFakeStore()
	store.settings = consoleTestSettings()
	svc := newTestService(store, &fakeUpstream{})

	// 空 id 与 "default" 指向顶层设置，顶层就是内置实例。
	for _, id := range []string{"", "default", "  "} {
		login := svc.ManagedWeKnoraConsole(id)
		if !login.Configured || login.Password != "env-pass" {
			t.Fatalf("id %q should resolve the managed instance: %+v", id, login)
		}
	}
	// 未知连接不得回退到顶层设置的密码。
	if login := svc.ManagedWeKnoraConsole("wk-missing"); login.Configured || login.Password != "" {
		t.Fatalf("unknown connection must not borrow the managed password: %+v", login)
	}
}

func TestManagedWeKnoraConsoleRequiresManagedInstance(t *testing.T) {
	t.Setenv("LOOPFORGE_WEKNORA_ADMIN_PASSWORD", "env-pass")

	cases := []struct {
		name   string
		envURL string
		mutate func(*domain.KnowledgeSettings)
	}{
		{name: "top-level external", envURL: "http://weknora-app:8080", mutate: func(s *domain.KnowledgeSettings) {
			s.URL = "https://kb.example.com"
		}},
		{name: "provider not weknora", envURL: "http://weknora-app:8080", mutate: func(s *domain.KnowledgeSettings) {
			s.Provider = domain.KnowledgeProviderLightCloud
		}},
		{name: "no managed url configured", envURL: "", mutate: func(*domain.KnowledgeSettings) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LOOPFORGE_WEKNORA_URL", tc.envURL)
			store := newFakeStore()
			store.managedPass = "generated-pass"
			store.settings = consoleTestSettings()
			tc.mutate(&store.settings)
			svc := newTestService(store, &fakeUpstream{})
			if login := svc.ManagedWeKnoraConsole(""); login.Configured || login.Password != "" {
				t.Fatalf("must not expose a password: %+v", login)
			}
		})
	}
}

func TestManagedWeKnoraConsoleURLComparison(t *testing.T) {
	t.Setenv("LOOPFORGE_WEKNORA_URL", "HTTP://WeKnora-App:8080")
	t.Setenv("LOOPFORGE_WEKNORA_ADMIN_PASSWORD", "env-pass")
	t.Setenv("LOOPFORGE_WEKNORA_ADMIN_EMAIL", "kb-admin@example.com")

	store := newFakeStore()
	store.settings = domain.KnowledgeSettings{
		Provider: domain.KnowledgeProviderWeKnora,
		URL:      "http://weknora-app:8080///",
		Connections: []domain.KnowledgeConnection{{
			ID:      "wk-1",
			URL:     " http://WEKNORA-APP:8080// ",
			Account: "kb-admin@example.com",
		}},
	}
	svc := newTestService(store, &fakeUpstream{})

	login := svc.ManagedWeKnoraConsole("wk-1")
	if !login.Configured || login.Password != "env-pass" || login.Email != "kb-admin@example.com" {
		t.Fatalf("case and trailing slashes should not change the match: %+v", login)
	}

	// 路径不同即视为不同实例。
	store.settings.Connections[0].URL = "http://weknora-app:8080/tenant"
	if login := svc.ManagedWeKnoraConsole("wk-1"); login.Configured {
		t.Fatalf("different path must not match: %+v", login)
	}
}

func TestManagedWeKnoraConsoleRefusesOtherAccount(t *testing.T) {
	t.Setenv("LOOPFORGE_WEKNORA_URL", "http://weknora-app:8080")
	t.Setenv("LOOPFORGE_WEKNORA_ADMIN_PASSWORD", "env-pass")
	t.Setenv("LOOPFORGE_WEKNORA_ADMIN_EMAIL", "")

	store := newFakeStore()
	store.settings = consoleTestSettings()
	// 同一个实例上的第二个账号：地址相同，但密码不是平台初始化时设置的那个。
	store.settings.Connections[1].URL = "http://weknora-app:8080"
	svc := newTestService(store, &fakeUpstream{})

	login := svc.ManagedWeKnoraConsole("wk-ext")
	if login.Configured || login.Password != "" {
		t.Fatalf("another account's connection must not borrow the admin password: %+v", login)
	}
	if login.Reason != "account_not_managed" || login.Email != "ops@example.com" {
		t.Fatalf("expected an account mismatch with the account shown: %+v", login)
	}
}

func TestManagedWeKnoraConsolePrefersSavedConnectionSecret(t *testing.T) {
	t.Setenv("LOOPFORGE_KNOWLEDGE_SECRET_KEY", "unit-test-key")
	t.Setenv("LOOPFORGE_WEKNORA_URL", "http://weknora-app:8080")
	t.Setenv("LOOPFORGE_WEKNORA_ADMIN_PASSWORD", "env-pass")

	store := newFakeStore()
	store.settings = consoleTestSettings()
	store.settings.Connections[1].URL = "http://weknora-app:8080"
	svc := newTestService(store, &fakeUpstream{})

	if err := svc.saveConnectionConsoleSecret("wk-ext", "ops@example.com", "own-pass"); err != nil {
		t.Fatalf("saveConnectionConsoleSecret: %v", err)
	}

	// 同一实例上的第二个账号：管理员补录的密码优先于内置管理员密码。
	login := svc.ManagedWeKnoraConsole("wk-ext")
	if !login.Configured || login.Email != "ops@example.com" || login.Password != "own-pass" || login.Source != "connection" {
		t.Fatalf("saved secret should win for its own connection: %+v", login)
	}

	// 每份密码只属于它自己的连接，内置实例仍走环境变量。
	login = svc.ManagedWeKnoraConsole("wk-1")
	if login.Password != "env-pass" || login.Source != "environment" {
		t.Fatalf("saved secret must stay scoped to its connection: %+v", login)
	}
	if login := svc.ManagedWeKnoraConsole("wk-missing"); login.Configured {
		t.Fatalf("unknown connection must stay unconfigured: %+v", login)
	}
}

func TestManagedWeKnoraConsoleIgnoresUnreadableSecret(t *testing.T) {
	t.Setenv("LOOPFORGE_KNOWLEDGE_SECRET_KEY", "unit-test-key")
	t.Setenv("LOOPFORGE_WEKNORA_URL", "http://weknora-app:8080")
	t.Setenv("LOOPFORGE_WEKNORA_ADMIN_PASSWORD", "env-pass")

	store := newFakeStore()
	store.settings = consoleTestSettings()
	store.settings.Connections[1].URL = "http://weknora-app:8080"
	store.consoleSecrets = map[string]string{
		"wk-ext": consoleSecretPrefix + "AAAA",
		"wk-1":   "garbage",
	}
	svc := newTestService(store, &fakeUpstream{})

	// 外部连接的密文损坏：不回退、不猜测，只说明平台没有保存。
	login := svc.ManagedWeKnoraConsole("wk-ext")
	if login.Configured || login.Password != "" || login.Reason != "account_not_managed" {
		t.Fatalf("corrupt secret must not produce a password: %+v", login)
	}

	// 内置实例的密文损坏：安全回退到部署密码。
	login = svc.ManagedWeKnoraConsole("wk-1")
	if !login.Configured || login.Password != "env-pass" || login.Source != "environment" {
		t.Fatalf("managed instance should fall back to the env password: %+v", login)
	}
}

func TestSaveWeKnoraConsolePasswordValidatesLogin(t *testing.T) {
	t.Setenv("LOOPFORGE_KNOWLEDGE_SECRET_KEY", "unit-test-key")

	var fail atomic.Bool
	var seenEmail, seenPassword string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/login" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		seenEmail, seenPassword = body.Email, body.Password
		w.Header().Set("Content-Type", "application/json")
		if fail.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "message": "invalid credentials"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"token":   "jwt-token",
			"user":    map[string]any{"tenant_id": 7},
		})
	}))
	defer srv.Close()

	store := newFakeStore()
	store.settings = domain.KnowledgeSettings{
		Provider: domain.KnowledgeProviderWeKnora,
		URL:      srv.URL,
		Connections: []domain.KnowledgeConnection{{
			ID:       "wk-x",
			Provider: domain.KnowledgeProviderWeKnora,
			URL:      srv.URL,
			Account:  "ops@example.com",
		}},
	}
	svc := newTestService(store, &fakeUpstream{})
	ctx := context.Background()

	if err := svc.SaveWeKnoraConsolePassword(ctx, "wk-x", "good-pass"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if seenEmail != "ops@example.com" || seenPassword != "good-pass" {
		t.Fatalf("save must verify the connection's own account, got %q / %q", seenEmail, seenPassword)
	}
	login := svc.ManagedWeKnoraConsole("wk-x")
	if !login.Configured || login.Password != "good-pass" || login.Source != "connection" {
		t.Fatalf("saved password should be returned: %+v", login)
	}

	// 校验失败不得覆盖已经保存的密码。
	fail.Store(true)
	if err := svc.SaveWeKnoraConsolePassword(ctx, "wk-x", "bad-pass"); err == nil {
		t.Fatal("a rejected login must not be stored")
	}
	if login := svc.ManagedWeKnoraConsole("wk-x"); login.Password != "good-pass" {
		t.Fatalf("failed validation must keep the previous secret: %+v", login)
	}

	if err := svc.SaveWeKnoraConsolePassword(ctx, "wk-x", "   "); err == nil {
		t.Fatal("an empty password must be refused")
	}
	if err := svc.SaveWeKnoraConsolePassword(ctx, "wk-missing", "some-pass"); err == nil {
		t.Fatal("an unknown connection must be refused")
	}
}

func TestLoginWeKnoraRedactsEchoedPassword(t *testing.T) {
	const password = "Sup3rSecret!42"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"message": "invalid credentials for " + password,
		})
	}))
	defer srv.Close()

	_, err := loginWeKnora(context.Background(), http.DefaultClient, srv.URL, "u@example.com", password)
	if err == nil {
		t.Fatal("expected the login to fail")
	}
	if strings.Contains(err.Error(), password) {
		t.Fatalf("upstream error must not echo the password: %v", err)
	}
}

func TestSaveWeKnoraConsolePasswordRequiresAccountOutsideManaged(t *testing.T) {
	t.Setenv("LOOPFORGE_KNOWLEDGE_SECRET_KEY", "unit-test-key")
	t.Setenv("LOOPFORGE_WEKNORA_URL", "http://weknora-app:8080")

	store := newFakeStore()
	store.settings = domain.KnowledgeSettings{
		Provider: domain.KnowledgeProviderWeKnora,
		URL:      "https://kb.example.com",
		Connections: []domain.KnowledgeConnection{{
			ID:       "wk-ext",
			Provider: domain.KnowledgeProviderWeKnora,
			URL:      "https://kb.example.com",
		}},
	}
	svc := newTestService(store, &fakeUpstream{})

	err := svc.SaveWeKnoraConsolePassword(context.Background(), "wk-ext", "pw")
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Kind != apperr.Invalid {
		t.Fatalf("a non-managed connection without an account must be invalid, got %v", err)
	}
	if got := store.KnowledgeConnectionConsoleSecret("wk-ext"); got != "" {
		t.Fatalf("nothing may be stored without an account, got %q", got)
	}
}

func TestProvisionWeKnoraSavesConsolePasswordOnlyWhenRequested(t *testing.T) {
	t.Setenv("LOOPFORGE_KNOWLEDGE_SECRET_KEY", "unit-test-key")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"token":   "jwt-tok",
				"user":    map[string]any{"tenant_id": 7},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/tenants/7/api-keys":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    map[string]any{"token": "sk-issued"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge-bases":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cases := []struct {
		name string
		save bool
	}{
		{name: "saves when requested", save: true},
		{name: "drops by default", save: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			svc := NewService(store, func() string { return "1" })
			res, err := svc.ProvisionWeKnora(context.Background(), WeKnoraProvisionInput{
				Mode:                "external",
				URL:                 srv.URL,
				Name:                "ext",
				Email:               "u@example.com",
				Password:            "secret-pass",
				SaveConsolePassword: tc.save,
			}, "admin")
			if err != nil {
				t.Fatalf("ProvisionWeKnora: %v", err)
			}
			conns := res.Settings.Connections
			if len(conns) == 0 {
				t.Fatal("expected a provisioned connection")
			}
			connID := conns[len(conns)-1].ID
			login := svc.ManagedWeKnoraConsole(connID)
			if !tc.save {
				if login.Configured || login.Password != "" {
					t.Fatalf("password must not be kept unless requested: %+v", login)
				}
				return
			}
			if !login.Configured || login.Password != "secret-pass" || login.Source != "connection" {
				t.Fatalf("saved password should be returned for %q: %+v", connID, login)
			}
		})
	}
}
