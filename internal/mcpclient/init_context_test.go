package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestMCPInitializationHonorsCallerCancellation(t *testing.T) {
	var started atomic.Int32
	release := make(chan struct{})
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad fixture request", http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "initialize":
			if started.Add(1) == 1 {
				<-release
			}
			writeJSON(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{}}`, req.ID))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		default:
			writeJSON(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"tools":[]}}`, req.ID))
		}
	}))
	defer endpoint.Close()
	defer close(release)
	client := New(endpoint.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := client.ListToolsContext(ctx)
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want caller deadline, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("initialization ignored the caller deadline")
	}
	if _, err := client.ListTools(); err != nil {
		t.Fatalf("fresh caller cannot retry initialization: %v", err)
	}
	if started.Load() != 2 {
		t.Fatalf("want two initialization requests, got %d", started.Load())
	}
}
