package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestApplyOpenCodeCredential(t *testing.T) {
	argv := []string{"opencode", "run", "x"}
	got, env, err := applyOpenCodeCredential("k", "https://gw.example", AgentCLIProtocolMessages, "m", " /s.md ", argv)
	if err != nil || !reflect.DeepEqual(got, argv) {
		t.Fatalf("argv=%q err=%v", got, err)
	}
	if env[agentCLIKeyEnv] != "k" || env["OPENCODE_DISABLE_AUTOUPDATE"] != "1" || len(env) != 3 {
		t.Fatalf("env: %v", env)
	}
	var config struct {
		Provider map[string]struct {
			NPM     string `json:"npm"`
			Options struct {
				BaseURL string `json:"baseURL"`
				APIKey  string `json:"apiKey"`
			} `json:"options"`
			Models map[string]map[string]string `json:"models"`
		} `json:"provider"`
		Model        string   `json:"model"`
		Instructions []string `json:"instructions"`
	}
	if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
		t.Fatal(err)
	}
	provider := config.Provider["agentflow"]
	if provider.NPM != "@ai-sdk/anthropic" || provider.Options.BaseURL != "https://gw.example/v1" || provider.Options.APIKey != "{env:"+agentCLIKeyEnv+"}" || provider.Models["m"]["name"] != "m" {
		t.Fatalf("provider: %+v", provider)
	}
	if config.Model != "agentflow/m" || !reflect.DeepEqual(config.Instructions, []string{"/s.md"}) {
		t.Fatalf("config: %+v", config)
	}

	// Only the messages protocol gets /v1, and never twice; a blank prompt
	// file adds no instructions.
	for _, tc := range []struct{ baseURL, protocol, npm, endpoint string }{
		{"https://gw.example/v1", AgentCLIProtocolMessages, "@ai-sdk/anthropic", "https://gw.example/v1"},
		{"https://gw.example", AgentCLIProtocolChat, "@ai-sdk/openai-compatible", "https://gw.example"},
		{"https://gw.example", AgentCLIProtocolResponses, "@ai-sdk/openai", "https://gw.example"},
	} {
		_, env, _ := applyOpenCodeCredential("k", tc.baseURL, tc.protocol, "m", " ", nil)
		content := env["OPENCODE_CONFIG_CONTENT"]
		if !strings.Contains(content, `"baseURL":"`+tc.endpoint+`"`) || !strings.Contains(content, tc.npm) || strings.Contains(content, "instructions") {
			t.Errorf("%s %s: %s", tc.baseURL, tc.protocol, content)
		}
	}
}

func TestApplyAntigravityCredential(t *testing.T) {
	home := t.TempDir()
	argv := []string{"agy", "-p", "hi"}
	got, env, err := applyAntigravityCredential(HostAgentCLIHome(home), "k", "https://gw.example", AgentCLIProtocolChat, "m", false, argv)
	if err != nil || !reflect.DeepEqual(got, argv) {
		t.Fatalf("argv=%q err=%v", got, err)
	}
	want := map[string]string{"AGY_LLM_GATEWAY_URL": "https://gw.example", "AGY_LLM_GATEWAY_API_KEY": "k", "AGY_LLM_GATEWAY_WIRE_PROTOCOL": "openai", "AGY_LLM_GATEWAY_MODELS": "m"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("gateway env: %v", env)
	}
	if data, _ := os.ReadFile(filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")); string(data) != "{}" {
		t.Fatalf("gateway settings: %s", data)
	}

	got, env, err = applyAntigravityCredential(HostAgentCLIHome(home), "k", "https://g.example", AgentCLIProtocolMessages, "m", true, argv)
	if err != nil {
		t.Fatal(err)
	}
	want = map[string]string{"GEMINI_API_KEY": "k", "GOOGLE_GEMINI_BASE_URL": "https://g.example", "GEMINI_BASE_URL": "https://g.example"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("gemini env: %v", env)
	}
	// The file keeps only the provider; read-only chat repeats it inline
	// with the deny rules, before -p.
	if data, _ := os.ReadFile(filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")); string(data) != `{"modelProvider":"gemini"}` {
		t.Fatalf("gemini settings: %s", data)
	}
	i := slices.Index(got, "--settings")
	if i != 1 || got[i+2] != "-p" || !strings.Contains(got[i+1], `"modelProvider":"gemini"`) || !strings.Contains(got[i+1], `"deny"`) {
		t.Fatalf("read-only argv: %q", got)
	}
}

func TestApplyKimiCredential(t *testing.T) {
	home := t.TempDir()
	_, env, err := applyKimiCredential(HostAgentCLIHome(home), "/w/home", "k", "", AgentCLIProtocolChat, "m", "/hook.sh", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{agentCLIKeyEnv: "k", "KIMI_CODE_HOME": "/w/home/.kimi-code", "KIMI_DISABLE_TELEMETRY": "1"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("env: %v", env)
	}
	toml, _ := os.ReadFile(filepath.Join(home, ".kimi-code", "config.toml"))
	for _, line := range []string{`type = "kimi"`, `api_key_env = "` + agentCLIKeyEnv + `"`, `model = "m"`, "[[hooks]]", `command = "/hook.sh"`, "timeout = 600"} {
		if !strings.Contains(string(toml), line) {
			t.Errorf("config misses %q:\n%s", line, toml)
		}
	}
	if strings.Contains(string(toml), "base_url") || strings.Contains(string(toml), `"k"`) {
		t.Fatalf("default provider config:\n%s", toml)
	}

	if _, _, err := applyKimiCredential(HostAgentCLIHome(home), "/w/home", "k", "https://api.example/v1", AgentCLIProtocolMessages, "m", "", nil); err != nil {
		t.Fatal(err)
	}
	toml, _ = os.ReadFile(filepath.Join(home, ".kimi-code", "config.toml"))
	if !strings.Contains(string(toml), `type = "anthropic"`) || !strings.Contains(string(toml), `base_url = "https://api.example/v1"`) || strings.Contains(string(toml), "[[hooks]]") {
		t.Fatalf("custom provider config:\n%s", toml)
	}
}

func TestApplyPiCredential(t *testing.T) {
	home := t.TempDir()
	argv := []string{"pi", "-p", "hi"}
	got, env, err := applyPiCredential(HostAgentCLIHome(home), "/w/home", "k", "https://api.example", AgentCLIProtocolResponses, "m", argv)
	if err != nil || !reflect.DeepEqual(got, argv) {
		t.Fatalf("argv=%q err=%v", got, err)
	}
	want := map[string]string{agentCLIKeyEnv: "k", "PI_CODING_AGENT_DIR": "/w/home/.pi/agent"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("env: %v", env)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".pi", "agent", "models.json"))
	var models struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			API     string `json:"api"`
			APIKey  string `json:"apiKey"`
			Models  []struct {
				ID        string `json:"id"`
				Reasoning bool   `json:"reasoning"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(data, &models); err != nil {
		t.Fatal(err)
	}
	provider := models.Providers["agentflow"]
	if provider.BaseURL != "https://api.example" || provider.API != "openai-responses" || provider.APIKey != "$"+agentCLIKeyEnv || len(provider.Models) != 1 || provider.Models[0].ID != "m" || !provider.Models[0].Reasoning {
		t.Fatalf("models.json: %s", data)
	}
	if strings.Contains(string(data), `"k"`) {
		t.Fatalf("models.json carries the key: %s", data)
	}
}
