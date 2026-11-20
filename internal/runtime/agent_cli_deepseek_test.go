package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestAgentCLIPiAPI(t *testing.T) {
	for protocol, want := range map[string]string{
		AgentCLIProtocolChat:      "openai-completions",
		AgentCLIProtocolResponses: "openai-responses",
		AgentCLIProtocolMessages:  "anthropic-messages",
		"unknown":                 "",
	} {
		if got := agentCLIPiAPI(protocol); got != want {
			t.Fatalf("agentCLIPiAPI(%q) = %q, want %q", protocol, got, want)
		}
	}
}

func TestApplyDeepSeekCredentialCustomBaseURL(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".dsh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".dsh", "agentflow-mcp.json"), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	argv, env, err := ApplyAgentCLICredential(AgentCLIDeepSeek,
		ProviderCredential{APIKey: "k", BaseURL: "https://gw.example/", Protocol: AgentCLIProtocolMessages},
		AgentCLIOptions{Model: "m"}, HostAgentCLIHome(home), "/h",
		[]string{"dsh", "--profile", "headless", "--json", "x"})
	if err != nil {
		t.Fatal(err)
	}
	wantArgv := []string{"dsh", "--profile", "headless", "--patch", "/h/.dsh/agentflow-model.json", "--patch", "/h/.dsh/agentflow-mcp.json", "--json", "x"}
	if !slices.Equal(argv, wantArgv) {
		t.Fatalf("argv = %q, want %q", argv, wantArgv)
	}
	if env[agentCLIKeyEnv] != "k" || env["DSH_HOME"] != "/h/.dsh" || env["DEEPSEEK_API_KEY"] != "" {
		t.Fatalf("env = %v", env)
	}
	data, err := os.ReadFile(filepath.Join(home, ".dsh", "agentflow-model.json"))
	if err != nil {
		t.Fatal(err)
	}
	var patch []struct {
		ID     string         `json:"id"`
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(data, &patch); err != nil || len(patch) != 2 {
		t.Fatalf("patch: %s (%v)", data, err)
	}
	provider := patch[0].Config["providers"].(map[string]any)["agentflow"].(map[string]any)
	if patch[0].ID != "llm-pi-ai" || provider["api"] != "anthropic-messages" || provider["baseURL"] != "https://gw.example" {
		t.Fatalf("provider patch: %s", data)
	}
	if patch[1].ID != "agent-default-model" || patch[1].Config["provider"] != "agentflow" || patch[1].Config["model"] != "m" {
		t.Fatalf("default model patch: %s", data)
	}
}

func TestApplyDeepSeekCredentialRejectsShortArgv(t *testing.T) {
	_, _, err := ApplyAgentCLICredential(AgentCLIDeepSeek, ProviderCredential{APIKey: "k"}, AgentCLIOptions{Model: "m"}, HostAgentCLIHome(t.TempDir()), "/h", []string{"dsh"})
	if err == nil || err.Error() != "invalid DeepSeek Harness invocation" {
		t.Fatalf("err = %v", err)
	}
}

func TestApplyDeepSeekCredentialSetsTurnEffort(t *testing.T) {
	home := t.TempDir()
	for _, tc := range []struct {
		baseURL, effort string
		provider        string
		want            any
	}{
		{"", "none", "deepseek-official", "off"},
		{"", "max", "deepseek-official", "max"},
		{"", "", "deepseek-official", "high"},
		{"https://gw.example/v1", "max", "agentflow-reasoning", "max"},
		{"https://gw.example/v1", "", "agentflow", nil},
	} {
		command := AgentCLICommand(AgentCLIDeepSeek, AgentCLICommandOptions{Model: "deepseek-flash", Effort: tc.effort, Resume: "session", Prompt: "probe"})
		if _, _, err := ApplyAgentCLICredential(AgentCLIDeepSeek, ProviderCredential{APIKey: "k", BaseURL: tc.baseURL},
			command.AgentCLI, HostAgentCLIHome(home), home, command.Argv); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(home, ".dsh", "agentflow-model.json"))
		if err != nil {
			t.Fatal(err)
		}
		var patch []struct {
			ID     string         `json:"id"`
			Config map[string]any `json:"config"`
		}
		if err = json.Unmarshal(data, &patch); err != nil || len(patch) == 0 {
			t.Fatalf("invalid model patch: %s (%v)", data, err)
		}
		selection := patch[len(patch)-1]
		if selection.ID != "agent-default-model" || selection.Config["provider"] != tc.provider || selection.Config["reasoningEffort"] != tc.want {
			t.Errorf("base %q and effort %q: wrong model selection in %s", tc.baseURL, tc.effort, data)
		}
	}
}
