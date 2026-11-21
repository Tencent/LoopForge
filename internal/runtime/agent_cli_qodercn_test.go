package runtime

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestQoderCNCommand(t *testing.T) {
	sop := AgentCLICommand(AgentCLIQoderCN, AgentCLICommandOptions{Prompt: "do it", SystemPrompt: "SYS", Model: "m"}).Argv
	if sop[0] != "qodercn" || sop[1] != "-p" || !strings.Contains(sop[2], "SYS") || !strings.HasSuffix(sop[2], "do it") {
		t.Fatalf("sop argv: %q", sop)
	}
	for _, arg := range []string{"--output-format", "stream-json", "--yolo", "--disallowed-tools", qoderCNSOPDisallowedTools} {
		if !slices.Contains(sop, arg) {
			t.Errorf("sop argv misses %q: %q", arg, sop)
		}
	}
	if strings.Contains(qoderCNSOPDisallowedTools, "Bash") {
		t.Errorf("SOP keeps native tools: %q", qoderCNSOPDisallowedTools)
	}

	chat := AgentCLICommand(AgentCLIQoderCN, AgentCLICommandOptions{Prompt: "hi", SystemPrompt: "SYS", Chat: true, Resume: "sess_1"})
	if !chat.AgentCLI.ReadOnly || chat.Provider != AgentCLIQoderCN {
		t.Fatalf("chat spec: %+v", chat)
	}
	i := slices.Index(chat.Argv, "--disallowed-tools")
	if i < 0 || chat.Argv[i+1] != qoderCNChatDisallowedTools {
		t.Fatalf("chat argv: %q", chat.Argv)
	}
	if slices.Contains(chat.Argv, "--yolo") {
		t.Errorf("chat must not bypass permissions: %q", chat.Argv)
	}
	for _, tool := range strings.Fields("Bash Edit Write WebFetch WebSearch Agent AskUserQuestion EnterPlanMode ExitPlanMode") {
		if !slices.Contains(strings.Fields(chat.Argv[i+1]), tool) {
			t.Errorf("chat does not disallow %s", tool)
		}
	}
	if j := slices.Index(chat.Argv, "--resume"); j < 0 || chat.Argv[j+1] != "sess_1" {
		t.Fatalf("chat resume: %q", chat.Argv)
	}
	// A resumed chat session keeps the system prompt of its first turn.
	if chat.Argv[2] != "hi" {
		t.Fatalf("resumed prompt: %q", chat.Argv[2])
	}
	workspace := AgentCLICommand(AgentCLIQoderCN, AgentCLICommandOptions{Chat: true, Workspace: true, Prompt: "edit", Resume: "sess_1"})
	i = slices.Index(workspace.Argv, "--disallowed-tools")
	if workspace.AgentCLI.ReadOnly || i < 0 || workspace.Argv[i+1] != qoderCNSOPDisallowedTools || !slices.Contains(workspace.Argv, "--yolo") {
		t.Fatalf("workspace retains native tools: %q", workspace.Argv)
	}
}

func TestApplyQoderCNCredential(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".qoder-cn"), 0o700); err != nil {
		t.Fatal(err)
	}
	seed := `{"mcpServers":{"gw":{"url":"http://127.0.0.1:1/mcp"}},"permissions":{"allow":["mcp__gw"]},"hooks":{"PreToolUse":[]},"general":{"defaultPermissionMode":"bypass_permissions"}}`
	if err := os.WriteFile(filepath.Join(home, ".qoder-cn", "settings.json"), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	argv := []string{"qodercn", "-p", "do it", "--output-format", "stream-json", "--yolo"}
	bound, env, err := ApplyAgentCLICredential(AgentCLIQoderCN, ProviderCredential{APIKey: " pat-secret "}, AgentCLIOptions{Model: "qoder-model"}, HostAgentCLIHome(home), "/w/home", argv)
	if err != nil {
		t.Fatal(err)
	}
	if env["QODERCN_PERSONAL_ACCESS_TOKEN"] != "pat-secret" || env["QODERCN_CONFIG_DIR"] != "/w/home/.qoder-cn" || env["QODERCN_MODEL"] != "qoder-model" {
		t.Fatalf("env: %v", env)
	}
	if !slices.Equal(bound, argv) {
		t.Fatalf("argv changed: %q", bound)
	}
	doc := readJSONFile(t, filepath.Join(home, ".qoder-cn", "settings.json"))
	if doc["model"].(map[string]any)["name"] != "qoder-model" {
		t.Fatalf("model not written: %v", doc)
	}
	// The merge keeps every other top-level key the CLI relies on.
	if doc["mcpServers"].(map[string]any)["gw"] == nil || doc["permissions"] == nil || doc["hooks"] == nil || doc["general"] == nil {
		t.Fatalf("merge lost keys: %v", doc)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".qoder-cn", "settings.json"))
	if strings.Contains(string(data), "pat-secret") {
		t.Fatalf("PAT leaked into settings.json:\n%s", data)
	}

	// An unset or "default" model keeps the CLI's own choice.
	for _, model := range []string{"", "default"} {
		h := t.TempDir()
		_, env, err := ApplyAgentCLICredential(AgentCLIQoderCN, ProviderCredential{APIKey: "k"}, AgentCLIOptions{Model: model}, HostAgentCLIHome(h), "/h", []string{"qodercn", "-p", "x"})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := env["QODERCN_MODEL"]; ok {
			t.Errorf("model %q must keep the CLI default: %v", model, env)
		}
		if doc := readJSONFile(t, filepath.Join(h, ".qoder-cn", "settings.json")); doc["model"] != nil {
			t.Errorf("model %q wrote a selection: %v", model, doc)
		}
	}

	// A custom Base URL is rejected: Qoder CN only reaches its official endpoint.
	if _, _, err := ApplyAgentCLICredential(AgentCLIQoderCN, ProviderCredential{APIKey: "k", BaseURL: "https://gw.example"}, AgentCLIOptions{Model: "m"}, HostAgentCLIHome(t.TempDir()), "/h", nil); err == nil || !strings.Contains(err.Error(), "官方端点") {
		t.Fatalf("baseURL must be rejected: %v", err)
	}
	// A missing PAT is rejected.
	if _, _, err := ApplyAgentCLICredential(AgentCLIQoderCN, ProviderCredential{}, AgentCLIOptions{Model: "m"}, HostAgentCLIHome(t.TempDir()), "/h", nil); err == nil || !strings.Contains(err.Error(), "Qoder CN") {
		t.Fatalf("missing PAT must be rejected: %v", err)
	}
	if _, _, err := applyQoderCNCredential(HostAgentCLIHome(t.TempDir()), "/h", "", "", "", "m", nil); err == nil || !strings.Contains(err.Error(), "PAT") {
		t.Fatalf("direct missing PAT: %v", err)
	}
}

