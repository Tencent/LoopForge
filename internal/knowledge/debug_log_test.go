package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestDebugLogsCaptureMCPFilterAndRedactedPayloads(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "debug-log-test-secret")
	t.Setenv("LOOPFORGE_KNOWLEDGE_DEBUG", "true")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": "2024-11-05"}})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"tools": []map[string]any{
						{"name": "feishu_search", "annotations": map[string]any{"readOnlyHint": true}},
						{"name": "feishu_mutate", "annotations": map[string]any{"readOnlyHint": false}},
						{"name": "feishu_write", "annotations": map[string]any{"destructiveHint": true}},
					},
				},
			})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{"content": []map[string]any{
					{"type": "text", "text": `{"results":[{"source":"s1","title":"Airtrain.md","snippet":"echo super-secret-token-value"}]}`},
				}},
			})
		}
	}))
	defer srv.Close()

	var buf bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prevOut)

	svc := NewService(newMemoryConnectorStore(), func() string { return "dbg" })
	conn, err := svc.CreateConnector(context.Background(), 1, ConnectorInput{
		Name:     "Feishu Debug",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srv.URL,
		Secret:   domain.KnowledgeConnectorSecret{Token: "super-secret-token-value"},
	})
	if err != nil {
		t.Fatal(err)
	}

	exec := NewExecutor(svc)
	ref := domain.KnowledgeRef{ConnectorID: conn.ID, Mode: "all"}
	_, tools, err := exec.ReferenceTools(context.Background(), 1, ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "feishu_search" {
		t.Fatalf("unexpected tools: %+v", tools)
	}

	_, _, err = exec.ExecuteReferenceTool(context.Background(), 1, ref, "feishu_search", map[string]any{"query": "Airtrain", "leak": "super-secret-token-value"}, domain.KnowledgeCallOriginModelTool)
	if err != nil {
		t.Fatal(err)
	}

	logs := buf.String()
	if !strings.Contains(logs, "knowledge-mcp-tools") ||
		!strings.Contains(logs, "feishu_mutate(readOnlyHint=false)") ||
		!strings.Contains(logs, "feishu_write(destructiveHint)") {
		t.Fatalf("expected filter reasons in logs, got:\n%s", logs)
	}
	if !strings.Contains(logs, "knowledge-debug-call") ||
		!strings.Contains(logs, `"query":"Airtrain"`) ||
		!strings.Contains(logs, "Airtrain.md") {
		t.Fatalf("expected request & response debug payload in logs, got:\n%s", logs)
	}
	if strings.Contains(logs, "super-secret-token-value") {
		t.Fatalf("secret leaked into debug logs:\n%s", logs)
	}
}
