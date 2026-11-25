package mcpclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func TestListToolsBoundsMalformedPagination(t *testing.T) {
	for _, tc := range []struct {
		name         string
		repeated     bool
		terminal     bool
		wantRequests int
		wantError    string
	}{
		{name: "repeated cursor", repeated: true, wantRequests: 2, wantError: "repeated its pagination cursor"},
		{name: "unique cursors", wantRequests: 100, wantError: "exceeded 100 pages"},
		{name: "terminal page at limit", terminal: true, wantRequests: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID     int64  `json:"id"`
					Method string `json:"method"`
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
					requestNumber := requests.Add(1)
					// Bound the unfixed client too, so the regression cannot hang.
					if requestNumber > 100 {
						http.Error(w, "test server page safety limit", http.StatusBadRequest)
						return
					}
					next := fmt.Sprintf("page-%d", requestNumber)
					if tc.repeated {
						next = "same-cursor"
					}
					if tc.terminal && requestNumber == 100 {
						next = ""
					}
					writeJSON(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"tool-%d"}],"nextCursor":%q}}`, request.ID, requestNumber, next))
				}
			}))
			defer server.Close()
			tools, err := New(server.URL, nil).ListTools()
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("expected error %q, got %v", tc.wantError, err)
				}
				if tools != nil {
					t.Fatalf("partial catalog must not escape: %v", tools)
				}
			} else if err != nil || len(tools) != 100 {
				t.Fatalf("expected complete 100-page catalog, got %d tools, %v", len(tools), err)
			}
			if got := requests.Load(); got != int64(tc.wantRequests) {
				t.Fatalf("got %d requests, want %d", got, tc.wantRequests)
			}
		})
	}
}
