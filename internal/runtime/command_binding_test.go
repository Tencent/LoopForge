package runtime

import "testing"

func TestBindClaudeCodeEnvironmentUsesBearerForCustomGateways(t *testing.T) {
	env := map[string]string{}
	if err := BindClaudeCodeEnvironment(env, "secret", "https://gateway.example/anthropic"); err != nil {
		t.Fatal(err)
	}
	if env["ANTHROPIC_API_KEY"] != "secret" || env["ANTHROPIC_AUTH_TOKEN"] != "secret" || env["ANTHROPIC_BASE_URL"] != "https://gateway.example/anthropic" {
		t.Fatalf("custom gateway env = %#v", env)
	}
}

func TestBindClaudeCodeEnvironmentKeepsOfficialEndpointOnAPIKey(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		wantBase string
	}{
		{name: "default endpoint", wantBase: "https://api.anthropic.com"},
		{name: "official endpoint", baseURL: "https://api.anthropic.com", wantBase: "https://api.anthropic.com"},
		{name: "official endpoint with path", baseURL: "https://API.ANTHROPIC.COM/v1", wantBase: "https://API.ANTHROPIC.COM/v1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := map[string]string{"ANTHROPIC_AUTH_TOKEN": "host-token"}
			if err := BindClaudeCodeEnvironment(env, "secret", test.baseURL); err != nil {
				t.Fatal(err)
			}
			if env["ANTHROPIC_API_KEY"] != "secret" || env["ANTHROPIC_AUTH_TOKEN"] != "" || env["ANTHROPIC_BASE_URL"] != test.wantBase {
				t.Fatalf("official endpoint env = %#v", env)
			}
		})
	}
}

func TestBindClaudeCodeEnvironmentRequiresAPIKey(t *testing.T) {
	if err := BindClaudeCodeEnvironment(map[string]string{}, " ", ""); err == nil {
		t.Fatal("empty API key must fail")
	}
}
