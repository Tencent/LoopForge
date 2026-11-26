package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDeepSeekOversizedPromptReachesCLIOnStdin(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "dsh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec cat > \"$0.stdin\"\n"), 0700); err != nil {
		t.Fatal(err)
	}

	const system = "Follow the project instructions."
	prompt := strings.Repeat("长对话", 24_000) + "\nend"
	for _, tc := range []struct {
		name   string
		resume string
		want   string
	}{
		{name: "fresh", want: "<system_instructions>\n" + system + "\n</system_instructions>\n\n" + prompt},
		{name: "resume", resume: "native-session", want: prompt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := AgentCLICommand(AgentCLIDeepSeek, AgentCLICommandOptions{
				Binary: bin, Chat: true, Resume: tc.resume, SystemPrompt: system, Prompt: prompt,
			})
			if _, err := ExecuteLocalCommand(t.Context(), dir, command, RunCredentials{}, nil); err != nil {
				t.Fatalf("DeepSeek failed to start: %v", err)
			}
			got, err := os.ReadFile(bin + ".stdin")
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("DeepSeek received %d bytes on stdin, want %d", len(got), len(tc.want))
			}
			if !ConversationCommandFor(command, RunCredentials{}).Interactive {
				t.Fatal("conversation execution must forward stdin")
			}
		})
	}
}

func TestDeepSeekShortPromptUsesPositionalArgument(t *testing.T) {
	prompt := strings.Repeat("x", maxArgvPromptBytes)
	command := AgentCLICommand(AgentCLIDeepSeek, AgentCLICommandOptions{Chat: true, Prompt: prompt})
	if command.Stdin != nil || command.Argv[len(command.Argv)-1] != prompt {
		t.Fatal("a prompt within the byte limit should remain a positional argument")
	}
}

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

func TestDeepSeekHarnessZhipuFlashOutputLimit(t *testing.T) {
	for _, tc := range []struct {
		name, baseURL, model, protocol string
		limit                          bool
	}{
		{"Zhipu Flash", "https://open.bigmodel.cn/api/paas/v4/", "glm-4-flash-250414", AgentCLIProtocolChat, true},
		{"another model", "https://open.bigmodel.cn/api/paas/v4", "glm-4.7-flash", AgentCLIProtocolChat, false},
		{"custom gateway", "https://open.bigmodel.cn.example.com/v1", "glm-4-flash-250414", AgentCLIProtocolChat, false},
		{"different protocol", "https://open.bigmodel.cn/api/anthropic", "glm-4-flash-250414", AgentCLIProtocolMessages, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			_, _, err := ApplyAgentCLICredential(AgentCLIDeepSeek, ProviderCredential{
				APIKey: "fixture", BaseURL: tc.baseURL, Protocol: tc.protocol,
			}, AgentCLIOptions{Model: tc.model}, HostAgentCLIHome(home), "/home",
				[]string{"dsh", "--profile", "headless", "--json", "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			patch, err := os.ReadFile(filepath.Join(home, ".dsh", "agentflow-model.json"))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(patch), `"maxTokens": 16384`); got != tc.limit {
				t.Fatalf("model patch has output limit=%v, want %v", got, tc.limit)
			}
		})
	}
}
