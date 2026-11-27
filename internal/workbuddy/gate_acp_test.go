package workbuddy

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gatemcp"
)

func TestWorkBuddyACPGateReceivesSignedResultWithoutRelay(t *testing.T) {
	publicKey, signingKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "gate.pub.pem")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(gatePublicKeyEnv, keyFile)
	t.Setenv(gateURLEnv, "")
	t.Setenv(gateControlTokenEnv, "")
	const connectorToken = "wma-connector"
	t.Setenv(gatemcp.TicketKeyEnv, "")
	handler, err := gatemcp.NewSignedHTTP(connectorToken, signingKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	gate, err := BeginGate(t.Context())
	if err != nil || !gate.ReturnsViaACP() {
		t.Fatalf("expected ACP gate attempt, got %v", err)
	}
	defer gate.Discard(t.Context())
	call := func(decision string) string {
		t.Helper()
		status, body := callGateTool(t, server, "tools/call", connectorToken, map[string]any{
			"name": "submit_gate_result", "arguments": map[string]string{
				"ticket": gate.Ticket, "decision": decision, "reason": "tested", "actionRequired": "check the report",
			},
		})
		if status != http.StatusOK {
			t.Fatalf("gate tool response = %d %s", status, body)
		}
		return body
	}
	first := call("human")
	receipt := gateReceiptPattern.FindString(first)
	if !strings.HasPrefix(receipt, gatemcp.ReceiptPrefix) || gateReceiptPattern.FindString(call("human")) != receipt {
		t.Fatal("retry did not return the original signed gate receipt")
	}
	if other := call("pass"); !strings.Contains(other, "already submitted") || gateReceiptPattern.MatchString(other) {
		t.Fatalf("conflicting decision response = %s", other)
	}
	for _, through := range []string{"tool result", "text chunks"} {
		t.Run(through, func(t *testing.T) {
			toolEvent := map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "gate-call", "status": "completed", "rawOutput": json.RawMessage(first)}
			updates := []map[string]any{toolEvent}
			if through == "text chunks" {
				mid := len(receipt) / 2
				updates = []map[string]any{
					{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"text": "\n" + receipt[:mid]}},
					{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"text": receipt[mid:]}},
				}
			}
			client, config, _ := testWorkBuddy(t, false, false, updates...)
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			session, err := client.CreateSession(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			turn, err := client.RunTurn(ctx, session, "acp-access-token", "", "submit the gate", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := gate.Result(ctx, turn)
			var result domain.GateResult
			if err != nil || json.Unmarshal(raw, &result) != nil || result.Decision != "human" || result.ActionRequired != "check the report" {
				t.Fatalf("gate decision = %q, %v", raw, err)
			}
			if display := GateDisplayText(turn.Text); display != "完成" {
				t.Fatalf("unexpected user-visible output %q", display)
			}
		})
	}

	signatureStart := strings.LastIndexByte(receipt, '.') + 1
	mutated := "A"
	if receipt[signatureStart] == 'A' {
		mutated = "B"
	}
	for _, text := range []string{`{"decision":"pass"}`, receipt[:signatureStart] + mutated + receipt[signatureStart+1:]} {
		if _, err := gate.Result(t.Context(), Turn{Text: text}); err == nil {
			t.Fatal("accepted a decision without a trusted signature")
		}
	}
	next, err := BeginGate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := next.Result(t.Context(), Turn{Text: receipt}); err == nil {
		t.Fatal("accepted a signed receipt for a different attempt")
	}
}
