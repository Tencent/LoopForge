package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

func TestMCPToolsCacheAndRetryAndErrorClassification(t *testing.T) {
	ResetMCPToolsCacheForTest()
	origDelay := mcpRetryBaseDelay
	mcpRetryBaseDelay = 10 * time.Millisecond
	defer func() { mcpRetryBaseDelay = origDelay }()

	var listCalls atomic.Int32
	var searchCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		reply := func(res any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": res})
		}
		switch req.Method {
		case "initialize":
			reply(map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}})
		case "notifications/initialized":
			reply(map[string]any{})
		case "tools/list":
			listCalls.Add(1)
			reply(map[string]any{"tools": []map[string]any{
				{"name": "manage.search_file", "description": "Search"},
				{"name": "get_content", "description": "Read"},
			}})
		case "tools/call":
			name, _ := req.Params["name"].(string)
			switch name {
			case "manage.search_file":
				n := searchCalls.Add(1)
				if n == 1 {
					// Simulate transient 429 rate limit on first attempt
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(`{"error":"rate limited"}`))
					return
				}
				reply(map[string]any{
					"content":           []map[string]any{{"type": "text", "text": "ok"}},
					"structuredContent": map[string]any{"file_list": []map[string]any{{"file_id": "f1", "file_name": "发布指南.md"}}},
				})
			case "get_content":
				// Simulate JSON-RPC -32602 invalid params
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": req.ID,
					"error": map[string]any{"code": -32602, "message": "missing required field file_id"},
				})
			}
		}
	}))
	defer srv.Close()

	svc := NewService(newMemoryConnectorStore(), func() string { return "conn_opt" })
	conn, err := svc.CreateConnector(context.Background(), 1, ConnectorInput{
		Name:     "腾讯文档",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srv.URL,
	})
	if err != nil {
		t.Fatalf("CreateConnector: %v", err)
	}
	exec := NewExecutor(svc)
	ref := domain.KnowledgeRef{ConnectorID: conn.ID, Mode: "all"}

	// First call: triggers 1 tools/list + 429 retry -> succeeds with structuredContent evidence
	read, _, err := exec.ExecuteReferenceTool(context.Background(), 1, ref, "manage.search_file", map[string]any{"query": "发布"}, "")
	if err != nil {
		t.Fatalf("expected 429 retry to succeed, got err: %v", err)
	}
	if searchCalls.Load() != 2 {
		t.Fatalf("expected 2 attempts (1 retry after 429), got %d", searchCalls.Load())
	}
	if len(read.Evidences) != 1 || read.Evidences[0].FileName != "发布指南.md" {
		t.Fatalf("expected structuredContent evidence, got %+v", read.Evidences)
	}

	// Second call: tools/list must hit cache (still 1 upstream call), and -32602 should classify as param error
	_, _, err = exec.ExecuteReferenceTool(context.Background(), 1, ref, "get_content", map[string]any{}, "")
	if err == nil || !strings.Contains(err.Error(), "知识库工具参数错误") || !strings.Contains(err.Error(), "file_id") {
		t.Fatalf("expected classified param error, got %v", err)
	}
	if listCalls.Load() != 1 {
		t.Fatalf("expected tools/list cached at 1 call, got %d", listCalls.Load())
	}

	// Check() must bypass cache and refresh upstream
	res := svc.CheckConnector(context.Background(), 1, conn.ID, nil)
	if !res.OK {
		t.Fatalf("expected CheckConnector OK, got %+v", res)
	}
	if listCalls.Load() != 2 {
		t.Fatalf("expected Check() to bypass tools/list cache (2 calls), got %d", listCalls.Load())
	}
}

func TestIsKnowledgeToolMayWriteAndClientReuseByConnectorID(t *testing.T) {
	if !IsKnowledgeToolMayWrite(mcpclient.Tool{Name: "doc.delete_file"}) {
		t.Fatalf("expected doc.delete_file to be classified as write")
	}
	if !IsKnowledgeToolMayWrite(mcpclient.Tool{Name: "updatePage"}) {
		t.Fatalf("expected updatePage to be classified as write")
	}
	if IsKnowledgeToolMayWrite(mcpclient.Tool{Name: "manage.search_file"}) {
		t.Fatalf("expected manage.search_file to be read-only")
	}
	if IsKnowledgeToolMayWrite(mcpclient.Tool{Name: "get_content"}) {
		t.Fatalf("expected get_content to be read-only")
	}

	c1 := getOrCreateMCPClient(1, "conn_reuse", "http://example.invalid/mcp", map[string]string{"Authorization": "Bearer tok1"}, CallTimeout)
	c2 := getOrCreateMCPClient(1, "conn_reuse", "http://example.invalid/mcp", map[string]string{"Authorization": "Bearer tok2"}, CallTimeout)
	if c1 != c2 {
		t.Fatalf("expected same *mcpclient.Client reused across token refresh for same connectorID")
	}
	if got := c2.Header("Authorization"); got != "Bearer tok2" {
		t.Fatalf("expected updated Authorization header, got %q", got)
	}
	cOtherUser := getOrCreateMCPClient(2, "conn_reuse", "http://example.invalid/mcp", map[string]string{"Authorization": "Bearer tok_user2"}, CallTimeout)
	if cOtherUser == c1 {
		t.Fatalf("expected different users to have isolated *mcpclient.Client instances")
	}
}
