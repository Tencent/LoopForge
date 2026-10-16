package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunnerUpdateReplacesBinaryOnlyAfterVerification(t *testing.T) {
	newBinary := []byte("new runner")
	hash := sha256.Sum256(newBinary)
	expected := hex.EncodeToString(hash[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runners/agentflow-runner" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("metadata") == "1" {
			fmt.Fprintf(w, `{"sha256":%q}`, expected)
			return
		}
		w.Write(newBinary)
	}))
	defer server.Close()
	d, err := New(Config{ServerURL: server.URL, BootstrapToken: "test"})
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "agentflow-runner")
	if err := os.WriteFile(binary, []byte("old runner"), 0o755); err != nil {
		t.Fatal(err)
	}
	atomic.StoreInt32(&d.running, 1)
	if err := d.checkForUpdate(context.Background(), binary); err != nil {
		t.Fatal(err)
	}
	if d.updateReady.Load() {
		t.Fatal("busy runner must not update")
	}
	atomic.StoreInt32(&d.running, 0)
	if err := d.checkForUpdate(context.Background(), binary); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newBinary) || !d.updateReady.Load() {
		t.Fatalf("binary=%q updateReady=%v", got, d.updateReady.Load())
	}
}

func TestRunnerUpdateRejectsChecksumMismatch(t *testing.T) {
	newBinary := []byte("new runner")
	hash := sha256.Sum256(newBinary)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("metadata") == "1" {
			fmt.Fprintf(w, `{"sha256":%q}`, hex.EncodeToString(hash[:]))
			return
		}
		w.Write([]byte("different payload"))
	}))
	defer server.Close()
	d, err := New(Config{ServerURL: server.URL, BootstrapToken: "test"})
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "agentflow-runner")
	if err := os.WriteFile(binary, []byte("old runner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.checkForUpdate(context.Background(), binary); err == nil {
		t.Fatal("expected checksum mismatch")
	}
	got, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old runner" || d.updateReady.Load() {
		t.Fatalf("binary changed after failed verification: %q", got)
	}
}

func TestHeartbeatRenewalPersistsExistingRunnerIdentity(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	d, err := New(Config{ServerURL: "https://runner.example", BootstrapToken: "one-time-code", StatePath: statePath})
	if err != nil {
		t.Fatal(err)
	}
	oldExpiry := time.Now().Add(6 * 24 * time.Hour)
	if err := d.saveState(daemonState{RunnerID: "runner-1", SessionToken: "session-1", ExpiresAt: oldExpiry}); err != nil {
		t.Fatal(err)
	}
	newExpiry := time.Now().Add(30 * 24 * time.Hour)
	d.refreshSessionExpiry(newExpiry)
	got, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var saved daemonState
	if err := json.Unmarshal(got, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.RunnerID != "runner-1" || saved.SessionToken != "session-1" || !saved.ExpiresAt.Equal(newExpiry) {
		t.Fatalf("renewed state = %+v", saved)
	}
}

func TestGateMCPHelperUpdatesWhileRunnerIdle(t *testing.T) {
	payload := []byte("new gate helper")
	hash := sha256.Sum256(payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/runners/agentflow-gate-mcp") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("metadata") == "1" {
			fmt.Fprintf(w, `{"sha256":%q}`, hex.EncodeToString(hash[:]))
			return
		}
		w.Write(payload)
	}))
	defer server.Close()
	d, err := New(Config{ServerURL: server.URL, BootstrapToken: "test"})
	if err != nil {
		t.Fatal(err)
	}
	pattern := filepath.Join(t.TempDir(), "agentflow-gate-mcp-{os}-{arch}")
	t.Setenv("AGENTFLOW_GATE_MCP_BINARY", pattern)
	atomic.StoreInt32(&d.running, 1)
	if err := d.checkGateMCPUpdates(context.Background()); err != nil {
		t.Fatal(err)
	}
	hostPath := strings.ReplaceAll(strings.ReplaceAll(pattern, "{os}", runtime.GOOS), "{arch}", runtime.GOARCH)
	if _, err := os.Stat(hostPath); !os.IsNotExist(err) {
		t.Fatalf("busy runner updated helper: %v", err)
	}
	atomic.StoreInt32(&d.running, 0)
	if err := d.checkGateMCPUpdates(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, targetOS := range []string{runtime.GOOS, "linux"} {
		if targetOS == "linux" && runtime.GOOS != "darwin" {
			continue
		}
		path := strings.ReplaceAll(strings.ReplaceAll(pattern, "{os}", targetOS), "{arch}", runtime.GOARCH)
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(payload) {
			t.Fatalf("helper %s = %q, %v", targetOS, got, err)
		}
	}
}