func TestWriteAgentCLIMCPConfigQoderCN(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".qoder-cn"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".qoder-cn", "settings.json"), []byte(`{"model":{"name":"keep"},"permissions":{"deny":["Bash"]},"hooks":{"Stop":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	entries := map[string]any{"gw": map[string]any{"type": "http", "url": "https://m.example/mcp", "headers": map[string]any{"Authorization": "Bearer t"}}}
	if err := WriteAgentCLIMCPConfig(AgentCLIQoderCN, HostAgentCLIHome(home), entries, AgentCLIMCPOptions{}); err != nil {
		t.Fatal(err)
	}
	doc := readJSONFile(t, filepath.Join(home, ".qoder-cn", "settings.json"))
	servers := doc["mcpServers"].(map[string]any)
	if servers["gw"].(map[string]any)["url"] != "https://m.example/mcp" {
		t.Fatalf("mcp server: %v", servers)
	}
	if doc["model"].(map[string]any)["name"] != "keep" || doc["permissions"] == nil || doc["hooks"] == nil {
		t.Fatalf("MCP write clobbered other keys: %v", doc)
	}
}

func TestQoderCNConversationApprovalPreservesMCPAndModel(t *testing.T) {
	root := t.TempDir()
	home := AgentCLIHome{Base: root, Rel: "home"}
	if err := WriteAgentCLIMCPConfig(AgentCLIQoderCN, home, map[string]any{"agentflow_session": map[string]any{"type": "http", "url": "http://127.0.0.1/mcp"}}, AgentCLIMCPOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallConversationApproval(root, AgentCLIQoderCN, CommandSpec{Argv: []string{"qodercn"}}); err != nil {
		t.Fatal(err)
	}
	doc := readJSONFile(t, filepath.Join(root, "home", ".qoder-cn", "settings.json"))
	if doc["hooks"].(map[string]any)["PreToolUse"] == nil || doc["mcpServers"] == nil {
		t.Fatalf("lost approval or MCP: %v", doc)
	}
	// Credential binding happens after approval and must retain the guard.
	if _, _, err := ApplyAgentCLICredential(AgentCLIQoderCN, ProviderCredential{APIKey: "k"}, AgentCLIOptions{Model: "m"}, home, filepath.Join(root, "home"), []string{"qodercn"}); err != nil {
		t.Fatal(err)
	}
	doc = readJSONFile(t, filepath.Join(root, "home", ".qoder-cn", "settings.json"))
	if doc["hooks"].(map[string]any)["PreToolUse"] == nil || doc["mcpServers"] == nil || doc["model"].(map[string]any)["name"] != "m" {
		t.Fatalf("binding replaced approval/MCP/model: %v", doc)
	}
}

func TestQoderCNTranslatorPassthrough(t *testing.T) {
	translate := NewAgentCLITranslator(AgentCLIQoderCN)
	if translate == nil {
		t.Fatal("no translator for qodercn")
	}
	samples := []string{
		`{"type":"system","subtype":"init","session_id":"sess_1","model":"qoder"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]},"session_id":"sess_1"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"sess_1"}`,
	}
	for _, line := range samples {
		if out := translate(line); len(out) != 1 || out[0] != line {
			t.Fatalf("passthrough changed line: %q -> %q", line, out)
		}
	}
	if out := translate("  " + samples[0] + "  "); len(out) != 1 || out[0] != samples[0] {
		t.Fatalf("trim: %q", out)
	}
	for _, bad := range []string{"", "not json", "[1,2]", `{"broken":`} {
		if out := translate(bad); out != nil {
			t.Fatalf("garbage %q -> %q", bad, out)
		}
	}
}