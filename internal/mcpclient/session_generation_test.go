package mcpclient

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// staleSessionFake hands out gen-N sessions and treats gen-1 as expired once
// the test flips the flag, holding both stale requests until released so the
// concurrent recovery is deterministic.
type staleSessionFake struct {
	mu       sync.Mutex
	inits    int
	expired  bool
	oldSeen  int
	bothOld  chan struct{}
	release  chan struct{}
	bothOnce sync.Once
}

func newStaleSessionFake() *staleSessionFake {
	return &staleSessionFake{
		bothOld: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (f *staleSessionFake) expire() {
	f.mu.Lock()
	f.expired = true
	f.mu.Unlock()
}

func (f *staleSessionFake) initCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inits
}

func (f *staleSessionFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
	}
	_ = json.Unmarshal(body, &req)
	write := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	switch req.Method {
	case "initialize":
		f.mu.Lock()
		f.inits++
		session := fmt.Sprintf("gen-%d", f.inits)
		f.mu.Unlock()
		w.Header().Set("Mcp-Session-Id", session)
		write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": "2024-11-05"}})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		session := r.Header.Get("Mcp-Session-Id")
		f.mu.Lock()
		expired := f.expired
		f.mu.Unlock()
		if expired && session == "gen-1" {
			f.mu.Lock()
			f.oldSeen++
			seen := f.oldSeen
			f.mu.Unlock()
			if seen >= 2 {
				f.bothOnce.Do(func() { close(f.bothOld) })
			}
			<-f.release
			w.WriteHeader(http.StatusNotFound)
			return
		}
		write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []map[string]any{{"name": "kb_search"}}}})
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func TestMCPClientConcurrent404ReinitializesOnce(t *testing.T) {
	fake := newStaleSessionFake()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	client := New(srv.URL, nil).WithTransport(srv.Client().Transport)
	if _, err := client.ListTools(); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	if got := client.currentSession(); got != "gen-1" {
		t.Fatalf("warmup session = %q", got)
	}

	fake.expire()
	before := fake.initCount()

	start := make(chan struct{})
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			_, err := client.ListToolsContext(nil)
			errs <- err
		}()
	}
	close(start)
	<-fake.bothOld
	close(fake.release)

	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent request after 404: %v", err)
		}
	}
	if reinit := fake.initCount() - before; reinit != 1 {
		t.Fatalf("initialize calls during recovery = %d, want 1", reinit)
	}
	if session := client.currentSession(); session != "gen-2" {
		t.Fatalf("final session = %q, want gen-2 (must not be cleared)", session)
	}
}
