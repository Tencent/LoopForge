//go:build linux || darwin

package runner

import (
	"agentflow/internal/domain"
	"agentflow/internal/managedcli"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedDockerAndExplicitBinaryStayUnmanaged(t *testing.T) {
	root := t.TempDir()
	binary := managedTestBinary(t, root, "claude-code", "1.0.0")
	d, _, _ := managedTestDaemon(t, root, nil, nil, nil)
	task := domain.RunnerTask{ID: "task", Provider: "claude-code", Invocation: &domain.RunnerInvocation{}}
	task.Invocation.Executor.Binary = "claude"
	d.managedTasks = map[string]*managedTask{"task": {inventory: managedcli.Inventory{"claude-code": {Version: "1.0.0", Binary: binary}}}}
	if got := d.invocationExecutor(task).Binary; got != binary {
		t.Fatalf("default executor=%s", got)
	}
	task.Invocation.Executor.Binary = "/custom/claude"
	if got := d.invocationExecutor(task).Binary; got != "/custom/claude" || len(d.taskManagedEnvironment(task)) != 0 {
		t.Fatalf("overrode explicit executor: %s", got)
	}
	task.Invocation.Executor.Binary = "claude"
	d.cfg.ExtraBinaries = map[string]string{"claude-code": "/explicit/claude"}
	if _, managed := d.managedBinary(task); managed {
		t.Fatal("overrode explicit runner binary")
	}
	d.cfg.ExtraBinaries = nil
	if err := json.Unmarshal([]byte(`{"sandbox":{"profile":{"driver":"docker"}}}`), &task); err != nil {
		t.Fatal(err)
	}
	if got := d.invocationExecutor(task).Binary; got != "claude" || len(d.taskManagedEnvironment(task)) != 0 {
		t.Fatalf("Docker received host binary/env: %s", got)
	}
	if got, ok := d.taskProviderBinary(task); !ok || got != "claude" {
		t.Fatalf("simple Docker command got host path: %s", got)
	}
}

func TestManagedEmptyRunPinNeverFallsBackToCurrentInventory(t *testing.T) {
	root := t.TempDir()
	host := t.TempDir()
	hostBinary := filepath.Join(host, "claude")
	if err := os.WriteFile(hostBinary, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", host)
	tasks := []domain.RunnerTask{{ID: "one", RunID: "old", Provider: "claude-code"}, {ID: "two", RunID: "old", Provider: "claude-code"}}
	d, _, _ := managedTestDaemon(t, root, tasks, nil, nil)
	task, _, err := d.claimTask(context.Background(), []string{"claude-code"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	d.releaseManagedTask(task.ID)
	binary := managedTestBinary(t, root, "claude-code", "1.0.0")
	writeManagedTestInventory(t, root, managedcli.Inventory{"claude-code": {Version: "1.0.0", Binary: binary}})
	d.runtimes = []domain.RunnerRuntime{{Provider: "claude-code", BinaryPath: binary}}
	task, _, err = d.claimTask(context.Background(), []string{"claude-code"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer d.releaseManagedTask(task.ID)
	if got, ok := d.taskProviderBinary(*task); !ok || got != hostBinary || len(d.taskManagedEnvironment(*task)) != 0 {
		t.Fatalf("empty old pin followed managed current: %s", got)
	}
}

func TestManagedDetectionReportsRealVersionAndRespectsExplicitBinary(t *testing.T) {
	t.Setenv("DISABLE_AUTOUPDATER", "user-auto")
	t.Setenv("DISABLE_UPDATES", "user-updates")
	root := t.TempDir()
	inventory := managedcli.Inventory{}
	for _, provider := range []string{"claude-code", "codebuddy", "codex"} {
		inventory[provider] = managedcli.Installation{Version: "1.0.0", Binary: managedTestBinary(t, root, provider, "1.0.0")}
	}
	for _, rt := range detectRuntimes(context.Background(), nil, inventory) {
		if rt.Provider == "mock" || rt.Provider == "terminal" {
			continue
		}
		if !strings.HasPrefix(rt.Version, "actual-1.0.0|") || rt.BinaryPath != inventory[rt.Provider].Binary {
			t.Fatalf("not actual managed runtime: %+v", rt)
		}
		if rt.Provider != "codex" && rt.Version != "actual-1.0.0|1|1" {
			t.Fatalf("probe environment: %+v", rt)
		}
	}
	explicit := managedTestBinary(t, root, "claude-code", "9.0.0")
	for _, rt := range detectRuntimes(context.Background(), map[string]string{"claude-code": explicit}, inventory) {
		if rt.Provider == "claude-code" && (rt.BinaryPath != explicit || rt.Version != "actual-9.0.0|user-auto|user-updates") {
			t.Fatalf("explicit probe overridden: %+v", rt)
		}
	}
}
