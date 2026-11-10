package runtime

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestZCodeCommand(t *testing.T) {
	sop := AgentCLICommand(AgentCLIZCode, AgentCLICommandOptions{Prompt: "do it", SystemPrompt: "SYS", Model: "glm-5.3"}).Argv
	if sop[0] != "zcode" || sop[1] != "--prompt" || !strings.Contains(sop[2], "SYS") || !strings.HasSuffix(sop[2], "do it") {
		t.Fatalf("sop argv: %q", sop)
	}
	for _, arg := range []string{"--output-format", "stream-json", "--mode", "yolo", "--no-color", "--disallowed-tools", zcodeSOPDisallowedTools} {
		if !slices.Contains(sop, arg) {
			t.Errorf("sop argv misses %q: %q", arg, sop)
		}
	}
	if slices.Contains(sop, "--resume") || slices.Contains(sop, "--model") || strings.Contains(zcodeSOPDisallowedTools, "Bash") {
		t.Errorf("sop argv: %q", sop)
	}

	// The effort has no flag; it travels to the provider file.
	withEffort := AgentCLICommand(AgentCLIZCode, AgentCLICommandOptions{Prompt: "do it", Model: "glm-5.3", Effort: "low"})
	if withEffort.AgentCLI.Effort != "low" || slices.Contains(withEffort.Argv, "low") {
		t.Errorf("effort spec: %+v", withEffort)
	}

	chat := AgentCLICommand(AgentCLIZCode, AgentCLICommandOptions{Prompt: "hi", SystemPrompt: "SYS", Chat: true, Resume: "sess_1"})
	if !chat.AgentCLI.ReadOnly || chat.Provider != AgentCLIZCode {
		t.Fatalf("chat spec: %+v", chat)
	}
	i := slices.Index(chat.Argv, "--disallowed-tools")
	if i < 0 || chat.Argv[i+1] != zcodeChatDisallowedTools {
		t.Fatalf("chat argv: %q", chat.Argv)
	}
	for _, tool := range strings.Fields("Bash Edit Write WebFetch Agent SendMessage TaskOutput TaskStop mcp__node_repl__js AskUserQuestion EnterPlanMode ExitPlanMode") {
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
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return doc
}

func zcodeKeyFiles(t *testing.T, home string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(home, ".zcode", "v2", "provider_config.*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestApplyZCodeCredential(t *testing.T) {
	home := t.TempDir()
	if err := WriteAgentCLIMCPConfig(AgentCLIZCode, home, map[string]any{"gw": map[string]any{"type": "http", "url": "http://127.0.0.1:1/mcp"}}, AgentCLIMCPOptions{}); err != nil {
		t.Fatal(err)
	}
	argv := []string{"zcode", "--prompt", "it's \"quoted\" $HOME"}
	bound, env, err := ApplyAgentCLICredential(AgentCLIZCode, ProviderCredential{APIKey: " secret-key "}, AgentCLIOptions{Model: "glm-5.3"}, home, "/w/home", argv)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(bound[:4], []string{"sh", "-c", zcodeCleanupScript, "zcode"}) || !slices.Equal(bound[4:], argv) {
		t.Fatalf("wrapped argv: %q", bound)
	}

	files := zcodeKeyFiles(t, home)
	if len(files) != 1 {
		t.Fatalf("key files: %v", files)
	}
	if info, _ := os.Stat(files[0]); info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %v", info.Mode().Perm())
	}
	if env[zcodePersonalFileEnv] != "/w/home/.zcode/v2/"+filepath.Base(files[0]) || env["ZCODE_DATA_BASE_DIR"] != "/w/home" || env["USERPROFILE"] != "/w/home" {
		t.Fatalf("env: %v", env)
	}
	rules := readJSONFile(t, files[0])["config"].(map[string]any)
	provider := rules["providerConfigRules"].(map[string]any)["providerRules"].([]any)[0].(map[string]any)
	config := provider["config"].(map[string]any)
	api := config["api"].(map[string]any)
	if provider["providerId"] != "agentflow" || config["group"] != "standard-personal" ||
		config["access"].(map[string]any)["apiKey"] != "secret-key" || config["access"].(map[string]any)["type"] != "api-key" ||
		api["type"] != "anthropic-messages" || api["baseUrl"] != zcodeDefaultBaseURL ||
		config["personalModelIds"].([]any)[0] != "glm-5.3" {
		t.Fatalf("provider config: %v", provider)
	}
	if selection := rules["defaultModelSelection"].(map[string]any); selection["providerId"] != "agentflow" || selection["modelId"] != "glm-5.3" {
		t.Fatalf("selection: %v", selection)
	}

	// The cleanup hook joins the MCP configuration without replacing it, and
	// the key itself never reaches config.json.
	cli, _ := os.ReadFile(filepath.Join(home, zcodeConfigPath))
	if strings.Contains(string(cli), "secret-key") {
		t.Fatalf("key leaked into config.json:\n%s", cli)
	}
	doc := readJSONFile(t, filepath.Join(home, zcodeConfigPath))
	if doc["features"].(map[string]any)["mcp"] != true || doc["mcp"].(map[string]any)["servers"].(map[string]any)["gw"] == nil {
		t.Fatalf("MCP configuration lost: %v", doc)
	}
	hook := doc["hooks"].(map[string]any)["events"].(map[string]any)["SessionStart"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if hook["type"] != "command" || hook["command"] != `rm -f "$ZCODE_PERSONAL_PROVIDER_CONFIG_FILE"` {
		t.Fatalf("hook: %v", hook)
	}

	// Existing hooks stay, and every call gets its own key file.
	existing := `{"hooks":{"events":{"SessionStart":[{"hooks":[{"type":"command","command":"echo hi"}]}],"Stop":[{"hooks":[]}]}},"other":1}`
	if err := os.WriteFile(filepath.Join(home, zcodeConfigPath), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, env2, err := ApplyAgentCLICredential(AgentCLIZCode, ProviderCredential{APIKey: "k"}, AgentCLIOptions{Model: "glm-5.3"}, home, "/w/home", argv); err != nil || env2[zcodePersonalFileEnv] == env[zcodePersonalFileEnv] {
		t.Fatalf("second call: %v %v", env2, err)
	}
	if len(zcodeKeyFiles(t, home)) != 2 {
		t.Fatalf("key files: %v", zcodeKeyFiles(t, home))
	}
	doc = readJSONFile(t, filepath.Join(home, zcodeConfigPath))
	events := doc["hooks"].(map[string]any)["events"].(map[string]any)
	if starts := events["SessionStart"].([]any); len(starts) != 2 || events["Stop"] == nil || doc["other"] != float64(1) {
		t.Fatalf("merged config: %v", doc)
	}
}

func TestApplyZCodeCredentialEffort(t *testing.T) {
	for _, tc := range []struct {
		effort string
		want   any
	}{{"", nil}, {"high", map[string]any{"reasoningLevel": "high"}}} {
		home := t.TempDir()
		if _, _, err := ApplyAgentCLICredential(AgentCLIZCode, ProviderCredential{APIKey: "k"}, AgentCLIOptions{Model: "glm-5.3", Effort: tc.effort}, home, home, []string{"zcode"}); err != nil {
			t.Fatal(err)
		}
		selection := readJSONFile(t, zcodeKeyFiles(t, home)[0])["config"].(map[string]any)["defaultModelSelection"].(map[string]any)
		if got := selection["options"]; !reflect.DeepEqual(got, tc.want) || selection["modelId"] != "glm-5.3" {
			t.Errorf("effort %q: selection %v", tc.effort, selection)
		}
	}
}

func TestApplyZCodeCredentialProtocols(t *testing.T) {
	for _, tc := range []struct{ baseURL, protocol, wantURL, wantType string }{
		{"", "chat", zcodeDefaultBaseURL, "anthropic-messages"},
		{"https://gw.example/v1/", "chat", "https://gw.example/v1", "openai-chat-completions"},
		{"https://gw.example/v1", "responses", "https://gw.example/v1", "openai-responses"},
		{"https://gw.example", "messages", "https://gw.example", "anthropic-messages"},
		{"https://gw.example", "", "https://gw.example", "anthropic-messages"},
	} {
		home := t.TempDir()
		if _, _, err := ApplyAgentCLICredential(AgentCLIZCode, ProviderCredential{APIKey: "k", BaseURL: tc.baseURL, Protocol: tc.protocol}, AgentCLIOptions{Model: "m"}, home, home, []string{"zcode"}); err != nil {
			t.Fatal(err)
		}
		rules := readJSONFile(t, zcodeKeyFiles(t, home)[0])["config"].(map[string]any)["providerConfigRules"].(map[string]any)["providerRules"].([]any)
		api := rules[0].(map[string]any)["config"].(map[string]any)["api"].(map[string]any)
		if api["baseUrl"] != tc.wantURL || api["type"] != tc.wantType {
			t.Errorf("%+v: got %v", tc, api)
		}
	}
	// Chat's "CLI default" arrives as "default"; the default endpoint maps it
	// to BigModel's coding model, a custom endpoint must name one.
	for _, model := range []string{"", "default"} {
		home := t.TempDir()
		if _, _, err := ApplyAgentCLICredential(AgentCLIZCode, ProviderCredential{APIKey: "k"}, AgentCLIOptions{Model: model}, home, home, []string{"zcode"}); err != nil {
			t.Fatal(err)
		}
		selection := readJSONFile(t, zcodeKeyFiles(t, home)[0])["config"].(map[string]any)["defaultModelSelection"].(map[string]any)
		if selection["modelId"] != zcodeDefaultModel {
			t.Errorf("model %q: selection %v", model, selection)
		}
		if _, _, err := ApplyAgentCLICredential(AgentCLIZCode, ProviderCredential{APIKey: "k", BaseURL: "https://gw.example"}, AgentCLIOptions{Model: model}, t.TempDir(), "/h", nil); err == nil || !strings.Contains(err.Error(), "模型") {
			t.Fatalf("custom endpoint without model %q: %v", model, err)
		}
	}
	if _, _, err := ApplyAgentCLICredential(AgentCLIZCode, ProviderCredential{}, AgentCLIOptions{Model: "m"}, t.TempDir(), "/h", nil); err == nil || !strings.Contains(err.Error(), "ZCode API Key") {
		t.Fatalf("zcode without key: %v", err)
	}
}

func TestWriteAgentCLIMCPConfigZCode(t *testing.T) {
	home := t.TempDir()
	entries := map[string]any{
		"web":   map[string]any{"type": "http", "url": "https://m.example/mcp", "headers": map[string]any{"Authorization": "Bearer t"}},
		"bare":  map[string]any{"type": "http", "url": "https://m.example/bare", "headers": map[string]string(nil)},
		"local": map[string]any{"command": "node", "args": []string(nil), "cwd": "", "env": map[string]string{}},
		"full":  map[string]any{"command": "npx", "args": []string{"-y", "x"}, "cwd": "/w", "env": map[string]string{"A": "1"}},
	}
	if err := WriteAgentCLIMCPConfig(AgentCLIZCode, home, entries, AgentCLIMCPOptions{ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	doc := readJSONFile(t, filepath.Join(home, zcodeConfigPath))
	servers := doc["mcp"].(map[string]any)["servers"].(map[string]any)
	if doc["features"].(map[string]any)["mcp"] != true || len(servers) != 4 {
		t.Fatalf("document: %v", doc)
	}
	// ZCode drops a server with an unknown or null field.
	keys := func(name string) []string {
		var out []string
		for key := range servers[name].(map[string]any) {
			out = append(out, key)
		}
		slices.Sort(out)
		return out
	}
	for name, want := range map[string][]string{
		"web":   {"headers", "type", "url"},
		"bare":  {"type", "url"},
		"local": {"command", "enabled", "timeoutMs", "type"},
		"full":  {"args", "command", "cwd", "enabled", "env", "timeoutMs", "type"},
	} {
		if got := keys(name); !slices.Equal(got, want) {
			t.Errorf("%s keys %v, want %v", name, got, want)
		}
	}
	if full := servers["full"].(map[string]any); full["type"] != "stdio" || full["timeoutMs"] != float64(600000) || full["enabled"] != true {
		t.Errorf("stdio server: %v", full)
	}
}

// The wrapper is the last line of defence: a run that exits before the
// SessionStart hook still removes its key file and keeps its exit code.
func TestZCodeCleanupScriptRemovesKeyFile(t *testing.T) {
	home := t.TempDir()
	fake := filepath.Join(t.TempDir(), "zcode")
	// Recreates the empty file ZCode writes when its personal config is gone.
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\"\nprintf 'base=%s builtin=%s data=%s\\n' \"${ZCODE_BASE_URL-unset}\" \"${ZCODE_BUILTIN_PROVIDER_CONFIG_FILE-unset}\" \"$ZCODE_DATA_BASE_DIR\"\nrm -f \"$ZCODE_PERSONAL_PROVIDER_CONFIG_FILE\"\n: > \"$ZCODE_PERSONAL_PROVIDER_CONFIG_FILE\"\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	argv, env, err := ApplyAgentCLICredential(AgentCLIZCode, ProviderCredential{APIKey: "k"}, AgentCLIOptions{Model: "m"}, home, home, []string{fake, "--prompt", "it's \"q\" $HOME"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "ZCODE_BASE_URL=https://host.example", "ZCODE_BUILTIN_PROVIDER_CONFIG_FILE=")
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	out, err := cmd.Output()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("exit: %v", err)
	}
	// Host ZCODE_* settings are unset, not just emptied: ZCode fails on an empty path.
	if want := "--prompt it's \"q\" $HOME\nbase=unset builtin=unset data=" + home + "\n"; string(out) != want {
		t.Fatalf("child saw %q, want %q", out, want)
	}
	if files := zcodeKeyFiles(t, home); len(files) != 0 {
		t.Fatalf("key file left behind: %v", files)
	}
	if _, err := os.Stat(env[zcodePersonalFileEnv]); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("recreated shell left behind: %v", err)
	}
}

// Both execution paths wrap the CLI with the cleanup script and then add the
// MCP preamble inside its shell string, so the final argv is the same.
func TestZCodeBindCommandComposition(t *testing.T) {
	cli := []string{"zcode", "--prompt", "p"}
	check := func(t *testing.T, argv []string, preamble string) {
		t.Helper()
		if len(argv) != 4+len(cli) || argv[0] != "sh" || argv[1] != "-c" || !slices.Equal(argv[4:], cli) {
			t.Fatalf("argv: %q", argv)
		}
		if !strings.HasPrefix(argv[2], preamble) || !strings.HasSuffix(argv[2], zcodeCleanupScript) {
			t.Fatalf("script: %q", argv[2])
		}
	}
	for _, tc := range []struct{ driver, preamble string }{
		{"seatbelt", isolatedMCPCallPreamble},
		{"local", localMCPCallPreamble},
	} {
		t.Run(tc.driver, func(t *testing.T) {
			workspace := t.TempDir()
			sb := &localSandbox{workspace: workspace, driver: tc.driver, environment: map[string]string{}}
			command := AgentCLICommand(AgentCLIZCode, AgentCLICommandOptions{Model: "m", Prompt: "p"})
			command.CredentialBindings = []CredentialBinding{BindAgentCLI, BindGit, BindMCP}
			command.PrivateHome = true
			command.PrivateHomeKey = "zcode-a"
			bound, err := bindCommand(sb, workspace, command, RunCredentials{AgentCLI: map[string]ProviderCredential{AgentCLIZCode: {APIKey: "k"}}})
			if err != nil {
				t.Fatal(err)
			}
			if len(bound.Argv) < 4 || bound.Argv[0] != "sh" || !strings.HasPrefix(bound.Argv[2], tc.preamble) || !strings.HasSuffix(bound.Argv[2], zcodeCleanupScript) || bound.Argv[4] != "zcode" {
				t.Fatalf("control plane argv: %q", bound.Argv)
			}
			home := filepath.Join(workspace, ".agentflow", "private-home", "zcode-a")
			if want := home + "/.zcode/v2/"; !strings.HasPrefix(bound.environment[zcodePersonalFileEnv], want) || bound.environment["HOME"] != home || bound.environment["ZCODE_DATA_BASE_DIR"] != home {
				t.Fatalf("control plane env: %v", bound.environment)
			}
			// Host ZCode settings never reach the child.
			if bound.environment["ZCODE_API_KEY"] != "" || bound.environment["ZCODE_BASE_URL"] != "" || bound.environment["ZCODE_STORAGE_DIR"] != "" {
				t.Fatalf("host ZCODE_* not cleared: %v", bound.environment)
			}

			// Runner: BindAgentCLICommand wraps first, BindMCPCallArgv follows.
			runnerArgv, runnerEnv, err := BindAgentCLICommand(sb, workspace, command, ProviderCredential{APIKey: "k"}, cli)
			if err != nil {
				t.Fatal(err)
			}
			if runnerEnv["ZCODE_API_KEY"] != "" || runnerEnv["HOME"] != home || runnerEnv["USERPROFILE"] != home || runnerEnv[zcodePersonalFileEnv] == "" {
				t.Fatalf("runner env: %v", runnerEnv)
			}
			check(t, BindMCPCallArgv(sb, runnerArgv), tc.preamble)
		})
	}

	// Docker leaves the shell string alone; the process still sees the key
	// file at its in-container path.
	workspace := t.TempDir()
	sb := &localSandbox{workspace: workspace, driver: "docker", environment: map[string]string{}}
	command := AgentCLICommand(AgentCLIZCode, AgentCLICommandOptions{Model: "m", Prompt: "p"})
	command.PrivateHome, command.PrivateHomeKey = true, "zcode-a"
	argv, env, err := BindAgentCLICommand(sb, workspace, command, ProviderCredential{APIKey: "k"}, cli)
	if err != nil {
		t.Fatal(err)
	}
	if got := BindMCPCallArgv(sb, argv); !slices.Equal(got, argv) || got[2] != zcodeCleanupScript {
		t.Fatalf("docker argv: %q", got)
	}
	if !strings.HasPrefix(env[zcodePersonalFileEnv], "/workspace/.agentflow/private-home/zcode-a/.zcode/v2/provider_config.") {
		t.Fatalf("docker env: %v", env)
	}
	if files := zcodeKeyFiles(t, filepath.Join(workspace, ".agentflow", "private-home", "zcode-a")); len(files) != 1 {
		t.Fatalf("host key files: %v", files)
	}
}

func replayZCode(t *testing.T, name string) []map[string]any {
	t.Helper()
	file, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	translate := NewAgentCLITranslator(AgentCLIZCode)
	var out []map[string]any
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		for _, line := range translate(scanner.Text()) {
			var event map[string]any
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				t.Fatal(err)
			}
			out = append(out, event)
		}
	}
	return out
}

func TestZCodeTranslatorSuccessSample(t *testing.T) {
	events := replayZCode(t, "zcode-0.17.1-stream-sample.ndjson")
	var thinking, text, toolInput, toolResult string
	var toolError any
	deltas, toolCalls := "", 0
	for _, event := range events {
		if event["session_id"] != "sess_27fb868f-6a9f-4930-948e-eaf76f370523" {
			t.Fatalf("event without session: %v", event)
		}
		switch event["type"] {
		case "stream_event":
			if delta, _ := event["event"].(map[string]any)["delta"].(map[string]any); delta != nil {
				part, _ := delta["text"].(string)
				if part == "" {
					part, _ = delta["thinking"].(string)
				}
				deltas += part
			}
		case "assistant":
			block := event["message"].(map[string]any)["content"].([]any)[0].(map[string]any)
			switch block["type"] {
			case "thinking":
				thinking = block["thinking"].(string)
			case "text":
				text = block["text"].(string)
			case "tool_use":
				toolCalls++
				data, _ := json.Marshal(block["input"])
				toolInput = block["id"].(string) + " " + block["name"].(string) + " " + string(data)
			}
		case "user":
			block := event["message"].(map[string]any)["content"].([]any)[0].(map[string]any)
			toolResult, toolError = block["tool_use_id"].(string)+" "+block["content"].(string), block["is_error"]
		}
	}
	if events[0]["type"] != "system" || events[0]["subtype"] != "init" {
		t.Fatalf("first event: %v", events[0])
	}
	if thinking != "plan: write file" || text != "done" || deltas != "plan: write filedone" {
		t.Errorf("thinking=%q text=%q deltas=%q", thinking, text, deltas)
	}
	if toolInput != `call_1 Write {"content":"hi\n","file_path":"hello.txt"}` {
		t.Errorf("tool call: %s", toolInput)
	}
	if !strings.HasPrefix(toolResult, "call_1 File created successfully") || toolError != false {
		t.Errorf("tool result: %s (error=%v)", toolResult, toolError)
	}
	last := events[len(events)-1]
	usage := last["usage"].(map[string]any)
	if last["type"] != "result" || last["is_error"] != false || last["result"] != "done" ||
		usage["input_tokens"] != float64(200) || usage["output_tokens"] != float64(20) || usage["cache_read_input_tokens"] != float64(0) {
		t.Errorf("result: %v", last)
	}
	// The stream repeats the call in several events; it is reported once.
	if toolCalls != 1 {
		t.Errorf("tool calls: %d", toolCalls)
	}
}

func TestZCodeTranslatorUnauthorizedSample(t *testing.T) {
	events := replayZCode(t, "zcode-0.17.1-stream-401-sample.ndjson")
	last := events[len(events)-1]
	if last["type"] != "result" || last["is_error"] != true || last["result"] != "Invalid API key (HTTP 401)" {
		t.Fatalf("result: %v", last)
	}
	if len(events) != 2 || events[0]["subtype"] != "init" {
		t.Fatalf("events: %v", events)
	}
}

func TestZCodeTranslatorFailureDetails(t *testing.T) {
	translate := NewAgentCLITranslator(AgentCLIZCode)
	lines := translate(`{"type":"turn.failed","sessionId":"sess_1","payload":{"error":{"message":"overloaded","attribution":{"statusCode":529}}}}`)
	last := lines[len(lines)-1]
	if !strings.Contains(last, `"result":"overloaded"`) || !strings.Contains(last, `"is_error":true`) {
		t.Fatalf("failure: %v", lines)
	}
	if lines := NewAgentCLITranslator(AgentCLIZCode)(`{"type":"turn.failed","payload":{"error":{}}}`); !strings.Contains(lines[len(lines)-1], "ZCode 本轮执行失败") {
		t.Fatalf("empty failure: %v", lines)
	}
	if lines := translate("not json"); lines != nil {
		t.Fatalf("garbage: %v", lines)
	}
}
