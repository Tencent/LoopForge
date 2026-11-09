package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// credentialSandbox binds credentials the way managedSandbox does.
type credentialSandbox struct {
	*localSandbox
	credentials RunCredentials
}

func (s credentialSandbox) Exec(ctx context.Context, command CommandSpec, sink EventSink) (string, error) {
	bound, err := bindCommand(s.localSandbox, s.workspace, command, s.credentials)
	if err != nil {
		return "", err
	}
	return s.localSandbox.Exec(ctx, bound, sink)
}

// probeMCPServer is a minimal Streamable HTTP MCP server with one tool.
func probeMCPServer(t *testing.T) (*httptest.Server, func() int) {
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if json.Unmarshal(body, &req) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		id, hasID := req["id"]
		if !hasID {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch req["method"] {
		case "initialize":
			version := "2025-03-26"
			if params, ok := req["params"].(map[string]any); ok {
				if v, ok := params["protocolVersion"].(string); ok && v != "" {
					version = v
				}
			}
			result = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "probe", "version": "1.0.0"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{
				"name": "read_secret", "description": "Returns the secret value.",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
			}}}
		case "tools/call":
			mu.Lock()
			calls++
			mu.Unlock()
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "secret=42"}}}
		case "ping":
			result = map[string]any{}
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "method not found"}})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}))
	t.Cleanup(server.Close)
	return server, func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
}

// Opt-in: runs the installed agent CLIs through the SOP node path with real
// keys. Per kind: AGENTFLOW_TEST_<KIND>_KEY, _BASE_URL, _PROTOCOL, _MODEL.
func TestAgentCLINodeInvocation(t *testing.T) {
	if os.Getenv("AGENTFLOW_TEST_AGENT_CLIS") != "1" {
		t.Skip("set AGENTFLOW_TEST_AGENT_CLIS=1 to exercise installed agent CLIs")
	}
	for _, kind := range AgentCLIKinds {
		prefix := "AGENTFLOW_TEST_" + strings.ToUpper(kind) + "_"
		key := os.Getenv(prefix + "KEY")
		t.Run(kind, func(t *testing.T) {
			if key == "" {
				t.Skip("no " + prefix + "KEY")
			}
			mcp, mcpCalls := probeMCPServer(t)
			workspace := t.TempDir()
			sb := credentialSandbox{
				localSandbox: &localSandbox{id: "agent-cli-test", workspace: workspace},
				credentials: RunCredentials{AgentCLI: map[string]ProviderCredential{kind: {
					APIKey: key, BaseURL: os.Getenv(prefix + "BASE_URL"), Protocol: os.Getenv(prefix + "PROTOCOL"),
				}}},
			}
			ex := domain.ExecutorDefinition{ID: kind, Kind: kind, Binary: AgentCLIBinary(kind), Protocol: "stream-json", Model: os.Getenv(prefix + "MODEL")}
			agent := domain.AgentDefinition{ID: "probe", Name: "probe", Prompt: "You are a terse test agent. Follow the task exactly."}
			assets := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{{ID: "probe", Name: "probe", Transport: "http", URL: mcp.URL + "/mcp"}}}
			run := func(prompt, resume string) (string, []domain.TelemetryEvent) {
				var mu sync.Mutex
				var events []domain.TelemetryEvent
				sink := func(event domain.TelemetryEvent) {
					mu.Lock()
					events = append(events, event)
					mu.Unlock()
				}
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
				defer cancel()
				output, err := NewExecutorRunner().Run(ctx, sb, fmt.Sprintf("inv-%d", time.Now().UnixNano()), ex, agent, assets, prompt, InvocationOptions{ResumeSessionID: resume}, sink)
				if err != nil {
					t.Fatalf("run failed: %v\n%s", err, output)
				}
				mu.Lock()
				defer mu.Unlock()
				return output, append([]domain.TelemetryEvent(nil), events...)
			}
			output, events := run("Call the read_secret tool of the probe MCP server, then reply with only the number after 'secret='.", "")
			session, toolSeen, usage := "", false, false
			for _, event := range events {
				if event.SessionID != "" {
					session = event.SessionID
				}
				if event.Tool != nil && strings.Contains(event.Tool.Name, "read_secret") {
					toolSeen = true
				}
				if event.Usage != nil && event.Usage.OutputTokens > 0 {
					usage = true
				}
			}
			t.Logf("%s first turn: output=%q session=%q tool=%v usage=%v mcpCalls=%d", kind, output, session, toolSeen, usage, mcpCalls())
			if mcpCalls() == 0 || !strings.Contains(output, "42") {
				t.Fatalf("MCP tool was not used: calls=%d output=%q", mcpCalls(), output)
			}
			if !toolSeen {
				var tools []string
				for _, event := range events {
					if event.Tool != nil {
						tools = append(tools, event.Name+":"+event.Tool.Name+":"+event.Message)
					}
				}
				t.Errorf("no tool telemetry for read_secret: %q", tools)
			}
			if session == "" {
				t.Fatalf("no session id reported")
			}
			output, events = run("What number did the tool return earlier in this conversation? Reply with only the number and do not call any tool.", session)
			resumed := ""
			for _, event := range events {
				if event.SessionID != "" {
					resumed = event.SessionID
				}
			}
			t.Logf("%s resumed turn: output=%q session=%q", kind, output, resumed)
			if !strings.Contains(output, "42") {
				t.Errorf("resumed turn lost the conversation: %q", output)
			}
		})
	}
}
