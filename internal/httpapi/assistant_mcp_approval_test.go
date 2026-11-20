package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

const mcpApprovalWriteRequest = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"tapd__proxy_execute_tool","arguments":{"tool_name":"stories_update","tool_args":{"workspace_id":123,"id":456,"name":"approved title","password":"example value"}}}}`

func mcpApprovalSession(t *testing.T, store *repo.Store, owner int64, actor domain.User, sessionID, projectID, runnerID, mode string) (*AgentToolSession, <-chan json.RawMessage) {
	t.Helper()
	calls := make(chan json.RawMessage, 10)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request agentToolRPCRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		result := map[string]any{}
		switch request.Method {
		case "tools/list":
			result["tools"] = []map[string]string{{"name": "proxy_execute_tool"}}
		case "tools/call":
			calls <- request.Params
			result = mcpToolTextResult("upstream success", false)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(agentToolRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: result})
	}))
	t.Cleanup(upstream.Close)
	server := domain.MCPServerDefinition{
		ID: "approval-tapd", Name: "TAPD", ProjectID: projectID, Scope: "project",
		MarketID: "70", Transport: "http", URL: upstream.URL,
	}
	if err := store.PutMCPServer(server); err != nil {
		t.Fatal(err)
	}
	g := domain.AssistantGeneration{ID: "approval-turn", SessionID: sessionID, UserID: owner, ActorID: actor.ID, ProjectID: projectID, Status: "running"}
	if err := store.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	s := &AgentToolSession{
		UserID: actor.ID, User: actor, ChatOwnerID: owner, ChatSessionID: sessionID, ProjectID: projectID,
		Mode: AgentToolAssistant, ExecMode: assistantExecModeForApproval(AssistantApprovalConfirm),
		RuntimeMode: mode, ExecutionRunnerID: runnerID, MCPServers: []domain.MCPServerDefinition{server},
		SelectedMCPIDs: map[string]bool{server.ID: true},
		ctx:            context.WithValue(context.Background(), assistantControlScopeKey{}, assistantControlScope{GenerationID: g.ID}),
	}
	if err := agentToolSessions.create(s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, calls
}

func waitForMCPApprovalCount(t *testing.T, store *repo.Store, s *AgentToolSession, count int) []domain.AssistantNativeApproval {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		items := store.PendingNativeApprovals(s.ChatOwnerID, s.ChatSessionID)
		if len(items) == count {
			return items
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected %d pending MCP approvals", count)
	return nil
}

func TestExternalMCPWriteWaitsForSpeaker(t *testing.T) {
	for _, tc := range []struct {
		action string
		mode   string
		result string
	}{
		{"allow", domain.AssistantModePlatform, "upstream success"},
		{"deny", domain.AssistantModeWorkspace, "用户拒绝"},
		{"cancel", domain.AssistantModeWorkspace, "审批已失效"},
		{"timeout", domain.AssistantModePlatform, "审批已失效"},
		{"handoff", domain.AssistantModePlatform, "审批已失效"},
		{"stop", domain.AssistantModePlatform, "审批已失效"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			h, owner, speaker, _, conv := sharedWorkbenchFixture(t)
			s, calls := mcpApprovalSession(t, h.repo, owner.ID, speaker, conv.ID, conv.ProjectID, "", tc.mode)
			requestCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.action == "timeout" {
				var stop context.CancelFunc
				requestCtx, stop = context.WithTimeout(requestCtx, 2*time.Second)
				defer stop()
			}
			var request agentToolRPCRequest
			if err := json.Unmarshal([]byte(mcpApprovalWriteRequest), &request); err != nil {
				t.Fatal(err)
			}
			result := make(chan *agentToolRPCResponse, 1)
			finished := make(chan struct{})
			defer func() { cancel(); <-finished }()
			go func() {
				defer close(finished)
				gateway := &agentToolGateway{deps: h.deps}
				result <- gateway.handleRPC(withAgentToolToken(requestCtx, s.Token), s, request)
			}()
			item := waitForMCPApprovalCount(t, h.repo, s, 1)[0]
			if item.ActorID != speaker.ID || item.Tool != "stories_update" {
				t.Fatalf("approval for wrong actor or tool: %+v", item)
			}
			select {
			case call := <-calls:
				t.Fatalf("upstream called before decision: %s", call)
			default:
			}

			send := func(actor domain.User, method, body string) *httptest.ResponseRecorder {
				c, w := workbenchContext(actor, method, "/", bytes.NewBufferString(body))
				c.Params = gin.Params{{Key: "id", Value: conv.ID}, {Key: "approvalId", Value: item.ID}}
				h.nativeApprovals(c)
				return w
			}
			switch tc.action {
			case "allow", "deny":
				mine := send(speaker, http.MethodGet, "")
				if mine.Code != 200 || !strings.Contains(mine.Body.String(), "approved title") ||
					!strings.Contains(mine.Body.String(), "[redacted]") || strings.Contains(mine.Body.String(), "example value") {
					t.Fatalf("speaker did not get a redacted approval: %d %s", mine.Code, mine.Body)
				}
				if peer := send(owner, http.MethodGet, ""); peer.Code != 200 || peer.Body.String() != "[]" {
					t.Fatalf("another member saw the approval: %d %s", peer.Code, peer.Body)
				}
				body, _ := json.Marshal(map[string]string{"decision": tc.action})
				if peer := send(owner, http.MethodPost, string(body)); peer.Code < 400 {
					t.Fatalf("another member decided the approval: %d %s", peer.Code, peer.Body)
				}
				if response := send(speaker, http.MethodPost, string(body)); response.Code != 200 {
					t.Fatalf("speaker decision failed: %d %s", response.Code, response.Body)
				}
			case "cancel":
				cancel()
			case "handoff":
				s.requestMode(domain.AssistantModeWorkspace)
			case "stop":
				if err := h.repo.CancelAssistantGeneration(owner.ID, conv.ID, item.GenerationID); err != nil {
					t.Fatal(err)
				}
			}

			select {
			case got := <-result:
				raw, _ := json.Marshal(got)
				failed := strings.Contains(string(raw), `"isError":true`)
				if !strings.Contains(string(raw), tc.result) || failed != (tc.action != "allow") {
					t.Fatalf("unexpected tool response: %s", raw)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("MCP call did not finish")
			}
			if tc.action == "allow" {
				select {
				case raw := <-calls:
					var params struct {
						Name      string `json:"name"`
						Arguments struct {
							ToolName string            `json:"tool_name"`
							ToolArgs map[string]string `json:"tool_args"`
						} `json:"arguments"`
					}
					if err := json.Unmarshal(raw, &params); err != nil || params.Name != "proxy_execute_tool" ||
						params.Arguments.ToolName != "stories_update" || params.Arguments.ToolArgs["id"] != "456" ||
						params.Arguments.ToolArgs["name"] != "approved title" || params.Arguments.ToolArgs["password"] != "example value" {
						t.Fatalf("approved call changed: %s (%v)", raw, err)
					}
				default:
					t.Fatal("approval never reached upstream")
				}
			}
			if len(calls) != 0 {
				t.Fatal("unexpected upstream write")
			}
			waitForMCPApprovalCount(t, h.repo, s, 0)
			if response := send(speaker, http.MethodPost, `{"decision":"allow"}`); response.Code < 400 {
				t.Fatalf("finished request accepted a new decision: %d %s", response.Code, response.Body)
			}
		})
	}
}

func TestRunnerWebsocketCancelsExternalMCPApproval(t *testing.T) {
	fixture := cvBNewHTTPFixture(t)
	runnerID, token := registerTestRunner(t, fixture.h)
	s, calls := mcpApprovalSession(t, fixture.s, fixture.user.ID, fixture.user, "remote-mcp", "platform", runnerID, domain.AssistantModePlatform)

	server := httptest.NewServer(fixture.h)
	defer server.Close()
	header := http.Header{"Authorization": {"Bearer " + token}}
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/runners/connect", header)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if err := ws.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	send := func(message runnerControlMessage) {
		t.Helper()
		if err := ws.WriteJSON(message); err != nil {
			t.Fatal(err)
		}
	}
	request := runnerControlMessage{
		Type: "agent_tool.request", RequestID: "legacy-write",
		ToolRequest: &domain.RunnerToolRequest{Token: s.Token, Body: json.RawMessage(mcpApprovalWriteRequest)},
	}
	send(request)
	var response runnerControlMessage
	if err := ws.ReadJSON(&response); err != nil || response.ToolResponse == nil ||
		!strings.Contains(string(response.ToolResponse.Body), "升级执行机器") {
		t.Fatalf("old Runner accepted an approval it cannot cancel: %+v %v", response, err)
	}

	request.RequestID = "cancellable-write"
	request.ToolRequest.SupportsCancel = true
	send(request)
	cancelled := waitForMCPApprovalCount(t, fixture.s, s, 1)[0]
	parallel := request
	parallel.RequestID = "parallel-write"
	send(parallel)
	waitForMCPApprovalCount(t, fixture.s, s, 2)

	send(runnerControlMessage{Type: "agent_tool.cancel", RequestID: request.RequestID})
	remaining := waitForMCPApprovalCount(t, fixture.s, s, 1)[0]
	if remaining.ID == cancelled.ID {
		t.Fatal("cancelled the wrong approval")
	}
	if err := fixture.s.DecideNativeApproval(s.ChatOwnerID, s.UserID, s.ChatSessionID, cancelled.ID, "allow"); err == nil {
		t.Fatal("a disconnected request could still be approved")
	}
	if len(calls) != 0 {
		t.Fatal("a request wrote to upstream before approval")
	}

	if err := fixture.s.DecideNativeApproval(s.ChatOwnerID, s.UserID, s.ChatSessionID, remaining.ID, "allow"); err != nil {
		t.Fatal(err)
	}
	for {
		if err := ws.ReadJSON(&response); err != nil {
			t.Fatal(err)
		}
		if response.RequestID == parallel.RequestID {
			break
		}
	}
	if response.ToolResponse == nil || !strings.Contains(string(response.ToolResponse.Body), "upstream success") || len(calls) != 1 {
		t.Fatalf("parallel approved request failed: %+v, upstream calls = %d", response, len(calls))
	}
	waitForMCPApprovalCount(t, fixture.s, s, 0)
}
