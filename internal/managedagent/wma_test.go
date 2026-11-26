package managedagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWMATextAndCancellation(t *testing.T) {
	for _, cancelPrompt := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelPrompt), func(t *testing.T) {
			events := make(chan string, 8)
			prompted, cancelled, closed := make(chan struct{}, 1), make(chan struct{}, 1), make(chan struct{}, 1)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/v2/") {
					if r.Header.Get("X-Api-Key") != "ck_test" {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					switch r.URL.Path {
					case "/v2/enterprises/ent/managedagent/agents/agt/sessions/sess":
						fmt.Fprintf(w, `{"code":0,"data":{"agentId":"agt","sessionId":"sess","endpointSet":[{"url":"https://%s","endpointType":"PUBLIC"}]}}`, r.Host)
					case "/v2/enterprises/ent/managedagent/user-access-token":
						fmt.Fprint(w, `{"code":0,"data":{"accessToken":"user-token"}}`)
					default:
						t.Errorf("unexpected control request %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
					return
				}
				if r.URL.Path != acpPath || r.Header.Get("Authorization") != "Bearer user-token" || r.Header.Get("X-Agent-Session-Id") != "sess" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if r.Method == http.MethodGet {
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("Acp-Connection-Id", "conn")
					w.(http.Flusher).Flush()
					for {
						select {
						case event := <-events:
							fmt.Fprintf(w, "data: %s\n\n", event)
							w.(http.Flusher).Flush()
						case <-r.Context().Done():
							return
						}
					}
				}
				if r.Header.Get("Acp-Connection-Id") != "conn" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if r.Method == http.MethodDelete {
					closed <- struct{}{}
					w.WriteHeader(http.StatusNoContent)
					return
				}
				var msg acpMessage
				if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
					t.Errorf("decode ACP request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				switch msg.Method {
				case "initialize", "session/load":
					if msg.Method == "session/load" {
						events <- chunk("history should not be replayed")
					}
					events <- fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{}}`, msg.ID)
					w.WriteHeader(http.StatusAccepted)
				case "session/prompt":
					if !strings.Contains(string(msg.Params), "say hello") {
						t.Error("prompt was not forwarded")
					}
					prompted <- struct{}{}
					if cancelPrompt {
						w.WriteHeader(http.StatusAccepted)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, ": heartbeat\n\ndata: %s\n\ndata: %s\n\n", chunk("hel"), chunk("lo"))
					fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"stopReason\":\"end_turn\"}}\n\n", msg.ID)
				case "session/cancel":
					cancelled <- struct{}{}
					w.WriteHeader(http.StatusAccepted)
				default:
					t.Errorf("unexpected RPC %s", msg.Method)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			client, err := NewWMA(Config{
				Endpoint: server.URL, ACPOrigin: server.URL, EnterpriseID: "ent", AgentID: "agt", SessionID: "sess", APIKey: "ck_test",
			}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if cancelPrompt {
				go func() {
					select {
					case <-prompted:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			var output strings.Builder
			text, err := client.Run(ctx, "say hello", func(delta string) { output.WriteString(delta) })
			if cancelPrompt {
				if err == nil {
					t.Fatal("expected interruption")
				}
				select {
				case <-cancelled:
				default:
					t.Fatal("ACP cancellation was not sent")
				}
			} else if err != nil || text != "hello" || output.String() != text {
				t.Fatalf("text = %q, observed = %q, err = %v", text, output.String(), err)
			}
			select {
			case <-closed:
			default:
				t.Fatal("ACP connection was not closed")
			}
		})
	}
}

func chunk(text string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":%q}}}}`, text)
}

func TestWMARefusesUntrustedACPOrigin(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agents/agt/sessions/sess") {
			t.Errorf("made an unexpected credential request: %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"code":0,"data":{"agentId":"agt","sessionId":"sess","endpointSet":[{"url":"https://agt.ma.workbuddy.cn.evil.test","endpointType":"PUBLIC"}]}}`)
	}))
	defer server.Close()
	client, err := NewWMA(Config{Endpoint: server.URL, EnterpriseID: "ent", AgentID: "agt", SessionID: "sess", APIKey: "ck_test"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Run(context.Background(), "test", nil); err == nil || !strings.Contains(err.Error(), "no trusted ACP endpoint") {
		t.Fatalf("unexpected error: %v", err)
	}
}
