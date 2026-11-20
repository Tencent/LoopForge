package mcpclient

import (
	"strings"
	"testing"
)

func TestMCPStreamAcceptsDataFieldsWithoutSpaces(t *testing.T) {
	client := New("http://example.test", nil)
	stream := "event: message\ndata:{\"jsonrpc\":\"2.0\",\"id\":2,\ndata: \"result\":{\"ok\":true}}\n\n"
	payload, err := client.readSSE(strings.NewReader(stream))
	if err != nil || !strings.Contains(string(payload), `"ok":true`) {
		t.Fatalf("valid SSE result was dropped: %s, %v", payload, err)
	}
}
