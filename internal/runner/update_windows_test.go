package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestWindowsRunnerClaimsWorkWithoutAutomaticUpdates(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("disable-auto-update=%t", disabled), func(t *testing.T) {
			t.Setenv("AGENTFLOW_GATE_MCP_BINARY", filepath.Join(t.TempDir(), "agentflow-gate-mcp.exe"))
			var claims, preflights, nonblockingClaims, updates atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/runners/tasks/claim" {
					updates.Add(1)
					// A regression must not try to replace the running test executable.
					http.Error(w, "unexpected update request", http.StatusServiceUnavailable)
					return
				}
				var request ClaimRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode claim: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if request.Preflight {
					preflights.Add(1)
					fmt.Fprint(w, `{"taskAvailable":true}`)
					return
				}
				if request.NoWait {
					nonblockingClaims.Add(1)
				}
				fmt.Fprintf(w, `{"task":{"id":"task-%d","provider":"mock"}}`, claims.Add(1))
			}))
			defer server.Close()
			d, err := New(Config{
				ServerURL: server.URL, BootstrapToken: "test",
				StatePath: filepath.Join(t.TempDir(), "state.json"), DefaultWorkspaceRoot: t.TempDir(),
				DisableAutoUpdate: disabled,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for i := 1; i <= 2; i++ {
				task, found, err := d.claimTask(ctx, []string{"mock"}, 1)
				if err != nil || !found || task == nil || task.ID != fmt.Sprintf("task-%d", i) {
					t.Fatalf("claim %d: task=%+v found=%v err=%v", i, task, found, err)
				}
				atomic.StoreInt32(&d.running, 0)
			}
			if updates.Load() != 0 || preflights.Load() != 0 || nonblockingClaims.Load() != 0 {
				t.Fatalf("Windows automatic update path used: updates=%d preflights=%d nonblocking claims=%d", updates.Load(), preflights.Load(), nonblockingClaims.Load())
			}
			if !d.cfg.DisableAutoUpdate || d.updateReady.Load() {
				t.Fatalf("Windows update state: disabled=%v ready=%v", d.cfg.DisableAutoUpdate, d.updateReady.Load())
			}
		})
	}
}
