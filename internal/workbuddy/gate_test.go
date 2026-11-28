package workbuddy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/gatemcp"
)

func TestWorkBuddyGateRequiresOneAuthenticatedSubmission(t *testing.T) {
	const control = "test-control"
	const connector = "test-connector"
	handler, err := gatemcp.NewHTTP(control, connector)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	t.Setenv(gateURLEnv, server.URL)
	t.Setenv(gateControlTokenEnv, control)
	gate, err := NewGateClient()
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	ticket, err := gate.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other, err := gate.Open(ctx)
	if err != nil || other == ticket {
		t.Fatal("expected separate attempt tickets", err)
	}
	if _, err := gate.Result(ctx, ticket); err == nil || !strings.Contains(err.Error(), "未收到 submit_gate_result") {
		t.Fatalf("missing submission = %v", err)
	}
	if status, _, err := (&GateClient{endpoint: gate.endpoint, token: connector}).request(ctx, http.MethodGet, ticket); err != nil || status != http.StatusUnauthorized {
		t.Fatalf("connector access to control endpoint = %d, %v", status, err)
	}

	if status, _ := callGateTool(t, server, "tools/list", control, nil); status != http.StatusUnauthorized {
		t.Fatalf("control access to MCP endpoint = %d", status)
	}
	if status, body := callGateTool(t, server, "tools/list", connector, nil); status != http.StatusOK || !strings.Contains(body, `"required":["decision","reason","ticket"]`) {
		t.Fatalf("tool schema = %d %s", status, body)
	}
	submit := func(arguments map[string]string) string {
		t.Helper()
		status, body := callGateTool(t, server, "tools/call", connector, map[string]any{"name": "submit_gate_result", "arguments": arguments})
		if status != http.StatusOK {
			t.Fatalf("submit response = %d %s", status, body)
		}
		return body
	}
	if body := submit(map[string]string{"ticket": ticket, "decision": "human", "reason": "need acceptance"}); !strings.Contains(body, "actionRequired is required") {
		t.Fatalf("accepted incomplete human decision: %s", body)
	}
	if body := submit(map[string]string{"ticket": ticket, "decision": "human", "reason": "need acceptance", "actionRequired": "confirm the owner"}); !strings.Contains(body, "Gate result submitted") {
		t.Fatalf("submission failed: %s", body)
	}
	if body := submit(map[string]string{"ticket": ticket, "decision": "pass", "reason": "overwrite"}); !strings.Contains(body, "already submitted") {
		t.Fatalf("accepted duplicate: %s", body)
	}
	var result domain.GateResult
	raw, err := gate.Result(ctx, ticket)
	if err != nil || json.Unmarshal(raw, &result) != nil || result.Decision != "human" || result.ActionRequired != "confirm the owner" {
		t.Fatalf("gate result = %s, %v", raw, err)
	}
	if _, err := gate.Result(ctx, other); err == nil || !strings.Contains(err.Error(), "未收到 submit_gate_result") {
		t.Fatalf("second attempt received first attempt's result: %v", err)
	}
	gate.Discard(ctx, ticket)
	if body := submit(map[string]string{"ticket": ticket, "decision": "pass", "reason": "late"}); !strings.Contains(body, "invalid or expired") {
		t.Fatalf("accepted a discarded ticket: %s", body)
	}
}

func callGateTool(t *testing.T, server *httptest.Server, method, token string, params any) (int, string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/mcp", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(body)
}
