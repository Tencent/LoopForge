package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"agentflow/internal/runtime"
)

func TestAssistantACPConversationInput(t *testing.T) {
	prompt := strings.Repeat("完整的对话内容 λ\n", 12_000)
	for _, resume := range []string{"", "old-session"} {
		name := "new"
		if resume != "" {
			name = "resume"
		}
		t.Run(name, func(t *testing.T) {
			cwd := t.TempDir()
			acp, err := newAssistantACP(cwd, prompt, resume)
			if err != nil {
				t.Fatal(err)
			}
			defer acp.close()
			wire := json.NewDecoder(acp.input)
			read := func(id int, method string) json.RawMessage {
				t.Helper()
				var request struct {
					ID     int             `json:"id"`
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
					Result json.RawMessage `json:"result"`
				}
				if err := wire.Decode(&request); err != nil || request.ID != id || request.Method != method {
					t.Fatalf("want %d %s, got %d %s: %v", id, method, request.ID, request.Method, err)
				}
				if method == "" {
					return request.Result
				}
				return request.Params
			}
			read(1, "initialize")
			acp.translate(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}}`)
			session := "new-session"
			if resume == "" {
				var params struct {
					Cwd string `json:"cwd"`
				}
				if err := json.Unmarshal(read(2, "session/new"), &params); err != nil || params.Cwd != cwd {
					t.Fatalf("new session working directory = %q, want %q: %v", params.Cwd, cwd, err)
				}
				init := acp.translate(`{"jsonrpc":"2.0","id":2,"result":{"sessionId":"new-session","models":{"currentModelId":"cursor-model"}}}`)
				if !strings.Contains(strings.Join(init, "\n"), `"model":"cursor-model"`) {
					t.Fatalf("missing model information: %v", init)
				}
			} else {
				session = resume
				if params := read(2, "session/load"); !strings.Contains(string(params), `"sessionId":"old-session"`) {
					t.Fatalf("load parameters: %s", params)
				}
				acp.translate(`{"jsonrpc":"2.0","id":2,"result":{}}`)
			}
			var turn struct {
				SessionID string `json:"sessionId"`
				Prompt    []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			if err := json.Unmarshal(read(3, "session/prompt"), &turn); err != nil || turn.SessionID != session ||
				len(turn.Prompt) != 1 || turn.Prompt[0].Text != prompt {
				t.Fatalf("the turn must carry the full prompt and native session: %v", err)
			}
			acp.translate(`{"jsonrpc":"2.0","id":4,"method":"session/request_permission","params":{}}`)
			if result := read(4, ""); !strings.Contains(string(result), `"outcome":"cancelled"`) {
				t.Fatalf("unhandled permission must not be granted: %s", result)
			}
			for _, chunk := range []string{"> Auto ro", "uted to GPT-5.6 Luna\n\n", "received"} {
				encoded, _ := json.Marshal(chunk)
				lines := acp.translate(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"` + session + `","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":` + string(encoded) + `}}}}`)
				if chunk != "received" && len(lines) != 0 {
					t.Fatalf("routing notice leaked into the answer stream: %v", lines)
				}
			}
			lines := acp.translate(`{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}`)
			var result struct {
				Result    string `json:"result"`
				SessionID string `json:"session_id"`
			}
			if len(lines) == 0 || json.Unmarshal([]byte(lines[len(lines)-1]), &result) != nil ||
				trimAssistantReplyText(result.Result) != "received" || result.SessionID != session {
				t.Fatalf("unexpected translated result: %v", lines)
			}
		})
	}
}

func TestAssistantACPStopsCLIThatLingersAfterReply(t *testing.T) {
	acp, err := newAssistantACP(t.TempDir(), "prompt", "")
	if err != nil {
		t.Fatal(err)
	}
	defer acp.close()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	_, err = acp.execute(ctx, func(execCtx context.Context) (string, error) {
		acp.translate(`{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}`)
		<-execCtx.Done()
		return "", execCtx.Err()
	})
	if err != nil || ctx.Err() != nil {
		t.Fatalf("completed turn did not release the CLI: %v, %v", err, ctx.Err())
	}
}

func TestAssistantACPRespectsUserCancellation(t *testing.T) {
	acp, err := newAssistantACP(t.TempDir(), "prompt", "")
	if err != nil {
		t.Fatal(err)
	}
	defer acp.close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err = acp.execute(ctx, func(execCtx context.Context) (string, error) {
		cancel()
		<-execCtx.Done()
		return "", execCtx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("user cancellation was lost: %v", err)
	}
}

func TestAssistantCursorShortResumeKeepsItsTransport(t *testing.T) {
	for _, tc := range []struct {
		name string
		acp  bool
	}{{"print", false}, {"acp", true}} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			config := filepath.Join(root, "mcp.json")
			if err := os.WriteFile(config, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := writeAgentCLISession(root, runtime.AgentCLICursor, "chat", "native-cursor", tc.acp); err != nil {
				t.Fatal(err)
			}
			inv, err := prepareAssistantAgentCLIInvocation(runtime.AgentCLICursor, assistantEngineInvocationOpts{
				Root: root, Config: config, SessionID: "chat", Resume: true, Prompt: "short follow-up",
			})
			if err != nil {
				t.Fatal(err)
			}
			defer inv.finish(false)
			if inv.sessionRef != "native-cursor" || slices.Contains(inv.command.Argv, "acp") != tc.acp {
				t.Fatalf("resume changed the native session or its transport: %v", inv.command.Argv)
			}
			if acp, ok := inv.command.Stdin.(*assistantACP); ok && acp.prompt != "short follow-up" {
				t.Fatalf("unexpected ACP prompt: %q", acp.prompt)
			}
		})
	}
}
