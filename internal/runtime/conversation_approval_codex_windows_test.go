package runtime

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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// Opt-in real Codex hook acceptance. The model and approval services are both
// loopback fixtures; the CLI receives only a fake gateway key and private HOME.
func TestWindowsNativeCodexApproval(t *testing.T) {
	binary := os.Getenv("LOOPFORGE_TEST_CODEX_BINARY")
	if binary == "" {
		t.Skip("set LOOPFORGE_TEST_CODEX_BINARY to an installed native Codex executable")
	}
	if !strings.EqualFold(filepath.Ext(binary), ".exe") {
		t.Fatal("Codex hook acceptance requires a native .exe")
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		allow   bool
		failure string
	}{
		{"deny", false, ""}, {"empty-denial-reason", false, ""}, {"allow", true, ""}, {"missing-script", true, "script"}, {"crashed-hook", true, "crash"}, {"invalid-hook-json", true, "json"}, {"missing-interpreter", true, "interpreter"},
		{"platform-native", false, ""}, {"platform-mcp", true, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			platformGuard := strings.HasPrefix(test.name, "platform-")
			root := filepath.Join(workspace, "native approval with spaces and ' quote")
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			pythonAlias := ""
			if test.failure == "interpreter" {
				python, err := conversationApprovalPythonExecutable(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				pythonAlias = filepath.Join(workspace, "python junction")
				junction := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), "/D", "/C", "mklink", "/J", pythonAlias, filepath.Dir(python))
				if output, err := junction.CombinedOutput(); err != nil {
					t.Fatalf("create interpreter fixture junction: %v %s", err, output)
				}
				t.Setenv("PATH", pythonAlias+string(os.PathListSeparator)+os.Getenv("PATH"))
			}
			marker := filepath.Join(workspace, "approved-tool-ran.txt")
			toolCommand := `powershell.exe -NoProfile -NonInteractive -Command "[IO.File]::WriteAllText('` + filepath.ToSlash(marker) + `','APPROVED')"`
			var mu sync.Mutex
			modelRequests, approvalRequests, mcpCalls := 0, 0, 0
			var failures []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/mcp" && r.Method == "POST" {
					var rpc struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
					}
					if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
						http.Error(w, "bad MCP", 400)
						return
					}
					if rpc.ID == nil {
						w.WriteHeader(http.StatusAccepted)
						return
					}
					var result any
					switch rpc.Method {
					case "initialize":
						result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "approval-fixture", "version": "1"}}
					case "tools/list":
						result = map[string]any{"tools": []any{map[string]any{"name": "fixture_tool", "description": "Local platform guard fixture", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
					case "tools/call":
						mu.Lock()
						mcpCalls++
						mu.Unlock()
						result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "MCP_POLICY_OK"}}}
					default:
						result = map[string]any{}
					}
					json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": result})
					return
				}
				if r.URL.Path == "/approval" {
					var rpc struct {
						Params struct {
							Name  string         `json:"name"`
							Input map[string]any `json:"input"`
						} `json:"params"`
					}
					err := json.NewDecoder(r.Body).Decode(&rpc)
					mu.Lock()
					approvalRequests++
					if err != nil || rpc.Params.Name != "Bash" || rpc.Params.Input["command"] != toolCommand {
						failures = append(failures, fmt.Sprintf("approval changed tool: %+v %v", rpc, err))
					}
					mu.Unlock()
					reason := "WINDOWS_FIXTURE_DECISION"
					if test.name == "empty-denial-reason" {
						reason = ""
					}
					json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"allow": test.allow, "reason": reason}})
					return
				}
				if r.Method != "POST" || r.URL.Path != "/v1/responses" {
					http.NotFound(w, r)
					return
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, "bad body", 400)
					return
				}
				mu.Lock()
				modelRequests++
				number := modelRequests
				if r.Header.Get("Authorization") != "Bearer local-hook-fixture-key" || body["model"] != "gateway-test-model" {
					failures = append(failures, "wrong model gateway key or model")
				}
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				emit := func(event string, data any) {
					raw, _ := json.Marshal(data)
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
				}
				var item map[string]any
				if number == 1 {
					toolName := ""
					for _, value := range body["tools"].([]any) {
						tool, _ := value.(map[string]any)
						name, _ := tool["name"].(string)
						if test.name == "platform-mcp" && name == "mcp__agentflow_session" {
							toolName = "fixture_tool"
							break
						}
						if test.name != "platform-mcp" && (name == "shell_command" || name == "exec_command") {
							toolName = name
							break
						}
					}
					if toolName == "" {
						mu.Lock()
						failures = append(failures, fmt.Sprintf("no shell tool in gateway request: %v", body["tools"]))
						mu.Unlock()
						http.Error(w, "shell tool missing", 400)
						return
					}
					arguments := map[string]any{"command": toolCommand, "workdir": workspace}
					if test.name == "platform-mcp" {
						arguments = map[string]any{}
					}
					if toolName == "exec_command" {
						arguments = map[string]any{"cmd": toolCommand, "workdir": workspace, "yield_time_ms": 1000}
					}
					raw, _ := json.Marshal(arguments)
					item = map[string]any{"id": "fc_fixture", "type": "function_call", "call_id": "call_fixture", "name": toolName, "arguments": string(raw), "status": "completed"}
					if test.name == "platform-mcp" {
						item["namespace"] = "mcp__agentflow_session"
					}
				} else {
					item = map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "HOOK_TEST_FINISHED", "annotations": []any{}}}}
				}
				emit("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": fmt.Sprintf("resp_%d", number), "status": "in_progress", "output": []any{}}})
				emit("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item})
				emit("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
				emit("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("resp_%d", number), "object": "response", "status": "completed", "model": "gateway-test-model", "output": []any{item}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 3, "total_tokens": 13}}})
			}))
			defer server.Close()
			args := []string{"exec", "--json", "--skip-git-repo-check", "--ephemeral", "--dangerously-bypass-hook-trust", "--sandbox", "danger-full-access", "-c", "features.plugins=false", "-c", "features.code_mode=false", "-c", "features.unified_exec=false"}
			if test.name == "platform-mcp" {
				args = append(args, "-c", `mcp_servers.agentflow_session.url="`+server.URL+`/mcp"`, "-c", `mcp_servers.agentflow_session.default_tools_approval_mode="approve"`)
			}
			ex := domain.ExecutorDefinition{Binary: binary, Args: args, Kind: "codex", Model: "gateway-test-model"}
			compiled := compiledConfig{AgentRoot: ".agentflow/test"}
			if err := os.MkdirAll(filepath.Join(workspace, ".agentflow", "test"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workspace, ".agentflow", "test", "agent-system.md"), []byte("Use the requested shell tool, then finish."), 0600); err != nil {
				t.Fatal(err)
			}
			command, err := providerForKind("codex").Prepare(providerInvocation{Executor: ex, Compiled: compiled, Prompt: "Execute the fixture shell tool once.", Driver: "local"})
			if err != nil {
				t.Fatal(err)
			}
			if platformGuard {
				guard := filepath.Join(root, "codex-native-guard.py")
				policy := []byte("import json,sys\nevent=json.load(sys.stdin)\nif not event.get('tool_name','').startswith('mcp__agentflow_session__'):\n    print(json.dumps({'hookSpecificOutput':{'hookEventName':'PreToolUse','permissionDecision':'deny','permissionDecisionReason':'Use platform MCP tools only.'}}))\n    sys.exit(2)\n")
				if err := os.WriteFile(guard, policy, 0600); err != nil {
					t.Fatal(err)
				}
				entry := `hooks.PreToolUse=[{hooks=[{type="command",command=` + strconv.Quote("python3 "+strconv.Quote(filepath.ToSlash(guard))) + `,timeout=5}]}]`
				command.Argv = append(command.Argv[:len(command.Argv)-1], "-c", "features.hooks=true", "-c", entry, command.Argv[len(command.Argv)-1])
				command, err = adaptPlatformConversationApproval(root, command)
				if err != nil {
					t.Fatal(err)
				}
				if current, err := os.ReadFile(guard); err != nil || !bytes.Equal(current, policy) {
					t.Fatal("Windows launch adapter changed the platform guard policy")
				}
			} else {
				command, err = InstallConversationApproval(root, "codex", command)
				if err != nil {
					t.Fatal(err)
				}
			}
			switch test.failure {
			case "script":
				if err := os.Remove(filepath.Join(root, "native-approval.py")); err != nil {
					t.Fatal(err)
				}
			case "crash":
				if err := os.WriteFile(filepath.Join(root, "native-approval.py"), []byte("raise SystemExit(7)\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "json":
				if err := os.WriteFile(filepath.Join(root, "native-approval.py"), []byte("print('invalid json')\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "interpreter":
				// Remove only the junction, retaining the installed interpreter.
				if err := os.Remove(pythonAlias); err != nil {
					t.Fatal(err)
				}
			}
			command.environment = map[string]string{"AGENTFLOW_NATIVE_APPROVAL_URL": server.URL + "/approval"}
			if platformGuard {
				command.environment = map[string]string{"AGENTFLOW_NATIVE_APPROVAL_URL": ""}
			}
			sandbox := &localSandbox{workspace: workspace}
			bound, err := bindCommand(sandbox, workspace, command, RunCredentials{Codex: ProviderCredential{APIKey: "local-hook-fixture-key", BaseURL: server.URL + "/v1"}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			output, err := sandbox.Exec(ctx, bound, nil)
			mu.Lock()
			defer mu.Unlock()
			expectedApprovals := 1
			if test.failure != "" || platformGuard {
				expectedApprovals = 0
			}
			if err != nil || len(failures) != 0 || modelRequests != 2 || approvalRequests != expectedApprovals {
				t.Fatalf("native Codex hook failed: error=%v model=%d approval=%d failures=%v\n%s", err, modelRequests, approvalRequests, failures, output)
			}
			_, statErr := os.Stat(marker)
			if test.name == "platform-mcp" && mcpCalls != 1 {
				t.Fatalf("platform guard blocked approved MCP tool: calls=%d\n%s", mcpCalls, output)
			}
			expectedEffect := test.allow && test.failure == "" && !platformGuard
			if expectedEffect && statErr != nil || !expectedEffect && !os.IsNotExist(statErr) {
				t.Fatalf("hook allow=%v failure=%s actual tool effect stat=%v\n%s", test.allow, test.failure, statErr, output)
			}
			if !strings.Contains(output, "HOOK_TEST_FINISHED") {
				t.Fatalf("CLI did not finish fixture turn: %s", output)
			}
		})
	}
}
