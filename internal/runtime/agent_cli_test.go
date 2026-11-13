package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestAgentCLICommandChatRestrictions(t *testing.T) {
	chat := func(kind string, o AgentCLICommandOptions) CommandSpec {
		o.Chat, o.Prompt, o.SystemPrompt = true, "hello", "SYSTEM"
		if o.Model == "" {
			o.Model = "m1"
		}
		return AgentCLICommand(kind, o)
	}
	cases := []struct {
		kind string
		o    AgentCLICommandOptions
		want []string
	}{
		{AgentCLICursor, AgentCLICommandOptions{}, []string{"--force", "--approve-mcps", "--model", "m1"}},
		{AgentCLIPi, AgentCLICommandOptions{SystemPromptFile: "/s.md"}, []string{"--exclude-tools", "bash,edit,write", "--append-system-prompt", "/s.md"}},
		{AgentCLIDeepSeek, AgentCLICommandOptions{}, []string{"--permission-mode", "read-only"}},
		{AgentCLIKimi, AgentCLICommandOptions{AgentFile: "/a.md"}, []string{"--agent-file", "/a.md"}},
		{AgentCLIOpenCode, AgentCLICommandOptions{Resume: "ses_1"}, []string{"-s", "ses_1", "-m", "agentflow/m1"}},
	}
	for _, c := range cases {
		command := chat(c.kind, c.o)
		if command.Provider != c.kind || !command.AgentCLI.ReadOnly {
			t.Fatalf("%s: provider=%q readOnly=%v", c.kind, command.Provider, command.AgentCLI.ReadOnly)
		}
		for _, arg := range c.want {
			if !slices.Contains(command.Argv, arg) {
				t.Errorf("%s argv misses %q: %q", c.kind, arg, command.Argv)
			}
		}
	}
	// The system prompt is inlined on the first turn only; OpenCode and Pi
	// load it themselves and Kimi carries it in its agent file.
	if argv := chat(AgentCLICursor, AgentCLICommandOptions{}).Argv; !strings.Contains(argv[len(argv)-1], "SYSTEM") {
		t.Errorf("first cursor turn lacks the system prompt: %q", argv[len(argv)-1])
	}
	if argv := chat(AgentCLICursor, AgentCLICommandOptions{Resume: "c1"}).Argv; argv[len(argv)-1] != "hello" {
		t.Errorf("resumed cursor turn repeats the system prompt: %q", argv[len(argv)-1])
	}
	for _, kind := range []string{AgentCLIKimi, AgentCLIPi, AgentCLIOpenCode} {
		if argv := chat(kind, AgentCLICommandOptions{}).Argv; argv[len(argv)-1] != "hello" {
			t.Errorf("%s inlines the system prompt: %q", kind, argv[len(argv)-1])
		}
	}
	// A resumed Kimi session keeps its agent; -S cannot be combined with it.
	if argv := chat(AgentCLIKimi, AgentCLICommandOptions{AgentFile: "/a.md", Resume: "session_1"}).Argv; slices.Contains(argv, "--agent-file") || !slices.Contains(argv, "session_1") {
		t.Errorf("resumed kimi argv: %q", argv)
	}
	if argv := AgentCLICommand(AgentCLIDeepSeek, AgentCLICommandOptions{Prompt: "x"}).Argv; !slices.Contains(argv, "danger-full-access") {
		t.Errorf("SOP dsh argv: %q", argv)
	}
}

func TestAgentCLICommandAgyEffortSuffix(t *testing.T) {
	for _, tc := range []struct{ model, effort, want string }{
		{"gemini-3.8-flash-medium", "low", "gemini-3.8-flash-low"},
		{"gemini-3.8-flash-medium", "", "gemini-3.8-flash-medium"},
		{"gemini-3.8-flash", "low", "gemini-3.8-flash"},
		{"gemini-4-argon-xhigh", "max", "gemini-4-argon-max"},
		{"gateway-model", "low", "gateway-model"},
	} {
		argv := AgentCLICommand(AgentCLIAntigravity, AgentCLICommandOptions{Model: tc.model, Effort: tc.effort, Prompt: "p"}).Argv
		if i := slices.Index(argv, "--model"); i < 0 || argv[i+1] != tc.want {
			t.Errorf("%s at %q: %q", tc.model, tc.effort, argv)
		}
		if want := tc.effort != "" && AgyModelEfforts(tc.model) != nil; slices.Contains(argv, "--effort") != want {
			t.Errorf("%s at %q: --effort passed=%v: %q", tc.model, tc.effort, !want, argv)
		}
	}
}

