package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// A CLI that gives up on a call (its own MCP tool timeout) sends
// notifications/cancelled. The waiting approval must end then: a late
// approval cannot write, and a retried call gets its own card.
func TestExternalMCPWriteCancelledByCLINotification(t *testing.T) {
	for _, id := range []string{`1`, `"call-7"`} {
		t.Run(id, func(t *testing.T) {
			h, owner, speaker, _, conv := sharedWorkbenchFixture(t)
			s, calls := mcpApprovalSession(t, h.repo, owner.ID, speaker, conv.ID, conv.ProjectID, "", domain.AssistantModePlatform)
			gateway := &agentToolGateway{deps: h.deps}
			ctx := withAgentToolToken(context.Background(), s.Token)
			// The HTTP and runner transports decode with UseNumber.
			var request agentToolRPCRequest
			decoded := json.NewDecoder(strings.NewReader(strings.Replace(mcpApprovalWriteRequest, `"id":1`, `"id":`+id, 1)))
			decoded.UseNumber()
			if err := decoded.Decode(&request); err != nil {
				t.Fatal(err)
			}
			result := make(chan *agentToolRPCResponse, 1)
			go func() { result <- gateway.handleRPC(ctx, s, request) }()
			item := waitForMCPApprovalCount(t, h.repo, s, 1)[0]

			// A cancellation for another request id leaves this call waiting.
			other := agentToolRPCRequest{JSONRPC: "2.0", Method: "notifications/cancelled", Params: json.RawMessage(`{"requestId":"unrelated","reason":"timeout"}`)}
			if response := gateway.handleRPC(ctx, s, other); response != nil {
				t.Fatalf("notification got a response: %+v", response)
			}
			select {
			case response := <-result:
				t.Fatalf("unrelated cancellation ended the call: %+v", response)
			case <-time.After(700 * time.Millisecond):
			}

			cancelled := agentToolRPCRequest{JSONRPC: "2.0", Method: "notifications/cancelled", Params: json.RawMessage(`{"requestId":` + id + `,"reason":"Request timed out"}`)}
			if response := gateway.handleRPC(ctx, s, cancelled); response != nil {
				t.Fatalf("notification got a response: %+v", response)
			}
			var response *agentToolRPCResponse
			select {
			case response = <-result:
			case <-time.After(5 * time.Second):
				t.Fatal("cancelled call kept waiting for approval")
			}
			raw, _ := json.Marshal(response)
			if !strings.Contains(string(raw), "审批已失效") {
				t.Fatalf("cancelled call result = %s", raw)
			}
			waitForMCPApprovalCount(t, h.repo, s, 0)
			if err := h.repo.DecideNativeApproval(s.ChatOwnerID, s.UserID, s.ChatSessionID, item.ID, "allow"); err == nil {
				t.Fatal("a late approval of the cancelled card was accepted")
			}
			select {
			case call := <-calls:
				t.Fatalf("cancelled call wrote upstream: %s", call)
			case <-time.After(300 * time.Millisecond):
			}
			s.mu.Lock()
			left := len(s.toolCalls)
			s.mu.Unlock()
			if left != 0 {
				t.Fatalf("finished calls stay tracked: %d", left)
			}
		})
	}
}

func TestAgentToolRPCIDKey(t *testing.T) {
	if agentToolRPCIDKey(json.Number("1")) == agentToolRPCIDKey("1") {
		t.Fatal("numeric and string ids must not collide")
	}
	if agentToolRPCIDKey(json.Number("3")) != cancelledRequestID(json.RawMessage(`{"requestId":3}`)) {
		t.Fatal("numeric request ids must match their cancellation")
	}
	if agentToolRPCIDKey(float64(3)) != agentToolRPCIDKey(json.Number("3")) {
		t.Fatal("ids decoded without UseNumber must match too")
	}
	if cancelledRequestID(json.RawMessage(`{}`)) != "" || cancelledRequestID(json.RawMessage(`not json`)) != "" {
		t.Fatal("missing requestId must not match any call")
	}
}

// Secret field names are matched after normalization and secret values are
// found whatever their key; containers such as headers stay visible except for
// their secret fields, and the card lists what it hides.
func TestMCPApprovalInputRedaction(t *testing.T) {
	// Credential-shaped fixtures are assembled at run time so the repository
	// secret scanner does not flag the test source.
	jwt := strings.Join([]string{"eyJ" + strings.Repeat("h", 16), "eyJ" + strings.Repeat("p", 16), strings.Repeat("s", 24)}, ".")
	raw, _ := json.Marshal(map[string]any{
		"server": "notes", "tool": "create_note",
		"arguments": map[string]any{
			"title":         "release notes",
			"content":       "deploy with Bearer abcdefghijklmnop and ghp_" + strings.Repeat("a", 36) + " then " + jwt,
			"apiToken":      "tok-123",
			"PRIVATE-TOKEN": "glpat-" + strings.Repeat("g", 20),
			"max_tokens":    512,
			"tokenCount":    3,
			"headers":       map[string]any{"x-api-key": "k-1", "X-Note-Body": "visible payload"},
			"credentials":   map[string]any{"user": "alice", "password": "hunter2"},
			"password":      "example value",
		},
	})
	out, hidden := redactNativeApprovalInputFields(raw, domain.User{})
	text := string(out)
	for _, leaked := range []string{"tok-123", "glpat-", "k-1", "hunter2", "example value", "abcdefghijklmnop", "ghp_", jwt} {
		if strings.Contains(text, leaked) {
			t.Fatalf("card leaks %q: %s", leaked, text)
		}
	}
	for _, shown := range []string{"release notes", "visible payload", `"user":"alice"`, `"max_tokens":512`, `"tokenCount":3`, "deploy with"} {
		if !strings.Contains(text, shown) {
			t.Fatalf("card hides %q that will be sent: %s", shown, text)
		}
	}
	want := []string{"arguments.PRIVATE-TOKEN", "arguments.apiToken", "arguments.credentials.password", "arguments.headers.x-api-key", "arguments.password"}
	if strings.Join(hidden, ",") != strings.Join(want, ",") {
		t.Fatalf("hidden fields = %v, want %v", hidden, want)
	}
	if !json.Valid(out) {
		t.Fatalf("card input must stay JSON: %s", out)
	}
}

// The approval card tells the approver how many fields it does not show.
func TestExternalMCPWriteCardCountsHiddenFields(t *testing.T) {
	h, owner, speaker, _, conv := sharedWorkbenchFixture(t)
	s, _ := mcpApprovalSession(t, h.repo, owner.ID, speaker, conv.ID, conv.ProjectID, "", domain.AssistantModePlatform)
	gateway := &agentToolGateway{deps: h.deps}
	ctx, cancel := context.WithCancel(withAgentToolToken(context.Background(), s.Token))
	var request agentToolRPCRequest
	if err := json.Unmarshal([]byte(mcpApprovalWriteRequest), &request); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); gateway.handleRPC(ctx, s, request) }()
	defer func() { cancel(); <-done }()
	item := waitForMCPApprovalCount(t, h.repo, s, 1)[0]
	if !strings.Contains(item.Summary, "1 个字段已隐藏") || !strings.Contains(item.Summary, "password") {
		t.Fatalf("summary does not mention the hidden field: %q", item.Summary)
	}
}
