package mcpclient

import (
	"net/http"
	"strings"
	"testing"
)

func TestMCPToolResultEnvelope(t *testing.T) {
	t.Run("error without text", func(t *testing.T) {
		_, client := newFake(t, func(w http.ResponseWriter, id int64) {
			writeJSON(w, `{"jsonrpc":"2.0","id":2,"result":{"isError":true,"content":[]}}`)
		})
		out, err := client.CallTool("fixture", nil)
		if err == nil || !strings.Contains(err.Error(), "fixture") || out != "" {
			t.Fatalf("expected a tool error, got %q, %v", out, err)
		}
	})
	t.Run("structured result without text", func(t *testing.T) {
		_, client := newFake(t, func(w http.ResponseWriter, id int64) {
			writeJSON(w, `{"jsonrpc":"2.0","id":2,"result":{"structuredContent":{"count":3}}}`)
		})
		out, err := client.CallTool("fixture", nil)
		if err != nil || out != `{"count":3}` {
			t.Fatalf("expected structured JSON, got %q, %v", out, err)
		}
	})
}
