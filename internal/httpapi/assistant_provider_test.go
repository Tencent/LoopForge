package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAssistantGenerationWithOnlySelectedProviderCredentials(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			h, _ := workbenchFixture(t)
			user := domain.User{LoginName: provider + "-owner"}
			if provider == "codex" {
				user.CodexAPIKey = "fixture-codex"
			} else {
				user.ClaudeCodeAPIKey = "fixture-claude"
			}
			if err := h.repo.PutUser(user); err != nil {
				t.Fatal(err)
			}
			user, _ = h.repo.UserByLoginName(user.LoginName)
			if err := h.repo.PutProject(domain.Project{ID: "workbench", Name: "Workbench", OwnerID: user.ID}); err != nil {
				t.Fatal(err)
			}
			h.generate = func(_ context.Context, _ string, actor domain.User) (string, error) {
				if actor.ID != user.ID || actor.CodeBuddyAPIKey != "" {
					return "", fmt.Errorf("wrong actor credentials")
				}
				return `{"message":"provider reply","drafts":[]}`, nil
			}
			c, response := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBufferString(`{"projectId":"workbench","sessionId":"provider-test","executor":"`+provider+`","model":"custom-model","messages":[{"role":"user","content":"Hello"}]}`))
			h.startGeneration(c)
			if response.Code != 202 {
				t.Fatalf("start: %d %s", response.Code, response.Body.String())
			}
			wantEngine, wantModel := "codex", "codex:custom-model"
			if provider == "claude" {
				wantEngine, wantModel = "claude-code", "claude-code:custom-model"
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				generation, _ := h.repo.AssistantGeneration(user.ID, "provider-test")
				if generation.Status != "running" {
					if generation.Status != "completed" {
						t.Fatalf("generation failed: %s", generation.Error)
					}
					session, _ := h.repo.AssistantSession(user.ID, "provider-test")
					if session.Executor != wantEngine || session.Model != wantModel || len(session.Messages) != 2 {
						t.Fatalf("provider conversation lost: executor=%s model=%s messages=%d", session.Executor, session.Model, len(session.Messages))
					}
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("generation never completed")
		})
	}
}

func TestAssistantProviderCredentialBoundary(t *testing.T) {
	for _, provider := range []string{"codebuddy", "codex", "claude"} {
		req := assistantRequest{Executor: provider}
		if validateAssistantProvider(&req, domain.User{}) == nil {
			t.Fatalf("%s accepted missing credentials", provider)
		}
	}
	u := domain.User{CodexAPIKey: "fixture", ClaudeCodeAPIKey: "fixture"}
	for _, provider := range []string{"codex", "claude"} {
		req := assistantRequest{Executor: provider, Model: "custom-model"}
		if err := validateAssistantProvider(&req, u); err != nil {
			t.Fatal(err)
		}
		if req.Model != "custom-model" {
			t.Fatal("custom model lost")
		}
	}
	if validateAssistantProvider(&assistantRequest{Executor: "shell"}, u) == nil {
		t.Fatal("unknown provider accepted")
	}
}

func TestAssistantCodexGuard(t *testing.T) {
	for _, name := range []string{"mcp__agentflow_session__submit_assistant_reply", "shell_command", "apply_patch", "exec_command", "", "mcp__other__write"} {
		cmd := exec.Command("python3", "-c", assistantCodexGuard)
		body, _ := json.Marshal(map[string]string{"tool_name": name})
		cmd.Stdin = strings.NewReader(string(body))
		out, err := cmd.CombinedOutput()
		allowed := strings.HasPrefix(name, "mcp__agentflow_session__")
		if allowed && err != nil {
			t.Fatal(err)
		}
		if !allowed && (err == nil || !strings.Contains(string(out), `"permissionDecision": "deny"`)) {
			t.Fatalf("%s was not denied", name)
		}
	}
}

