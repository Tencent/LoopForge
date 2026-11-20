package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func TestAtomCodeConfigAndChatPolicy(t *testing.T) {
	root := t.TempDir()
	home := AgentCLIHome{Base: root, Rel: "home"}
	command := AgentCLICommand(AgentCLIAtomCode, AgentCLICommandOptions{Chat: true, Prompt: "-test", Resume: "test-session", Model: "test-model"})
	if slices.Contains(command.Argv, "--dangerously-skip-permissions") || !slices.Contains(command.Argv, "--prompt=-test") || !slices.Contains(command.Argv, "test-session") {
		t.Fatalf("invalid headless chat command: %q", command.Argv)
	}
	cred := ProviderCredential{APIKey: "private-test-value", BaseURL: "https://example.invalid", Protocol: AgentCLIProtocolMessages}
	_, env, err := ApplyAgentCLICredential(AgentCLIAtomCode, cred, command.AgentCLI, home, home.Path(), command.Argv)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home.Path(), atomCodeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Providers map[string]struct {
			Type string `toml:"type"`
			Key  string `toml:"api_key"`
		} `toml:"providers"`
		Permissions struct{ Deny []string } `toml:"permissions"`
	}
	if err := toml.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	provider := config.Providers["loopforge"]
	if provider.Type != "claude" || provider.Key != "$"+agentCLIKeyEnv ||
		env[agentCLIKeyEnv] != cred.APIKey || env["ATOMCODE_SUBAGENT"] != "0" ||
		!slices.Contains(config.Permissions.Deny, "bash") || !slices.Contains(config.Permissions.Deny, "team") {
		t.Fatal("unexpected protocol, credential binding or tool policy")
	}
	if err := WriteAgentCLIMCPConfig(AgentCLIAtomCode, home, map[string]any{
		"tools": map[string]any{"url": "https://example.invalid/mcp"},
	}, AgentCLIMCPOptions{}); err != nil {
		t.Fatal(err)
	}
	path := "home/" + atomCodeMCPPath
	if allowed, rewrite := ConversationAsset(path); !allowed || !rewrite {
		t.Fatal("AtomCode MCP settings must be synced and remapped for remote workspaces")
	}
	if err := os.WriteFile(filepath.Join(root, ".hooks.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := BindAgentCLICommand(nil, root, command, cred, command.Argv); err == nil || !strings.Contains(err.Error(), ".hooks.json") {
		t.Fatalf("expected project hook rejection, got %v", err)
	}
}

func TestAtomCodeConversationGuards(t *testing.T) {
	root := t.TempDir()
	command := AgentCLICommand(AgentCLIAtomCode, AgentCLICommandOptions{Chat: true, Workspace: true})
	if !slices.Contains(command.Argv, "--require-approval-hook") {
		t.Fatal("workspace chat must require a binary with strict approval support")
	}
	if _, err := InstallConversationApproval(root, AgentCLIAtomCode, command); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "home", atomCodeHooksPath))
	if err != nil || !strings.Contains(string(raw), `"required_approval":true`) {
		t.Fatalf("missing required approval policy: %s %v", raw, err)
	}
	mounts, err := conversationGuardMounts(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{atomCodeHooksPath, atomCodeConfigPath, atomCodeMCPPath} {
		path := filepath.Join(root, "home", rel)
		if !slices.Contains(mounts, path+":"+path+":ro") {
			t.Errorf("missing read-only mount for %s", rel)
		}
	}
}

func TestAtomCodeRejectsLegacyHeadlessFlags(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "atomcode")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n[ \"$*\" = '--output-format jsonl --no-review --version' ]\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !AtomCodeHeadlessAvailable(binary) {
		t.Fatal("compatible CLI was rejected")
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n[ \"$*\" = '--version' ]\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if AtomCodeHeadlessAvailable(binary) {
		t.Fatal("legacy CLI was accepted")
	}
}

