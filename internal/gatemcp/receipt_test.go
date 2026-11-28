package gatemcp

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSignedHTTPRequiresAuthenticatedTicketWithoutConnectorHeader(t *testing.T) {
	public, signer, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{1}, 32)
	gate, err := NewSignedHTTP("", signer, key)
	if err != nil {
		t.Fatal(err)
	}
	call := func(ticket string) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "submit_gate_result", "arguments": map[string]any{"ticket": ticket, "decision": "pass", "reason": "checked"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		gate.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(raw)))
		return response
	}
	if body := call(IssueTicket(bytes.Repeat([]byte{2}, 32))).Body.String(); !strings.Contains(body, "invalid Gate MCP ticket") {
		t.Fatalf("invalid ticket did not fail: %s", body)
	}
	fresh := IssueTicket(key)
	response := call(fresh)
	var rpc struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
		} `json:"result"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &rpc) != nil || len(rpc.Result.Content) != 1 {
		t.Fatalf("ticket tool call failed: %s", response.Body.String())
	}
	if _, err := VerifyReceipt(public, fresh, rpc.Result.Content[0].Text); err != nil {
		t.Fatal(err)
	}
}
