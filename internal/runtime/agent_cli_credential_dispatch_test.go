package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApplyAgentCLICredentialForwardsOptions goes through the exported entry
// point so the per-CLI helpers get the caller's options: the OpenCode system
// prompt, the Kimi approval hook and the Pi protocol.
func TestApplyAgentCLICredentialForwardsOptions(t *testing.T) {
	home := t.TempDir()

	_, env, err := ApplyAgentCLICredential(AgentCLIOpenCode, ProviderCredential{APIKey: "k", BaseURL: "https://gw.example", Protocol: AgentCLIProtocolChat}, AgentCLIOptions{Model: "m", SystemPromptFile: " prompts/system.md "}, HostAgentCLIHome(home), home, nil)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Instructions []string `json:"instructions"`
	}
	if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Instructions) != 1 || config.Instructions[0] != "prompts/system.md" {
		t.Fatalf("opencode instructions = %q, want the system prompt file", config.Instructions)
	}

	if _, _, err := ApplyAgentCLICredential(AgentCLIKimi, ProviderCredential{APIKey: "k"}, AgentCLIOptions{Model: "m", ApprovalCommand: "/bin/approve --run r1"}, HostAgentCLIHome(home), "/w/home", nil); err != nil {
		t.Fatal(err)
	}
	toml, _ := os.ReadFile(filepath.Join(home, ".kimi-code", "config.toml"))
	if !strings.Contains(string(toml), "[[hooks]]") || !strings.Contains(string(toml), `command = "/bin/approve --run r1"`) {
		t.Fatalf("kimi config lacks the approval hook:\n%s", toml)
	}

	if _, _, err := ApplyAgentCLICredential(AgentCLIPi, ProviderCredential{APIKey: "k", BaseURL: "https://api.example/", Protocol: AgentCLIProtocolMessages}, AgentCLIOptions{Model: " m "}, HostAgentCLIHome(home), "/w/home", nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".pi", "agent", "models.json"))
	var models struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			API     string `json:"api"`
			Models  []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(data, &models); err != nil {
		t.Fatal(err)
	}
	provider := models.Providers["agentflow"]
	if provider.API != "anthropic-messages" || provider.BaseURL != "https://api.example" || len(provider.Models) != 1 || provider.Models[0].ID != "m" {
		t.Fatalf("pi models.json: %s", data)
	}
}

// TestApplyAgentCLICredentialConfigWriteFailure checks that the CLIs which
// write a config file fail closed when the home cannot be written (here a
// symlinked home), instead of launching without their configuration.
func TestApplyAgentCLICredentialConfigWriteFailure(t *testing.T) {
	for _, tc := range []struct {
		kind string
		cred ProviderCredential
	}{
		{AgentCLIAntigravity, ProviderCredential{APIKey: "k"}},
		{AgentCLIAntigravity, ProviderCredential{APIKey: "k", BaseURL: "https://gw.example", Protocol: AgentCLIProtocolChat}},
		{AgentCLIKimi, ProviderCredential{APIKey: "k"}},
		{AgentCLIPi, ProviderCredential{APIKey: "k", BaseURL: "https://api.example"}},
	} {
		outside := t.TempDir()
		home := filepath.Join(t.TempDir(), "home")
		if err := os.Symlink(outside, home); err != nil {
			t.Fatal(err)
		}
		argv, env, err := ApplyAgentCLICredential(tc.kind, tc.cred, AgentCLIOptions{Model: "m", ReadOnly: true}, HostAgentCLIHome(home), home, []string{"cli", "-p", "hi"})
		if err == nil || argv != nil || env != nil {
			t.Fatalf("%s %s: argv=%q env=%v err=%v; want an error and nothing to run", tc.kind, tc.cred.Protocol, argv, env, err)
		}
		if entries, _ := os.ReadDir(outside); len(entries) != 0 {
			t.Fatalf("%s: files written outside home: %v", tc.kind, entries)
		}
	}
}