func TestApplyAgentCLICredential(t *testing.T) {
	home := t.TempDir()
	if _, _, err := ApplyAgentCLICredential(AgentCLIOpenCode, ProviderCredential{}, AgentCLIOptions{Model: "m"}, HostAgentCLIHome(home), home, nil); err == nil || !strings.Contains(err.Error(), "API Key") {
		t.Fatalf("missing key: %v", err)
	}
	if _, _, err := ApplyAgentCLICredential(AgentCLIPi, ProviderCredential{APIKey: "k"}, AgentCLIOptions{Model: "m"}, HostAgentCLIHome(home), home, nil); err == nil || !strings.Contains(err.Error(), "Base URL") {
		t.Fatalf("pi without Base URL: %v", err)
	}
	if _, _, err := ApplyAgentCLICredential(AgentCLIKimi, ProviderCredential{APIKey: "k"}, AgentCLIOptions{}, HostAgentCLIHome(home), home, nil); err == nil || !strings.Contains(err.Error(), "模型") {
		t.Fatalf("kimi without model: %v", err)
	}

	// The key travels in the environment; config files only name the variable.
	_, env, err := ApplyAgentCLICredential(AgentCLIOpenCode, ProviderCredential{APIKey: "secret-key", BaseURL: "https://gw.example/", Protocol: "messages"}, AgentCLIOptions{Model: "m"}, HostAgentCLIHome(home), home, []string{"opencode", "run", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if env[agentCLIKeyEnv] != "secret-key" || strings.Contains(env["OPENCODE_CONFIG_CONTENT"], "secret-key") || !strings.Contains(env["OPENCODE_CONFIG_CONTENT"], `"baseURL":"https://gw.example/v1"`) || !strings.Contains(env["OPENCODE_CONFIG_CONTENT"], "@ai-sdk/anthropic") {
		t.Fatalf("opencode env: %v", env)
	}

	_, env, err = ApplyAgentCLICredential(AgentCLIKimi, ProviderCredential{APIKey: "secret-key", BaseURL: "https://api.example/v1", Protocol: "responses"}, AgentCLIOptions{Model: "m"}, HostAgentCLIHome(home), "/w/home", nil)
	if err != nil {
		t.Fatal(err)
	}
	toml, _ := os.ReadFile(filepath.Join(home, ".kimi-code", "config.toml"))
	if env["KIMI_CODE_HOME"] != "/w/home/.kimi-code" || !strings.Contains(string(toml), `type = "openai_responses"`) || strings.Contains(string(toml), "secret-key") {
		t.Fatalf("kimi config: %v\n%s", env, toml)
	}

	// Chat on Antigravity adds deny rules inline, repeating the provider.
	argv, env, err := ApplyAgentCLICredential(AgentCLIAntigravity, ProviderCredential{APIKey: "g"}, AgentCLIOptions{ReadOnly: true}, HostAgentCLIHome(home), home, []string{"agy", "--output-format", "stream-json", "-p", "hi"})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(argv, "--settings")
	if i < 0 || argv[i+2] != "-p" || env["GEMINI_API_KEY"] != "g" {
		t.Fatalf("agy chat argv: %q env=%v", argv, env)
	}
	var settings struct {
		ModelProvider string `json:"modelProvider"`
		Permissions   struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(argv[i+1]), &settings); err != nil || settings.ModelProvider != "gemini" || !slices.Contains(settings.Permissions.Deny, "command(*)") {
		t.Fatalf("agy inline settings: %s (%v)", argv[i+1], err)
	}
	if argv, _, _ := ApplyAgentCLICredential(AgentCLIAntigravity, ProviderCredential{APIKey: "g"}, AgentCLIOptions{}, HostAgentCLIHome(home), home, []string{"agy", "-p", "hi"}); slices.Contains(argv, "--settings") {
		t.Fatalf("SOP agy argv carries chat settings: %q", argv)
	}

	argv, _, err = ApplyAgentCLICredential(AgentCLIDeepSeek, ProviderCredential{APIKey: "d"}, AgentCLIOptions{Model: "deepseek-v4-flash"}, HostAgentCLIHome(home), "/h", []string{"dsh", "--profile", "headless", "--json", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(argv[:5], []string{"dsh", "--profile", "headless", "--patch", "/h/.dsh/agentflow-model.json"}) {
		t.Fatalf("dsh argv: %q", argv)
	}
}

func TestWriteAgentCLIMCPConfigChat(t *testing.T) {
	entries := map[string]any{"gw": map[string]any{"type": "http", "url": "http://127.0.0.1:1/mcp", "headers": map[string]any{"Authorization": "Bearer t"}}}
	read := func(home, rel string) map[string]any {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(home, rel))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	home := t.TempDir()
	if err := WriteAgentCLIMCPConfig(AgentCLICursor, HostAgentCLIHome(home), entries, AgentCLIMCPOptions{ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if deny := read(home, ".cursor/cli-config.json")["permissions"].(map[string]any)["deny"].([]any); !slices.Contains(deny, any("Shell(**)")) {
		t.Fatalf("cursor chat deny: %v", deny)
	}
	if server := read(home, ".cursor/mcp.json")["mcpServers"].(map[string]any)["gw"].(map[string]any); server["url"] != "http://127.0.0.1:1/mcp" {
		t.Fatalf("cursor mcp: %v", server)
	}
	// An SOP invocation in the same home lifts the chat rules again.
	if err := WriteAgentCLIMCPConfig(AgentCLICursor, HostAgentCLIHome(home), entries, AgentCLIMCPOptions{}); err != nil {
		t.Fatal(err)
	}
	if deny := read(home, ".cursor/cli-config.json")["permissions"].(map[string]any)["deny"].([]any); len(deny) != 0 {
		t.Fatalf("cursor SOP deny: %v", deny)
	}
	if err := WriteAgentCLIMCPConfig(AgentCLIOpenCode, HostAgentCLIHome(home), entries, AgentCLIMCPOptions{ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	doc := read(home, ".config/opencode/opencode.json")
	if permission := doc["permission"].(map[string]any); permission["bash"] != "deny" || permission["edit"] != "deny" {
		t.Fatalf("opencode chat permission: %v", permission)
	}
	if server := doc["mcp"].(map[string]any)["gw"].(map[string]any); server["type"] != "remote" {
		t.Fatalf("opencode mcp: %v", server)
	}
	if err := WriteAgentCLIMCPConfig(AgentCLIAntigravity, HostAgentCLIHome(home), entries, AgentCLIMCPOptions{}); err != nil {
		t.Fatal(err)
	}
	if server := read(home, ".gemini/config/mcp_config.json")["mcpServers"].(map[string]any)["gw"].(map[string]any); server["serverUrl"] == nil {
		t.Fatalf("agy mcp: %v", server)
	}
}

func TestWriteKimiChatAgent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.md")
	if err := WriteKimiChatAgent(path, "Be terse."); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if text := string(data); !strings.HasPrefix(text, "---\nname: agentflow-chat\n") || !strings.Contains(text, "disallowedTools: [Bash, Edit, Write") || !strings.HasSuffix(text, "---\nBe terse.\n") {
		t.Fatalf("kimi agent:\n%s", text)
	}
}

func TestAgentCLITranslatorPiNestedTools(t *testing.T) {
	translate := NewAgentCLITranslator(AgentCLIPi)
	var lines []string
	for _, raw := range []string{
		`{"type":"session","id":"s-1"}`,
		`{"type":"tool_execution_start","toolCallId":"outer","toolName":"codemode","args":{}}`,
		`{"type":"tool_execution_start","toolCallId":"inner","toolName":"probe_read_secret","args":{},"parentToolCallId":"outer"}`,
		`{"type":"tool_execution_end","toolCallId":"inner","result":{"content":[{"type":"text","text":"secret=42"}]},"isError":false,"parentToolCallId":"outer"}`,
	} {
		lines = append(lines, translate(raw)...)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `"name":"probe_read_secret"`) || !strings.Contains(joined, `"tool_use_id":"inner"`) {
		t.Fatalf("nested pi tool calls are not reported:\n%s", joined)
	}
}

func TestAgentCLITranslatorDeepSeekReplacesTruncatedTextWithFinal(t *testing.T) {
	texts := func(lines []string) []string {
		var out []string
		for _, line := range lines {
			var event struct {
				Type    string
				Message struct{ Content []struct{ Type, Text string } }
			}
			if json.Unmarshal([]byte(line), &event) == nil && event.Type == "assistant" {
				for _, block := range event.Message.Content {
					if block.Type == "text" {
						out = append(out, block.Text)
					}
				}
			}
		}
		return out
	}
	full := strings.Repeat("长篇正文", 1000) + "（全文完）"
	preview := full[:8190]
	translate := NewAgentCLITranslator(AgentCLIDeepSeek)
	var lines []string
	for _, raw := range []string{
		`{"type":"session","sessionId":"s-1"}`,
		`{"type":"text","truncated":true,"text":` + strconv.Quote(preview) + `}`,
		`{"type":"status","phase":"step_end","usage":{"inputTokens":1,"outputTokens":2}}`,
		`{"type":"status","phase":"turn_end","reason":{"kind":"completed"}}`,
		`{"type":"final","sessionId":"s-1","reason":{"kind":"completed"},"text":` + strconv.Quote(full) + `}`,
	} {
		lines = append(lines, translate(raw)...)
	}
	if got := texts(lines); len(got) != 1 || got[0] != full {
		t.Fatalf("answer blocks %d, first %d bytes; want the whole final text once", len(got), len(got[0]))
	}
	if last := lines[len(lines)-1]; !strings.Contains(last, `"type":"result"`) || !strings.Contains(last, "（全文完）") {
		t.Fatalf("result: %.200s", last)
	}

	translate = NewAgentCLITranslator(AgentCLIDeepSeek)
	lines = nil
	for _, raw := range []string{
		`{"type":"text","text":"短回复"}`,
		`{"type":"text","truncated":true,"text":"被截断的说明"}`,
		`{"type":"tool_call","callId":"c1","tool":"read","input":{}}`,
	} {
		lines = append(lines, translate(raw)...)
	}
	if got := texts(lines); !slices.Equal(got, []string{"短回复", "被截断的说明"}) || !strings.Contains(lines[len(lines)-1], `"tool_use"`) {
		t.Fatalf("texts before a tool call: %q\n%s", got, strings.Join(lines, "\n"))
	}
}

func TestAgentCLIUserCredentials(t *testing.T) {
	models := []domain.CLIModel{{ID: "widget", Efforts: map[string]string{"low": "widget-low"}}}
	creds := AgentCLIUserCredentials(domain.User{AgentCLICredentials: map[string]domain.AgentCLICredential{"kimi": {APIKey: "k", BaseURL: "u", Protocol: "messages", Models: models}}})
	if !reflect.DeepEqual(creds["kimi"], ProviderCredential{APIKey: "k", BaseURL: "u", Protocol: "messages", Models: models}) {
		t.Fatalf("credentials: %+v", creds)
	}
	if AgentCLIUserCredentials(domain.User{}) != nil {
		t.Fatal("empty user has agent CLI credentials")
	}
}

func TestWriteAgentCLIFileSecurity(t *testing.T) {
	home := t.TempDir()
	data := []byte(`{"test":true}`)

	// Normal write within home subdirectories
	if err := writeAgentCLIFile(HostAgentCLIHome(home), ".cursor/cli-config.json", data); err != nil {
		t.Fatalf("valid write failed: %v", err)
	}
	readBack, err := os.ReadFile(filepath.Join(home, ".cursor", "cli-config.json"))
	if err != nil || string(readBack) != string(data) {
		t.Fatalf("read back content mismatch: %q, %v", readBack, err)
	}

	// Traversal attacks must be rejected
	for _, invalid := range []string{
		"../escape.json",
		"../../escape.json",
		"sub/../../escape.json",
		"/etc/passwd",
		filepath.Join("..", "foo.json"),
	} {
		if err := writeAgentCLIFile(HostAgentCLIHome(home), invalid, data); err == nil {
			t.Errorf("path %q was accepted, expected error", invalid)
		}
	}
}

func TestWriteAgentCLIFileRejectsSymlinkedHome(t *testing.T) {
	outside := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(outside, home); err != nil {
		t.Fatal(err)
	}
	if err := writeAgentCLIFile(HostAgentCLIHome(home), ".kimi-code/config.toml", []byte("x")); err == nil {
		t.Fatal("write through a symlinked home was accepted")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("files written outside home: %v", entries)
	}
}

func TestBindCommandRejectsSymlinkedParentsBeforeWritingCredentials(t *testing.T) {
	for _, kind := range []string{AgentCLIKimi, AgentCLIZCode} {
		for _, component := range []string{".agentflow", "private-home", "tmp", "home"} {
			t.Run(kind+"/"+component, func(t *testing.T) {
				workspace, outside := t.TempDir(), t.TempDir()
				command := AgentCLICommand(kind, AgentCLICommandOptions{Model: "m", Prompt: "p"})
				command.PrivateHome, command.PrivateHomeKey = true, kind+"-agent-1"
				command.CredentialBindings = []CredentialBinding{BindAgentCLI}
				link := filepath.Join(workspace, ".agentflow")
				if component != ".agentflow" {
					link = filepath.Join(link, component)
					if component == "home" {
						link = filepath.Join(workspace, ".agentflow", "private-home", command.PrivateHomeKey)
					}
				}
				if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, link); err != nil {
					t.Fatal(err)
				}
				sb := &localSandbox{workspace: workspace, environment: map[string]string{}}
				credentials := RunCredentials{AgentCLI: map[string]ProviderCredential{
					kind: {APIKey: "fake-test-key", BaseURL: "https://example.test"},
				}}
				if _, err := bindCommand(sb, workspace, command, credentials); err == nil {
					t.Fatal("symlinked private-home component was accepted")
				}
				entries, err := os.ReadDir(outside)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Fatalf("credential files written outside the workspace: %v", entries)
				}
			})
		}
	}
}

func TestBindAgentCLICommandRejectsSymlinkedPrivateHome(t *testing.T) {
	for _, kind := range []string{AgentCLIKimi, AgentCLIZCode} {
		t.Run(kind, func(t *testing.T) {
			workspace, outside := t.TempDir(), t.TempDir()
			command := AgentCLICommand(kind, AgentCLICommandOptions{Model: "m", Prompt: "p"})
			command.PrivateHome, command.PrivateHomeKey = true, kind+"-agent-1"
			parent := filepath.Join(workspace, ".agentflow", "private-home")
			if err := os.MkdirAll(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(parent, command.PrivateHomeKey)); err != nil {
				t.Fatal(err)
			}
			sb := &localSandbox{workspace: workspace, environment: map[string]string{}}
			if _, _, err := BindAgentCLICommand(sb, workspace, command, ProviderCredential{APIKey: "secret-key", BaseURL: "https://example.test"}, command.Argv); err == nil {
				t.Fatal("symlinked private home was accepted")
			}
			if _, err := bindCommand(sb, workspace, command, RunCredentials{}); err == nil {
				t.Fatal("bindCommand accepted a symlinked private home")
			}
			if entries, _ := os.ReadDir(outside); len(entries) != 0 {
				t.Fatalf("files written outside the workspace: %v", entries)
			}
		})
	}
}

// Every component below the trusted base is checked, not only the HOME
// itself: a symlinked ancestor must not redirect mkdir or the config write.
func TestWriteAgentCLIMCPConfigRejectsSymlinkedAncestors(t *testing.T) {
	entries := map[string]any{"gw": map[string]any{"type": "http", "url": "http://127.0.0.1:1/mcp"}}
	for _, kind := range []string{AgentCLIKimi, AgentCLIZCode, AgentCLICursor} {
		for _, component := range []string{".agentflow", "private-home", "home"} {
			t.Run(kind+"/"+component, func(t *testing.T) {
				workspace, outside := t.TempDir(), t.TempDir()
				name := AgentCLIPrivateHomeKey(kind, "agent-1")
				link := map[string]string{
					".agentflow":   filepath.Join(workspace, ".agentflow"),
					"private-home": filepath.Join(workspace, ".agentflow", "private-home"),
					"home":         filepath.Join(workspace, ".agentflow", "private-home", name),
				}[component]
				if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, link); err != nil {
					t.Fatal(err)
				}
				if err := WriteAgentCLIMCPConfig(kind, WorkspaceAgentCLIHome(workspace, name), entries, AgentCLIMCPOptions{}); err == nil {
					t.Fatal("write through a symlinked ancestor was accepted")
				}
				// Cursor's credential lives in the environment only; the others write files.
				if _, _, err := ApplyAgentCLICredential(kind, ProviderCredential{APIKey: "fake-test-key", BaseURL: "https://example.test"}, AgentCLIOptions{Model: "m"}, WorkspaceAgentCLIHome(workspace, name), "/h", []string{kind}); err == nil && kind != AgentCLICursor {
					t.Fatal("credential write through a symlinked ancestor was accepted")
				}
				if got, _ := os.ReadDir(outside); len(got) != 0 {
					t.Fatalf("files written outside the workspace: %v", got)
				}
			})
		}
	}
}

func TestBindAgentCLICommandRejectsSymlinkedPrivateHomeParent(t *testing.T) {
	for _, kind := range []string{AgentCLIKimi, AgentCLIZCode} {
		t.Run(kind, func(t *testing.T) {
			workspace, outside := t.TempDir(), t.TempDir()
			command := AgentCLICommand(kind, AgentCLICommandOptions{Model: "m", Prompt: "p"})
			command.PrivateHome, command.PrivateHomeKey = true, kind+"-agent-1"
			if err := os.MkdirAll(filepath.Join(workspace, ".agentflow"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(workspace, ".agentflow", "private-home")); err != nil {
				t.Fatal(err)
			}
			sb := &localSandbox{workspace: workspace, environment: map[string]string{}}
			if _, _, err := BindAgentCLICommand(sb, workspace, command, ProviderCredential{APIKey: "fake-test-key", BaseURL: "https://example.test"}, command.Argv); err == nil {
				t.Fatal("symlinked private-home parent was accepted")
			}
			if got, _ := os.ReadDir(outside); len(got) != 0 {
				t.Fatalf("files written outside the workspace: %v", got)
			}
		})
	}
}

// The trusted base may itself be reached through a symlink (macOS temp
// directories are); only components below it are refused.
func TestOpenAgentCLIHomeTrustsBase(t *testing.T) {
	real := t.TempDir()
	base := filepath.Join(t.TempDir(), "base")
	if err := os.Symlink(real, base); err != nil {
		t.Fatal(err)
	}
	if err := writeAgentCLIFile(AgentCLIHome{Base: base, Rel: "a/home"}, ".kimi-code/config.toml", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(real, "a", "home", ".kimi-code", "config.toml")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"", ".", "..", "../x", "/abs"} {
		if _, err := openAgentCLIHome(AgentCLIHome{Base: base, Rel: rel}); err == nil {
			t.Errorf("rel %q was accepted", rel)
		}
	}
}
