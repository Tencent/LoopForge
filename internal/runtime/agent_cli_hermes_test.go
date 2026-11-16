package runtime

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestHermesCommand(t *testing.T) {
	sop := AgentCLICommand(AgentCLIHermes, AgentCLICommandOptions{Prompt: "do it", SystemPrompt: "SYS", Model: "glm-5.3-flash"}).Argv
	if sop[0] != "hermes" {
		t.Fatalf("sop argv: %q", sop)
	}
	if !slices.Contains(sop, "--yolo") {
		t.Errorf("sop argv must bypass approvals: %q", sop)
	}
	if !slices.Contains(sop, "-z") || sop[len(sop)-2] != "-z" || !strings.HasSuffix(sop[len(sop)-1], "do it") {
		t.Errorf("prompt must be the last -z value: %q", sop)
	}
	if !strings.Contains(sop[len(sop)-1], "SYS") {
		t.Errorf("system prompt must be inlined for Hermes: %q", sop)
	}
	if slices.Contains(sop, "--resume") {
		t.Errorf("sop argv: %q", sop)
	}

	chat := AgentCLICommand(AgentCLIHermes, AgentCLICommandOptions{Prompt: "hi", SystemPrompt: "SYS", Model: "glm-5.3-flash", Chat: true, Resume: "sess_1"})
	if chat.Provider != AgentCLIHermes || chat.AgentCLI.Model != "glm-5.3-flash" {
		t.Fatalf("chat spec: %+v", chat)
	}
	if slices.Contains(chat.Argv, "--yolo") {
		t.Errorf("chat must keep Hermes approvals: %q", chat.Argv)
	}
	if i := slices.Index(chat.Argv, "--resume"); i < 0 || chat.Argv[i+1] != "sess_1" {
		t.Fatalf("chat resume: %q", chat.Argv)
	}
	if chat.Argv[len(chat.Argv)-2] != "-z" || chat.Argv[len(chat.Argv)-1] != "hi" {
		t.Errorf("chat prompt: %q", chat.Argv)
	}
}

func TestHermesCredentialWritesRouteAndUsageFlag(t *testing.T) {
	home := HostAgentCLIHome(t.TempDir())
	argv := AgentCLICommand(AgentCLIHermes, AgentCLICommandOptions{Prompt: "hi"}).Argv
	bound, env, err := ApplyAgentCLICredential(AgentCLIHermes,
		ProviderCredential{APIKey: "sk-secret", BaseURL: "https://gw.example/v1", Protocol: AgentCLIProtocolChat},
		AgentCLIOptions{Model: "glm-5.3-flash", Effort: "high"}, home, "/w/home", argv)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if env["HERMES_HOME"] != "/w/home/.hermes" {
		t.Fatalf("HERMES_HOME: %q", env["HERMES_HOME"])
	}
	usage := "/w/home/.hermes/agentflow-usage.json"
	// --usage-file must precede the prompt so the CLI still parses it.
	if i := slices.Index(bound, "--usage-file"); i < 0 || bound[i+1] != usage {
		t.Fatalf("usage flag: %q", bound)
	}
	if i, j := slices.Index(bound, "--usage-file"), slices.Index(bound, "-z"); i > j {
		t.Fatalf("usage flag must come before -z: %q", bound)
	}

	raw, err := os.ReadFile(filepath.Join(home.Path(), filepath.FromSlash(hermesConfigPath)))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	for _, want := range []string{
		"provider: agentflow",
		`default: "glm-5.3-flash"`,
		`base_url: "https://gw.example/v1"`,
		`api_key: "sk-secret"`,
		`api: "chat"`,
		`reasoning_effort: "high"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("config misses %q:\n%s", want, raw)
		}
	}
}

func TestHermesCredentialRequiresBaseURLAndModel(t *testing.T) {
	if _, _, err := ApplyAgentCLICredential(AgentCLIHermes, ProviderCredential{APIKey: "k"}, AgentCLIOptions{Model: "m"}, HostAgentCLIHome(t.TempDir()), "/h", nil); err == nil || !strings.Contains(err.Error(), "Base URL") {
		t.Fatalf("missing base url: %v", err)
	}
	if _, _, err := ApplyAgentCLICredential(AgentCLIHermes, ProviderCredential{APIKey: "k", BaseURL: "https://gw.example/v1"}, AgentCLIOptions{}, HostAgentCLIHome(t.TempDir()), "/h", nil); err == nil || !strings.Contains(err.Error(), "模型") {
		t.Fatalf("missing model: %v", err)
	}
	if _, _, err := ApplyAgentCLICredential(AgentCLIHermes, ProviderCredential{}, AgentCLIOptions{Model: "m"}, HostAgentCLIHome(t.TempDir()), "/h", nil); err == nil || !strings.Contains(err.Error(), "Hermes API Key") {
		t.Fatalf("missing key: %v", err)
	}
}

func TestHermesTranslatorForwardsPlainText(t *testing.T) {
	translate := NewAgentCLITranslator(AgentCLIHermes)
	if translate == nil {
		t.Fatal("no translator")
	}
	lines := translate("镜像已推送：b2e0e25-202611161214UTC")
	if len(lines) == 0 {
		t.Fatal("plain text line produced no events")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "镜像已推送") {
		t.Fatalf("event loses the text: %s", joined)
	}
	if got := translate("   "); got != nil {
		t.Fatalf("blank line: %v", got)
	}
}

func TestHermesMCPConfigIsANoop(t *testing.T) {
	// Tier 1 has no Hermes MCP bridge; the writer must not fail the node.
	if err := WriteAgentCLIMCPConfig(AgentCLIHermes, HostAgentCLIHome(t.TempDir()), map[string]any{"gw": map[string]any{"url": "http://127.0.0.1:1/mcp"}}, AgentCLIMCPOptions{}); err != nil {
		t.Fatalf("mcp config: %v", err)
	}
}