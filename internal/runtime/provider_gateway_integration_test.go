package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// Opt-in: exercises installed native CLIs against a local, zero-cost gateway.
func TestNativeProviderCustomGateway(t *testing.T) {
	if os.Getenv("AGENTFLOW_TEST_NATIVE_PROVIDERS") != "1" {
		t.Skip("set AGENTFLOW_TEST_NATIVE_PROVIDERS=1 to exercise installed CLIs")
	}
	for _, kind := range []string{"codex", "claude-code"} {
		t.Run(kind, func(t *testing.T) {
			binary := "codex"
			if kind == "claude-code" {
				binary = "claude"
			}
			bin, err := exec.LookPath(binary)
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			requests := 0
			var wrong string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, "bad body", 400)
					return
				}
				mu.Lock()
				requests++
				expectedPath, header := "/v1/responses", r.Header.Get("Authorization")
				expectedKey := "Bearer local-gateway-test-key"
				if kind == "claude-code" {
					expectedPath = "/v1/messages"
					header = r.Header.Get("X-Api-Key")
					expectedKey = "local-gateway-test-key"
				}
				if r.URL.Path != expectedPath || header != expectedKey || body["model"] != "gateway-test-model" {
					wrong = "wrong endpoint, key or model"
				}
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				emit := func(event string, data any) {
					raw, _ := json.Marshal(data)
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
				}
				if kind == "codex" {
					message := map[string]any{"id": "msg_test", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "GATEWAY_OK", "annotations": []any{}}}}
					response := map[string]any{"id": "resp_test", "object": "response", "status": "completed", "model": "gateway-test-model", "output": []any{message}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 3, "total_tokens": 13}}
					emit("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_test", "status": "in_progress", "output": []any{}}})
					emit("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": "msg_test", "type": "message", "role": "assistant", "content": []any{}}})
					emit("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "item_id": "msg_test", "output_index": 0, "content_index": 0, "delta": "GATEWAY_OK"})
					emit("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": message})
					emit("response.completed", map[string]any{"type": "response.completed", "response": response})
				} else {
					emit("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_test", "type": "message", "role": "assistant", "model": "gateway-test-model", "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 0}}})
					emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}})
					emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": "GATEWAY_OK"}})
					emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
					emit("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 3}})
					emit("message_stop", map[string]string{"type": "message_stop"})
				}
			}))
			defer server.Close()
			args := []string{"exec", "--json", "--skip-git-repo-check", "--ephemeral"}
			if kind == "claude-code" {
				args = []string{"-p", "--output-format", "stream-json", "--tools", "", "--max-turns", "1", "--no-session-persistence"}
			}
			workspace := t.TempDir()
			ex := domain.ExecutorDefinition{Binary: bin, Args: args, Kind: kind, Model: "gateway-test-model"}
			// Use the same preparation and closed credential binding as real SOP nodes.
			compiled := compiledConfig{AgentRoot: ".agentflow/test"}
			if err := os.MkdirAll(workspace+"/.agentflow/test", 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(workspace+"/.agentflow/test/agent-system.md", []byte("Reply GATEWAY_OK. Do not call tools."), 0600); err != nil {
				t.Fatal(err)
			}
			command, err := providerForKind(kind).Prepare(providerInvocation{Executor: ex, Compiled: compiled, Prompt: "Reply GATEWAY_OK.", Driver: "local"})
			if err != nil {
				t.Fatal(err)
			}
			creds := RunCredentials{Codex: ProviderCredential{APIKey: "local-gateway-test-key", BaseURL: server.URL + "/v1"}, ClaudeCode: ProviderCredential{APIKey: "local-gateway-test-key", BaseURL: server.URL}}
			sb := &localSandbox{workspace: workspace}
			bound, err := bindCommand(sb, workspace, command, creds)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			output, err := sb.Exec(ctx, bound, nil)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Fatalf("CLI failed (%d gateway requests): %v\n%s", requests, err, output)
			}
			if requests == 0 || wrong != "" || !strings.Contains(output, "GATEWAY_OK") {
				t.Fatalf("custom gateway verification failed: requests=%d %s\n%s", requests, wrong, output)
			}
			t.Logf("%s: custom endpoint, user API key and model verified (%d requests)", kind, requests)
		})
	}
}