// Opt-in, zero-cost protocol integration. Real installed CLIs use the exact
// Main Bot command, private HOME, credentials, HTTP MCP and streamed results.
// This is NOT verification against paid OpenAI/Anthropic services.
func TestAssistantNativeProviderGateway(t *testing.T) {
	if os.Getenv("AGENTFLOW_TEST_NATIVE_PROVIDERS") != "1" {
		t.Skip("set AGENTFLOW_TEST_NATIVE_PROVIDERS=1 with both pinned CLIs installed")
	}
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			var mu sync.Mutex
			requests, calls := 0, 0
			deniedNative := false
			mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					w.WriteHeader(405)
					return
				}
				var req struct {
					ID     any    `json:"id"`
					Method string `json:"method"`
					Params struct {
						ProtocolVersion string `json:"protocolVersion"`
					} `json:"params"`
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					w.WriteHeader(400)
					return
				}
				if req.ID == nil {
					w.WriteHeader(202)
					return
				}
				var result any
				switch req.Method {
				case "initialize":
					result = map[string]any{"protocolVersion": req.Params.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "fixture", "version": "1"}}
				case "tools/list":
					result = map[string]any{"tools": []any{map[string]any{"name": "read_demo", "description": "Read fixture", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
				case "tools/call":
					mu.Lock()
					calls++
					mu.Unlock()
					result = map[string]any{"content": []any{map[string]string{"type": "text", "text": "TOOL_OK"}}}
				default:
					result = map[string]any{}
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			}))
			defer mcp.Close()
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "count_tokens") {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"input_tokens":10}`)
					return
				}
				if r.Method != "POST" {
					http.NotFound(w, r)
					return
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					http.Error(w, "body", 400)
					return
				}
				key := r.Header.Get("Authorization")
				if provider == "claude" {
					key = r.Header.Get("X-Api-Key")
				}
				expected := "Bearer fixture-provider-key"
				if provider == "claude" {
					expected = "fixture-provider-key"
				}
				if key != expected || body["model"] != "gateway-test-model" {
					t.Error("wrong credential or model")
				}
				if input, ok := body["input"].([]any); ok {
					for _, value := range input {
						if item, ok := value.(map[string]any); ok && item["type"] == "function_call_output" {

							if strings.Contains(fmt.Sprint(item["output"]), "native tools are disabled") || strings.Contains(fmt.Sprint(item["output"]), "unsupported call: view_image") {
								deniedNative = true
							}
						}
					}
				}
				mu.Lock()
				requests++
				first := requests == 1
				callMCP := first || (provider == "codex" && requests == 2)
				mu.Unlock()
				tools, _ := json.Marshal(body["tools"])
				if provider == "codex" {
					for _, name := range []string{"view_image", "shell_command", "exec_command", "apply_patch", "spawn_agent", "create_goal"} {
						if strings.Contains(string(tools), `"name":"`+name+`"`) {
							t.Errorf("native tool exposed: %s", name)
						}
					}
				}

				if !strings.Contains(string(tools), "read_demo") {
					t.Error("session MCP tool not available")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				emit := func(event string, data any) {
					raw, _ := json.Marshal(data)
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
				}
				answer := `{"message":"GATEWAY_OK","drafts":[]}`
				if provider == "codex" {
					item := map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": answer, "annotations": []any{}}}}
					if callMCP {
						item = map[string]any{"id": "fc_fixture", "type": "function_call", "call_id": "call_fixture", "name": "read_demo", "namespace": "mcp__agentflow_session", "arguments": "{}", "status": "completed"}
					}
					if first {
						item = map[string]any{"id": "fc_native", "type": "function_call", "call_id": "call_native", "name": "view_image", "arguments": `{"path":"/tmp/loopforge-no-native-read"}`, "status": "completed"}
					}
					emit("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_fixture", "status": "in_progress", "output": []any{}}})
					emit("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item})
					emit("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
					emit("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_fixture", "object": "response", "status": "completed", "model": "gateway-test-model", "output": []any{item}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 3, "total_tokens": 13}}})
				} else {
					emit("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "model": "gateway-test-model", "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 0}}})
					block := map[string]any{"type": "text", "text": ""}
					delta := map[string]string{"type": "text_delta", "text": answer}
					stop := "end_turn"
					if first {
						block = map[string]any{"type": "tool_use", "id": "call_fixture", "name": "mcp__agentflow_session__read_demo", "input": map[string]any{}}
						delta = map[string]string{"type": "input_json_delta", "partial_json": "{}"}
						stop = "tool_use"
					}
					emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": block})
					emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": delta})
					emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
					emit("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 3}})
					emit("message_stop", map[string]string{"type": "message_stop"})
				}
			}))
			defer gateway.Close()
			dir := t.TempDir()
			config := filepath.Join(dir, "mcp.json")
			raw, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"agentflow_session": map[string]string{"type": "http", "url": mcp.URL}}})
			os.WriteFile(config, raw, 0600)
			user := domain.User{CodexAPIKey: "fixture-provider-key", CodexBaseURL: gateway.URL + "/v1", ClaudeCodeAPIKey: "fixture-provider-key", ClaudeCodeBaseURL: gateway.URL}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			var out string
			var err error
			if provider == "codex" {
				out, _, err = runAssistantCLI(ctx, assistantCLIRequest{Executor: provider, Model: "gateway-test-model", Prompt: "Read demo then reply JSON.", MCPConfig: config, Native: true, SessionRoot: dir}, user, nil)
			} else {
				out, _, err = runAlternateAssistantCLI(ctx, assistantCLIRequest{Executor: provider, Model: "gateway-test-model", Prompt: "Read demo then reply JSON.", MCPConfig: config}, user, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if provider == "codex" && !deniedNative {
				t.Fatal("native tool did not pass through the denial hook")
			}
			if calls != 1 || requests < 2 || !strings.Contains(out, "GATEWAY_OK") {
				t.Fatalf("MCP/stream mismatch: requests=%d calls=%d output=%q", requests, calls, out)
			}
		})
	}
}

func TestAssistantConversationEngineKeepsLegacyExecutor(t *testing.T) {
	reply := []assistantMessage{{Role: "user", Content: "hi"}, {Role: "assistant", Model: "gpt-5.4", Content: "ok"}}
	if got := assistantConversationEngine(reply, "codex"); got != assistantEngineCodex {
		t.Fatalf("codex legacy = %q", got)
	}
	if got := assistantConversationEngine(reply, "claude"); got != assistantEngineClaude {
		t.Fatalf("claude legacy = %q", got)
	}
	if got := assistantConversationEngine(reply, "agy"); got != "agy" {
		t.Fatalf("agy legacy = %q", got)
	}
	if got := assistantConversationEngine(reply); got != assistantEngineCodeBuddy {
		t.Fatalf("unprefixed = %q", got)
	}
	if got := assistantConversationEngine(reply, "codebuddy"); got != assistantEngineCodeBuddy {
		t.Fatalf("codebuddy legacy = %q", got)
	}
	prefixed := []assistantMessage{{Role: "assistant", Model: "codex:gpt-5.4", Content: "ok"}}
	if got := assistantConversationEngine(prefixed, ""); got != assistantEngineCodex {
		t.Fatalf("prefix wins = %q", got)
	}
	role := []assistantMessage{{Role: "assistant", Model: "gpt-5.4", SpeakerAgentID: "role", Content: "ok"}}
	if got := assistantConversationEngine(role, "codex"); got != "" {
		t.Fatalf("role reply locked = %q", got)
	}
	if refusal := assistantEngineSwitchRefusal(assistantConversationEngine(reply, "codex"), "codex:gpt-5.4"); refusal != "" {
		t.Fatal(refusal)
	}
	if refusal := assistantEngineSwitchRefusal(assistantConversationEngine(reply, "codex"), "default"); refusal == "" {
		t.Fatal("expected refusal when a legacy codex session continues on CodeBuddy")
	}
}

func TestAssistantCodexAppServerDisablesNativeTools(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(config, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	inv, err := prepareAssistantCodexInvocation(assistantEngineInvocationOpts{Bin: "codex", Root: dir, Config: config, Prompt: "hi", Model: "gpt", SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := inv.command.Stdin.(interface{ Close() error }); ok {
		closer.Close()
	}
	joined := strings.Join(inv.command.Argv, "\n")
	for _, feature := range []string{"view_image", "goals", "shell_tool", "unified_exec"} {
		if !strings.Contains(joined, "features."+feature+"=false") {
			t.Fatalf("missing features.%s=false:\n%s", feature, joined)
		}
	}
	if !strings.Contains(joined, "features.hooks=true") || !strings.Contains(joined, "hooks.bypass_hook_trust=true") || !strings.Contains(joined, "hooks.PreToolUse=") {
		t.Fatalf("guard hook missing:\n%s", joined)
	}
	if strings.Contains(joined, "--dangerously-bypass-hook-trust") {
		t.Fatalf("app-server argv must not use the exec-only trust flag:\n%s", joined)
	}
	guard, err := os.ReadFile(filepath.Join(dir, "codex-native-guard.py"))
	if err != nil || !strings.Contains(string(guard), "native tools are disabled") {
		t.Fatalf("guard script = %q err=%v", guard, err)
	}
}

// The "CLI default" catalog entry is a placeholder; a non-CodeBuddy CLI must
// keep its own model instead of receiving the literal "default" (#461).
func TestAssistantCLIModelArgDropsDefaultPlaceholder(t *testing.T) {
	engine, model := assistantModelEngine(assistantRequestModel("dsh", assistantDefaultModel))
	if engine != "dsh" || model != assistantDefaultModel {
		t.Fatalf("dsh default = %q %q", engine, model)
	}
	for in, want := range map[string]string{"default": "", " Default ": "", "": "", "deepseek-flash": "deepseek-flash", " gpt-5.5 ": "gpt-5.5"} {
		if got := assistantCLIModelArg(in); got != want {
			t.Fatalf("assistantCLIModelArg(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAssistantClaudeOmitsEmptyModel(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "mcp.json")
	if err := os.WriteFile(config, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	inv, err := prepareAssistantClaudeInvocation(assistantEngineInvocationOpts{Bin: "claude", Root: root, Config: config, Prompt: "p", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range inv.command.Argv {
		if arg == "--model" {
			t.Fatalf("Claude argv names a model without one: %q", inv.command.Argv)
		}
	}
	inv, err = prepareAssistantClaudeInvocation(assistantEngineInvocationOpts{Bin: "claude", Root: root, Config: config, Prompt: "p", Model: "claude-haiku-5.5", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if argv := strings.Join(inv.command.Argv, "\x00"); !strings.Contains(argv, "\x00--model\x00claude-haiku-5.5") {
		t.Fatalf("Claude argv lost its model: %q", inv.command.Argv)
	}
}

func TestAssistantCodexThreadOmitsEmptyModel(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  any
	}{{"", nil}, {"gpt-5.5", "gpt-5.5"}} {
		dir := t.TempDir()
		config := filepath.Join(dir, "mcp.json")
		if err := os.WriteFile(config, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		inv, err := prepareAssistantCodexInvocation(assistantEngineInvocationOpts{Bin: "codex", Root: dir, Config: config, Prompt: "hi", Model: tc.model, SessionID: "s1"})
		if err != nil {
			t.Fatal(err)
		}
		inv.translate(`{"id":1,"result":{}}`)
		// Every request is already in the pipe; one read drains it.
		buf := make([]byte, 1<<16)
		n, err := inv.command.Stdin.(*os.File).Read(buf)
		inv.finish(false)
		if err != nil {
			t.Fatal(err)
		}
		data := buf[:n]
		var start map[string]any
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var message struct {
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if json.Unmarshal([]byte(line), &message) == nil && message.Method == "thread/start" {
				start = message.Params
			}
		}
		if start == nil {
			t.Fatalf("no thread/start request in %q", data)
		}
		if got, ok := start["model"]; tc.want == nil && ok || tc.want != nil && got != tc.want {
			t.Fatalf("model %q: thread/start params = %v", tc.model, start)
		}
	}
}
