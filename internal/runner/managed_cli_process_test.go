//go:build linux || darwin

package runner

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/managedcli"
)

func TestManagedEarlyControlFailureReapsProcessBeforeCompletion(t *testing.T) {
	root := t.TempDir()
	binary := managedTestBinary(t, root, "claude-code", "1.0.0")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ntrap 'printf terminated > terminated-marker; exit 0' TERM\nwhile :; do /bin/sleep 1; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeManagedTestInventory(t, root, managedcli.Inventory{"claude-code": {Version: "1.0.0", Binary: binary}})
	task := domain.RunnerTask{ID: "early-return", Provider: "claude-code"}
	var d *Daemon
	var completed <-chan CompleteRequest
	d, _, completed = managedTestDaemon(t, root, []domain.RunnerTask{task}, map[string]int{"control": http.StatusUnauthorized}, func() {
		if raw, err := os.ReadFile(filepath.Join(d.taskWorkspace(task), "terminated-marker")); err != nil || string(raw) != "terminated" {
			t.Errorf("completion preceded termination: %q %v", raw, err)
		}
		assertManagedMaintenance(t, root, true)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	claimed, found, err := d.claimTask(ctx, []string{"claude-code"}, 1)
	if err != nil || !found {
		t.Fatalf("claim found=%v err=%v", found, err)
	}
	done := make(chan struct{})
	go func() { d.executeTask(ctx, *claimed); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("early return blocked waiting for an already reaped child")
	}
	if req := <-completed; req.Status != domain.RunnerTaskFailed || req.Error != "runner session rejected" {
		t.Fatalf("completion=%+v", req)
	}
	assertManagedMaintenance(t, root, false)
}
