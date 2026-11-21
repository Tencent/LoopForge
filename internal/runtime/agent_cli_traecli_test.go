package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestTraeCLICommandAndCredentials(t *testing.T) {
	if !domain.IsAgentCLIKind(AgentCLITraeCLI) || !domain.SupportsRunnerLogin(AgentCLITraeCLI) {
		t.Fatalf("traecli must be an AgentCLI supporting runner login")
	}

	sop := AgentCLICommand(AgentCLITraeCLI, AgentCLICommandOptions{
		Prompt:       "fix bug",
		SystemPrompt: "SYS",
		Model:        "gpt-5",
		Effort:       "high",
	})
	if sop.Provider != AgentCLITraeCLI {
		t.Fatalf("provider = %q", sop.Provider)
	}
	for _, want := range []string{"traecli", "exec", "--json", "--skip-git-repo-check", "--sandbox", "danger-full-access", "--dangerously-bypass-hook-trust", `approval_policy="never"`, "--permission-mode", "bypass_permissions", "--model", "gpt-5"} {
		if !slices.Contains(sop.Argv, want) {
			t.Fatalf("sop argv missing %q: %q", want, sop.Argv)
		}
	}
	if !slices.Contains(sop.Argv, `model_reasoning_effort="high"`) {
		t.Fatalf("sop argv missing reasoning effort: %q", sop.Argv)
	}
	if last := sop.Argv[len(sop.Argv)-1]; !strings.Contains(last, "SYS") || !strings.HasSuffix(last, "fix bug") {
		t.Fatalf("sop prompt = %q", last)
	}

	chat := AgentCLICommand(AgentCLITraeCLI, AgentCLICommandOptions{
		Prompt: "hi",
		Chat:   true,
		Resume: "thread-1",
	})
	if !slices.Contains(chat.Argv, "read-only") || !slices.Contains(chat.Argv, "resume") || !slices.Contains(chat.Argv, "thread-1") || slices.Contains(chat.Argv, "bypass_permissions") {
		t.Fatalf("chat argv = %q", chat.Argv)
	}

	dir := t.TempDir()
	home := AgentCLIHome{Base: dir, Rel: "home"}
	if err := os.MkdirAll(filepath.Join(dir, "home"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteAgentCLIMCPConfig(AgentCLITraeCLI, home, map[string]any{
		"agentflow-session": map[string]any{
			"url":     "http://127.0.0.1:9999/mcp",
			"headers": map[string]string{"Authorization": "Bearer tok"},
		},
		"agentflow-gate": map[string]any{
			"command": "/app/bin/agentflow-gate-mcp",
			"cwd":     "/workspace",
			"args":    []string{"--stdio"},
			"env":     map[string]string{"AGENTFLOW_GATE_RESULT_PATH": "/workspace/.agentflow/gate.json"},
		},
	}, AgentCLIMCPOptions{ReadOnly: true}); err != nil {
		t.Fatalf("WriteAgentCLIMCPConfig: %v", err)
	}
	tomlBytes, err := os.ReadFile(filepath.Join(dir, "home", ".trae", "traecli.toml"))
	if err != nil {
		t.Fatalf("read traecli.toml: %v", err)
	}
	tomlText := string(tomlBytes)
	for _, want := range []string{
		"[mcp_servers.agentflow-session]",
		`url = "http://127.0.0.1:9999/mcp"`,
		`http_headers = { "Authorization" = "Bearer tok" }`,
		"[mcp_servers.agentflow-gate]",
		`command = "/app/bin/agentflow-gate-mcp"`,
		`cwd = "/workspace"`,
		`args = ["--stdio"]`,
		`env = { "AGENTFLOW_GATE_RESULT_PATH" = "/workspace/.agentflow/gate.json" }`,
		`default_tools_approval_mode = "approve"`,
	} {
		if !strings.Contains(tomlText, want) {
			t.Fatalf("traecli.toml missing %q:\n%s", want, tomlText)
		}
	}

	argv, env, err := ApplyAgentCLICredential(AgentCLITraeCLI, ProviderCredential{
		APIKey:   "sk-trae",
		BaseURL:  "https://gw.example/v1",
		Protocol: AgentCLIProtocolChat,
	}, AgentCLIOptions{Model: "m1"}, home, filepath.Join(dir, "home"), chat.Argv)
	if err != nil {
		t.Fatalf("ApplyAgentCLICredential: %v", err)
	}
	if env[agentCLIKeyEnv] != "sk-trae" || env["TRAE_HOME"] != filepath.ToSlash(filepath.Join(dir, "home", ".trae")) {
		t.Fatalf("env = %+v", env)
	}
	resumeIdx := slices.Index(argv, "resume")
	providerIdx := slices.Index(argv, `model_provider="agentflow"`)
	if resumeIdx < 0 || providerIdx < 0 || providerIdx > resumeIdx {
		t.Fatalf("provider config must precede resume: %q", argv)
	}
	if !slices.Contains(argv, `model_providers.agentflow.wire_api="chat"`) {
		t.Fatalf("missing wire_api=chat: %q", argv)
	}
}

func TestTraeCLIStreamTranslator(t *testing.T) {
	tr := NewAgentCLITranslator(AgentCLITraeCLI)
	if tr == nil {
		t.Fatal("missing traecli translator")
	}
	lines := []string{
		`{"type":"thread.started","thread_id":"th-1"}`,
		`{"type":"item.completed","item":{"id":"r1","type":"reasoning","text":"think"}}`,
		`{"type":"item.started","item":{"id":"c1","type":"command_execution","command":"ls"}}`,
		`{"type":"item.completed","item":{"id":"c1","type":"command_execution","exit_code":0,"aggregated_output":"ok"}}`,
		`{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"done"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":5,"cached_input_tokens":2}}`,
	}
	var got []map[string]any
	for _, line := range lines {
		for _, out := range tr(line) {
			var m map[string]any
			if err := json.Unmarshal([]byte(out), &m); err != nil {
				t.Fatalf("invalid json %q: %v", out, err)
			}
			got = append(got, m)
		}
	}
	if len(got) == 0 {
		t.Fatal("expected translated events")
	}
	last := got[len(got)-1]
	if last["type"] != "result" || last["is_error"] != false || last["session_id"] != "th-1" {
		t.Fatalf("unexpected terminal event: %+v", last)
	}
}
