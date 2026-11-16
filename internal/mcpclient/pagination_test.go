package mcpclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListToolsFollowsNextCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				Cursor string `json:"cursor"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch request.Method {
		case "initialize":
			writeJSON(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{}}`, request.ID))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if request.Params.Cursor == "" {
				writeJSON(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"one"}],"nextCursor":"next/+="}}`, request.ID))
			} else if request.Params.Cursor == "next/+=" {
				writeJSON(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"two"}]}}`, request.ID))
			} else {
				http.Error(w, "unexpected cursor", http.StatusBadRequest)
			}
		}
	}))
	defer server.Close()

	tools, err := New(server.URL, nil).ListTools()
	if err != nil || len(tools) != 2 || tools[0].Name != "one" || tools[1].Name != "two" {
		t.Fatalf("expected both catalog pages, got %v, %v", tools, err)
	}
}
