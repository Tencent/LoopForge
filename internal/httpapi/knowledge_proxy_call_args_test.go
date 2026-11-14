package httpapi

import (
	"context"
	"encoding/json"
	"testing"

	"agentflow/internal/knowledge"
)

// A tools/call without "arguments" (or without a query) must still reach the
// upstream and record an empty query, not the "<nil>" that fmt.Sprint would
// produce for the missing map entry.
func TestCallProxyToolRecordsEmptyQueryWithoutArguments(t *testing.T) {
	h, _, settings := newProxyToolTestHandlers(t)
	claims := knowledge.ProxyClaims{ProjectID: "p1", AgentID: "dev"}

	for _, raw := range []string{
		`{"name":"kb_list_sources"}`,
		`{"name":"mcp__agentflow-knowledge__kb_list_sources","arguments":{}}`,
	} {
		result, rpcErr := h.callProxyTool(context.Background(), settings, claims, json.RawMessage(raw))
		if rpcErr != nil {
			t.Fatalf("%s: %+v", raw, rpcErr)
		}
		if _, isErr := proxyToolResultText(t, result); isErr {
			t.Fatalf("%s: list sources should succeed", raw)
		}
	}

	activity := h.activity.recent()
	if len(activity) != 2 {
		t.Fatalf("activity = %#v", activity)
	}
	for _, a := range activity {
		if a.Tool != "kb_list_sources" || !a.OK || a.Query != "" || a.Error != "" {
			t.Fatalf("activity = %#v, want ok kb_list_sources with empty query", a)
		}
	}
}
