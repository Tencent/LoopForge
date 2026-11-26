package mcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInitializeFallbackAndStructuredContent(t *testing.T) {
	var initVersions []string
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
		switch req.Method {
		case "initialize":
			v, _ := req.Params["protocolVersion"].(string)
			initVersions = append(initVersions, v)
			if v == "2025-06-18" {
				// Simulate legacy server rejecting 2025-06-18
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": req.ID,
					"error": map[string]any{"code": -32602, "message": "unsupported protocolVersion"},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"protocolVersion": "2024-11-05"},
			})
		case "notifications/initialized":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{
					"content":           []map[string]any{{"type": "text", "text": "正文摘要"}},
					"structuredContent": map[string]any{"document_id": "d-1", "title": "结构化标题"},
				},
			})
		}
	}))
	defer srv.Close()

	c := New(srv.URL, nil).WithTransport(srv.Client().Transport)
	text, structured, err := c.CallToolStructuredContext(context.Background(), "get_content", map[string]any{"file_id": "d-1"})
	if err != nil {
		t.Fatalf("CallToolStructuredContext: %v", err)
	}
	if len(initVersions) != 2 || initVersions[0] != "2025-06-18" || initVersions[1] != "2024-11-05" {
		t.Fatalf("expected initialize fallback [2025-06-18, 2024-11-05], got %v", initVersions)
	}
	if text != "正文摘要" {
		t.Fatalf("unexpected text: %q", text)
	}
	if string(structured) == "" {
		t.Fatalf("expected structuredContent to be populated")
	}
}
