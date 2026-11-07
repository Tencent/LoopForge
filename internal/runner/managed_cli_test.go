//go:build linux || darwin

package runner

import (
	"agentflow/internal/domain"
	"agentflow/internal/managedcli"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func managedTestDaemon(t *testing.T, root string, tasks []domain.RunnerTask, status map[string]int, onComplete func()) (*Daemon, *atomic.Int32, <-chan CompleteRequest) {
	t.Helper()
	claims := &atomic.Int32{}
	completed := make(chan CompleteRequest, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		action := filepath.Base(r.URL.Path)
		n := -1
		if action == "claim" {
			n = int(claims.Add(1)) - 1
		}
		if code := status[action]; code != 0 {
			w.WriteHeader(code)
			return
		}
		switch action {
		case "claim":
			if n >= len(tasks) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"task": tasks[n]})
		case "complete":
			if onComplete != nil {
				onComplete()
			}
			var req CompleteRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			completed <- req
			_, _ = w.Write([]byte("{}"))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	t.Cleanup(server.Close)
	d, err := New(Config{ServerURL: server.URL, BootstrapToken: "test", StatePath: filepath.Join(t.TempDir(), "runner.json"), DefaultWorkspaceRoot: t.TempDir(), DisableAutoUpdate: true, ManagedCLIRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	d.state.RunnerID = "runner-test"
	return d, claims, completed
}

func TestManagedClaimOptInAndMaintenanceGate(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("AGENTFLOW_MANAGED_CLI_ROOT", root)
			configuredRoot := ""
			if enabled {
				configuredRoot = root
			}
			d, claims, _ := managedTestDaemon(t, configuredRoot, []domain.RunnerTask{{ID: "one", Provider: "mock"}}, nil, nil)
			if err := managedcli.WithMaintenance(context.Background(), root, func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
				defer cancel()
				task, found, err := d.claimTask(ctx, []string{"mock"}, 1)
				if enabled {
					if !errors.Is(err, context.DeadlineExceeded) || task != nil || found || claims.Load() != 0 {
						t.Errorf("claim crossed gate: task=%v found=%v err=%v requests=%d", task, found, err, claims.Load())
					}
				} else if err != nil || !found || task == nil || claims.Load() != 1 {
					t.Errorf("default runner changed: task=%v found=%v err=%v", task, found, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManagedClaimNoTaskOrErrorReleasesLocks(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			root := t.TempDir()
			d, _, _ := managedTestDaemon(t, root, nil, map[string]int{"claim": status}, nil)
			_, found, err := d.claimTask(context.Background(), []string{"mock"}, 1)
			if found || (status == http.StatusInternalServerError) != (err != nil) {
				t.Fatalf("found=%v err=%v", found, err)
			}
			assertManagedMaintenance(t, root, false)
		})
	}
}

func TestManagedPinFailureCompletesTaskBeforeReleasingUsage(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "pins")); err != nil {
		t.Fatal(err)
	}
	d, _, completed := managedTestDaemon(t, root, []domain.RunnerTask{{ID: "bad-pin", Provider: "claude-code"}}, nil, func() {
		assertManagedMaintenance(t, root, true)
	})
	task, found, err := d.claimTask(context.Background(), []string{"claude-code"}, 1)
	if err != nil || !found || task == nil {
		t.Fatalf("claimed task lost: found=%v err=%v", found, err)
	}
	d.executeTask(context.Background(), *task)
	req := <-completed
	if req.Status != domain.RunnerTaskFailed || !strings.Contains(req.Error, "pin managed CLI inventory") {
		t.Fatalf("completion=%+v", req)
	}
	assertManagedMaintenance(t, root, false)
	if d.managedTaskState(task.ID) != nil || atomic.LoadInt32(&d.running) != 0 {
		t.Fatal("task state leaked")
	}
}

func assertManagedMaintenance(t *testing.T, root string, blocked bool) {
	t.Helper()
	timeout := time.Second
	if blocked {
		timeout = 80 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := managedcli.WithMaintenance(ctx, root, func() error { return nil })
	if (blocked && !errors.Is(err, context.DeadlineExceeded)) || (!blocked && err != nil) {
		t.Errorf("maintenance blocked=%t: %v", blocked, err)
	}
}

func writeManagedTestInventory(t *testing.T, root string, inventory managedcli.Inventory) {
	t.Helper()
	raw, err := json.Marshal(managedcli.State{Current: inventory})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func managedTestBinary(t *testing.T, root, provider, version string) string {
	t.Helper()
	binary := filepath.Join(root, "providers", provider, version, defaultProviderBinary(provider))
	if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf 'actual-" + version + "|%s|%s\\n' \"$DISABLE_AUTOUPDATER\" \"$DISABLE_UPDATES\"; exit 0; fi\nprintf '%s|%s|%s' \"$0\" \"$DISABLE_AUTOUPDATER\" \"$DISABLE_UPDATES\" > execution-marker\n"
	if err := os.WriteFile(binary, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return binary
}
