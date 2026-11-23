package knowledge

import (
	"testing"

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
