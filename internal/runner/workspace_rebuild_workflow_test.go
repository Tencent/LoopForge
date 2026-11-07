package runner

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// The checkbox changes reconstruction, not normal SOP scheduling: each marked
// script must still be dispatched once in its original graph position.
func TestRunnerRebuildCheckboxPreservesNormalSOPScriptOrder(t *testing.T) {
	server, store, engine := newDaemonTestServer(t)
	daemon, err := New(Config{ServerURL: server.URL, BootstrapToken: "boot-secret", StatePath: filepath.Join(t.TempDir(), "state.json"), Name: "rebuild-checkbox-runner", MaxConcurrency: 1, HeartbeatInterval: time.Second, LongPollTimeout: 2 * time.Second, DefaultWorkspaceRoot: filepath.Join(t.TempDir(), "workspace")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runner shutdown did not finish")
		}
	}()
	waitFor(t, 5*time.Second, "runner registration and control", func() bool { return len(store.Runners()) == 1 && daemon.wsOnline.Load() })
	runner := store.Runners()[0]
	runner.OwnerUserID = 42
	if err := store.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDefaultRunner(42, runner.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	workflow := domain.WorkflowDefinition{ID: "rebuild-checkbox-normal-order", Name: "normal order", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "one", Name: "One", Type: "script", ScriptLanguage: "shell", RunOnWorkspaceRebuild: true, Script: "printf '1\\n' >> normal-order", MaxAttempts: 1},
		{ID: "two", Name: "Two", Type: "script", ScriptLanguage: "shell", RunOnWorkspaceRebuild: true, Script: "test \"$(cat normal-order)\" = 1 || exit 7; printf '2\\n' >> normal-order", MaxAttempts: 1},
		{ID: "check", Name: "Check", Type: "script", ScriptLanguage: "shell", Script: "test \"$(cat normal-order)\" = \"$(printf '1\\n2')\" || exit 8; printf graph-ok", MaxAttempts: 1},
	}, Edges: []domain.WorkflowEdge{{ID: "one-two", Source: "one", Target: "two", When: "success"}, {ID: "two-check", Source: "two", Target: "check", When: "success"}}}
	if err := store.PutWorkflow(workflow); err != nil {
		t.Fatal(err)
	}
	run, err := engine.StartWithExecutionContext(workflow.ID, "", domain.RepositoryContext{}, domain.TelemetryContext{ResponsibleUserID: 42})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "normal SOP order", func() bool {
		current, ok := store.Run(run.ID)
		return ok && (current.Status == "succeeded" || current.Status == "failed")
	})
	final, _ := store.Run(run.ID)
	if final.Status != "succeeded" {
		t.Fatalf("normal execution failed: one=%+v two=%+v check=%+v", final.Nodes["one"], final.Nodes["two"], final.Nodes["check"])
	}
	if !strings.Contains(final.Nodes["check"].Output, "graph-ok") {
		t.Fatalf("normal execution order was changed: %q", final.Nodes["check"].Output)
	}
	count := map[string]int{}
	for _, task := range store.RunnerTasks() {
		if task.RunID == run.ID && task.Action == "" {
			count[task.NodeID]++
		}
	}
	if count["one"] != 1 || count["two"] != 1 || count["check"] != 1 {
		t.Fatalf("normal script dispatch was skipped or duplicated: %v", count)
	}
}
