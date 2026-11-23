package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
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
				acp.translate(`{"jsonrpc":"2.0","id":2,"result":{"sessionId":"new-session"}}`)
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
			acp.translate(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"` + session + `","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"received"}}}}`)
			result := strings.Join(acp.translate(`{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}`), "\n")
			if !strings.Contains(result, `"result":"received"`) || !strings.Contains(result, `"session_id":"`+session+`"`) {
				t.Fatalf("unexpected translated result: %s", result)
			}
		})
	}
}