func TestAtomCodeNativeResolutionFallsBackFromLegacyCLI(t *testing.T) {
	binDir, cache := t.TempDir(), t.TempDir()
	t.Setenv("PATH", binDir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTFLOW_ATOMCODE_BIN", "")
	t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", "")
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", cache)

	if err := os.WriteFile(filepath.Join(binDir, "atomcode"), []byte("#!/bin/sh\nexit 2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cache, "atomcode", "current", "node_modules", ".bin", "atomcode")
	if err := os.MkdirAll(filepath.Dir(want), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := resolveNativeConversationBinary(AgentCLIAtomCode)
	if err != nil || path != want {
		t.Fatalf("expected cached AtomCode %s, got %s: %v", want, path, err)
	}
}

func TestAtomCodeResumeStaysInOriginalWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "first")
	command := AgentCLICommand(AgentCLIAtomCode, AgentCLICommandOptions{Chat: true, Workspace: true})
	command.WorkingDir = filepath.Join(root, "ws")
	if err := bindAtomCodeConversationWorkspace(root, workspace, command); err != nil {
		t.Fatal(err)
	}
	command.Argv = append(command.Argv, "--resume", "session-id")
	if err := bindAtomCodeConversationWorkspace(root, workspace, command); err != nil {
		t.Fatal(err)
	}
	if err := bindAtomCodeConversationWorkspace(root, filepath.Join(root, "second"), command); err == nil || !strings.Contains(err.Error(), "No conversation found with session ID: session-id") {
		t.Fatalf("cross-workspace resume should require a new CLI session, got %v", err)
	}
}

func TestAtomCodeTranslator(t *testing.T) {
	translate := NewAgentCLITranslator(AgentCLIAtomCode)
	lines := []string{
		`{"type":"run.started","session_id":"atom-session","model":"test"}`,
		`{"type":"reasoning.delta","text":"think"}`,
		`{"type":"tool.started","id":"call","name":"bash","arguments":"{\"command\":\"pwd\"}"}`,
		`{"type":"tool.completed","id":"call","content":"work","is_error":false}`,
		`{"type":"message.delta","text":"hel"}`,
		`{"type":"message.delta","text":"lo"}`,
		`{"type":"turn.completed","exit_code":0,"prompt_tokens":24,"completion_tokens":7,"cached_tokens":2}`,
	}
	var translated []string
	for _, line := range lines {
		translated = append(translated, translate(line)...)
	}
	out := strings.Join(translated, "\n")
	for _, want := range []string{`"session_id":"atom-session"`, `"thinking":"think"`, `"command":"pwd"`, `"content":"work"`, `"result":"hello"`, `"output_tokens":7`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
	translate = NewAgentCLITranslator(AgentCLIAtomCode)
	translate(`{"type":"error","message":"unauthorized","http_status":401}`)
	failure := strings.Join(translate(`{"type":"turn.completed","exit_code":1}`), "\n")
	if !strings.Contains(failure, `"is_error":true`) || !strings.Contains(failure, "HTTP 401") {
		t.Fatalf("missing authentication failure: %s", failure)
	}
}

// Uses the released CLI with a local model endpoint and the platform's real
// approval hook. It requires no external model service or credential.
func TestAtomCodeBinaryConversation(t *testing.T) {
	binary := os.Getenv("LOOPFORGE_ATOMCODE_TEST_BIN")
	if binary == "" {
		t.Skip("set LOOPFORGE_ATOMCODE_TEST_BIN to an AtomCode executable with --require-approval-hook")
	}
	var approvals atomic.Int32
	var mcpVisible atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/approval" {
			n := approvals.Add(1)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"allow":%t,"reason":"denied by LoopForge"}}`, n == 1)
			return
		}
		if r.URL.Path == "/mcp" && r.Method == http.MethodPost {
			var call struct {
				ID     json.RawMessage
				Method string
			}
			if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
				http.Error(w, "invalid RPC request", http.StatusBadRequest)
				return
			}
			var result any
			switch call.Method {
			case "initialize":
				result = map[string]any{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]string{"name": "loopforge-test", "version": "1"},
				}
			case "tools/list":
				result = map[string]any{"tools": []any{map[string]any{
					"name":        "probe",
					"inputSchema": map[string]string{"type": "object"},
				}}}
			default:
				w.WriteHeader(http.StatusAccepted)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": call.ID, "result": result})
			return
		}
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer local-test-key" {
			http.Error(w, "invalid endpoint or credential", http.StatusUnauthorized)
			return
		}
		var request struct {
			Tools    []json.RawMessage
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		for _, tool := range request.Tools {
			if strings.Contains(string(tool), "mcp__loopforge_test__probe") {
				mcpVisible.Store(true)
			}
		}
		delta := map[string]any{"role": "assistant", "content": "verified"}
		stop := "stop"
		name := ""
		if len(request.Tools) > 0 {
			for _, message := range slices.Backward(request.Messages) {
				content := string(message.Content)
				if message.Role == "user" && strings.Contains(content, "<system-reminder>") {
					continue
				}
				if message.Role == "user" {
					for _, state := range []string{"allowed", "blocked", "timeout", "unavailable"} {
						if strings.Contains(content, "try-"+state) {
							name = state
						}
					}
				}
				break
			}
		}
		if name != "" {
			args, _ := json.Marshal(map[string]string{"command": "touch " + name})
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
				"index": 0, "id": name, "type": "function",
				"function": map[string]any{"name": "bash", "arguments": string(args)},
			}}}
			stop = "tool_calls"
		}
		chunk, _ := json.Marshal(map[string]any{
			"id": "local", "model": "test-model", "object": "chat.completion.chunk",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": stop}},
			"usage":   map[string]int{"prompt_tokens": 12, "completion_tokens": 3, "total_tokens": 15},
		})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer server.Close()

	root, workspace := t.TempDir(), t.TempDir()
	home := AgentCLIHome{Base: root, Rel: "home"}
	err := WriteAgentCLIMCPConfig(AgentCLIAtomCode, home, map[string]any{
		"loopforge_test": map[string]any{"url": server.URL + "/mcp"},
	}, AgentCLIMCPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	session := ""
	for _, prompt := range []string{"hello", "try-allowed", "try-blocked", "try-timeout", "try-unavailable"} {
		command := AgentCLICommand(AgentCLIAtomCode, AgentCLICommandOptions{
			Binary: binary, Chat: true, Workspace: true, Prompt: prompt, Model: "test-model", Resume: session,
		})
		command, err := InstallConversationApproval(root, AgentCLIAtomCode, command)
		if err != nil {
			t.Fatal(err)
		}
		url := server.URL + "/approval"
		if prompt == "try-timeout" {
			raw := []byte(`{"hooks":{"loopforge-approval":{"event":"PreToolUse","command":"exec sleep 60","timeout_ms":10,"required_approval":true}}}`)
			if err := writeAgentCLIFile(home, atomCodeHooksPath, raw); err != nil {
				t.Fatal(err)
			}
		}
		if prompt == "try-unavailable" {
			url = server.URL + "/unavailable"
		}
		argv, env, err := ApplyAgentCLICredential(AgentCLIAtomCode, ProviderCredential{
			APIKey: "local-test-key", BaseURL: server.URL + "/v1",
		}, command.AgentCLI, home, home.Path(), command.Argv)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = workspace
		cmd.Env = append(os.Environ(), "HOME="+home.Path(), "AGENTFLOW_NATIVE_APPROVAL_URL="+url)
		for key, value := range env {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("run AtomCode: %v\n%s", err, out)
		}
		translate := NewAgentCLITranslator(AgentCLIAtomCode)
		started := ""
		toolCompleted := false
		for _, line := range strings.Split(string(out), "\n") {
			var event struct {
				Type   string `json:"type"`
				ID     string `json:"id"`
				Failed bool   `json:"is_error"`
			}
			if json.Unmarshal([]byte(line), &event) == nil &&
				event.Type == "tool.completed" && "try-"+event.ID == prompt {
				toolCompleted = true
				if event.Failed != (prompt != "try-allowed") {
					t.Fatalf("unexpected AtomCode tool outcome: %s", line)
				}
			}
			for _, translated := range translate(line) {
				var event map[string]any
				if json.Unmarshal([]byte(translated), &event) == nil && event["type"] == "system" {
					started = stringValue(event["session_id"])
				}
			}
		}
		if strings.HasPrefix(prompt, "try-") && !toolCompleted {
			t.Fatalf("missing tool result for %q\n%s", prompt, out)
		}
		if started == "" || (session != "" && started != session) {
			t.Fatalf("AtomCode did not keep its session: %q -> %q\n%s", session, started, out)
		}
		session = started
	}
	if !mcpVisible.Load() {
		t.Fatal("AtomCode did not expose the configured MCP tool")
	}
	if approvals.Load() != 2 {
		t.Fatalf("expected 2 approvals, received %d", approvals.Load())
	}
	if _, err := os.Stat(filepath.Join(workspace, "allowed")); err != nil {
		t.Fatalf("approved command did not run: %v", err)
	}
	for _, path := range []string{"blocked", "timeout", "unavailable"} {
		if _, err := os.Stat(filepath.Join(workspace, path)); !os.IsNotExist(err) {
			t.Fatalf("%s command was not blocked: %v", path, err)
		}
	}
}
