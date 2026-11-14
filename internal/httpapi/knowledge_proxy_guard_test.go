package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"agentflow/internal/knowledge"
)

// handleProxyRPC must surface callProxyTool's JSON-RPC errors with their own
// code and message (and the request id), and pass tool results through as a
// result rather than an error.
func TestKnowledgeProxyRPCKeepsToolCallErrorCodes(t *testing.T) {
	h, kb, settings := newProxyToolTestHandlers(t)
	claims := knowledge.ProxyClaims{ProjectID: "p1", AgentID: "dev"}
	rpc := func(id any, method, params string) *agentToolRPCResponse {
		t.Helper()
		req := agentToolRPCRequest{JSONRPC: "2.0", ID: id, Method: method}
		if params != "" {
			req.Params = json.RawMessage(params)
		}
		resp := h.handleProxyRPC(context.Background(), settings, claims, req)
		if resp == nil {
			t.Fatalf("%s: nil response", method)
		}
		if resp.ID != id {
			t.Fatalf("%s: response id = %v, want %v", method, resp.ID, id)
		}
		return resp
	}

	malformed := rpc(11, "tools/call", `{"name":1}`)
	if malformed.Error == nil || malformed.Error.Code != -32602 || malformed.Result != nil {
		t.Fatalf("malformed tools/call = %+v / %+v", malformed.Error, malformed.Result)
	}

	write := rpc(12, "tools/call", `{"name":"mcp__agentflow-knowledge__kb_write_document","arguments":{"source":"a"}}`)
	if write.Error == nil || write.Error.Code != -32602 || write.Result != nil {
		t.Fatalf("write tools/call = %+v / %+v", write.Error, write.Result)
	}
	want := "知识库工具 mcp__agentflow-knowledge__kb_write_document 不可用：Agent 只能只读检索，新知识请写入 knowledge.md 等待审核"
	if write.Error.Message != want {
		t.Fatalf("write message = %q, want %q", write.Error.Message, want)
	}
	if kb.lastCall("kb_write_document") != nil {
		t.Fatal("rejected write reached upstream")
	}

	if unknown := rpc(13, "tools/delete", ""); unknown.Error == nil || unknown.Error.Code != -32601 || unknown.Error.Message != "method not found" {
		t.Fatalf("unknown method = %+v", unknown.Error)
	}

	outOfScope := rpc(14, "tools/call", `{"name":"kb_fetch","arguments":{"source":"b","path":"x.md"}}`)
	if outOfScope.Error != nil {
		t.Fatalf("tool failure must be an MCP result, not an rpc error: %+v", outOfScope.Error)
	}
	if text, isErr := proxyToolResultText(t, outOfScope.Result); !isErr || !strings.Contains(text, "授权范围") {
		t.Fatalf("out-of-scope result = %q isError=%t", text, isErr)
	}

	search := rpc(15, "tools/call", `{"name":"kb_search","arguments":{"query":"x"}}`)
	if search.Error != nil {
		t.Fatalf("search: %+v", search.Error)
	}
	if text, isErr := proxyToolResultText(t, search.Result); isErr || !strings.Contains(text, "a@c1:x.md") {
		t.Fatalf("search result = %q isError=%t", text, isErr)
	}
}

// The recorded activity keeps its size limits (query 120 runes, error 200
// runes) and a UTC start timestamp, as before the extraction.
func TestCallProxyToolActivityTruncatesAndStampsUTC(t *testing.T) {
	h, _, settings := newProxyToolTestHandlers(t)
	claims := knowledge.ProxyClaims{ProjectID: "p1", AgentID: "dev"}

	query := strings.Repeat("知", 130)
	args, _ := json.Marshal(map[string]any{"name": "kb_search", "arguments": map[string]any{"query": query}})
	before := time.Now()
	if _, rpcErr := h.callProxyTool(context.Background(), settings, claims, args); rpcErr != nil {
		t.Fatalf("search: %+v", rpcErr)
	}
	after := time.Now()

	source := strings.Repeat("源", 300)
	args, _ = json.Marshal(map[string]any{"name": "kb_fetch", "arguments": map[string]any{"source": source, "path": "x.md"}})
	result, rpcErr := h.callProxyTool(context.Background(), settings, claims, args)
	if rpcErr != nil {
		t.Fatalf("fetch: %+v", rpcErr)
	}
	fullErr, isErr := proxyToolResultText(t, result)
	if !isErr || utf8.RuneCountInString(fullErr) <= 200 {
		t.Fatalf("expected a long tool error, got %d runes isError=%t", utf8.RuneCountInString(fullErr), isErr)
	}

	activity := h.activity.recent()
	if len(activity) != 2 {
		t.Fatalf("activity = %#v", activity)
	}
	failed, searched := activity[0], activity[1]

	if want := strings.Repeat("知", 120) + "…"; searched.Query != want {
		t.Fatalf("query not truncated to 120 runes: %d runes", utf8.RuneCountInString(searched.Query))
	}
	if searched.At.Location() != time.UTC {
		t.Fatalf("activity time not UTC: %v", searched.At.Location())
	}
	if searched.At.Before(before.Add(-time.Second)) || searched.At.After(after.Add(time.Second)) {
		t.Fatalf("activity time %v outside [%v, %v]", searched.At, before, after)
	}
	if searched.LatencyMS < 0 {
		t.Fatalf("negative latency %d", searched.LatencyMS)
	}

	if want := string([]rune(fullErr)[:200]) + "…"; failed.Error != want {
		t.Fatalf("error not truncated to 200 runes: got %d runes", utf8.RuneCountInString(failed.Error))
	}
}
