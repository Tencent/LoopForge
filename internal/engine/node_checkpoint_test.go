package engine

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestCheckpointRetryStrategies(t *testing.T) {
	for _, strategy := range []string{"full_redo", "targeted_fix"} {
		t.Run(strategy, func(t *testing.T) {
			store, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			manager := runtime.NewManager(t.TempDir())
			sandbox, err := manager.AcquireRun(context.Background(), "retry", domain.SandboxProfile{Driver: "local"}, runtime.SandboxRequirements{}, runtime.RunCredentials{}, runtime.SandboxResources{})
			if err != nil {
				t.Fatal(err)
			}
			defer sandbox.Close(context.Background())
			workspace := sandbox.Workspace()
			git := func(args ...string) {
				t.Helper()
				out, err := exec.Command("git", append([]string{"-C", workspace}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %s: %v", args, out, err)
				}
			}
			write := func(name, contents string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(workspace, name), []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			git("init", "-q")
			write("y", "before")
			write("z", "before")
			git("add", ".")
			git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "base")
			run := domain.Run{ID: "retry", Status: "running", Generation: 1, Repository: domain.RepositoryContext{RepositoryID: "repo"}, Nodes: map[string]*domain.NodeRun{"a": {NodeID: "a", Status: "running", Attempt: 1}}}
			if err := store.PutRun(run); err != nil {
				t.Fatal(err)
			}
			e := New(store, manager)
			sb := &runEnvironment{sandbox: sandbox}
			lease, err := store.BeginWorkspaceUse(run.ID, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer store.FinishWorkspaceUse(run.ID, lease, time.Now().UTC(), nil, "")
			ctx := context.WithValue(context.Background(), nodeCheckpointLeaseKey{}, lease)
			node := domain.WorkflowNode{ID: "a", RetryStrategy: strategy}
			if err := e.prepareNodeCheckpoint(ctx, run, node, 1, 1, sb); err != nil {
				t.Fatal(err)
			}
			write("x", "new")
			write("y", "changed")
			if err := os.Remove(filepath.Join(workspace, "z")); err != nil {
				t.Fatal(err)
			}
			if err := e.finishNodeCheckpoint(ctx, run, node, 1, 1, sb); err != nil {
				t.Fatal(err)
			}
			current, _ := store.RunState(run.ID)
			current.Nodes["a"].Attempt = 2
			if err := store.PutRun(current); err != nil {
				t.Fatal(err)
			}
			if err := e.prepareNodeCheckpoint(ctx, current, node, 2, 1, sb); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct{ name, full, targeted string }{{"x", "", "new"}, {"y", "before", "changed"}, {"z", "before", ""}} {
				want := tc.full
				if strategy == "targeted_fix" {
					want = tc.targeted
				}
				got, err := os.ReadFile(filepath.Join(workspace, tc.name))
				if want == "" && os.IsNotExist(err) {
					continue
				}
				if err != nil || string(got) != want {
					t.Fatalf("%s = %q, %v; want %q", tc.name, got, err, want)
				}
			}
		})
	}
}

func TestRejectRestoresImplementationAfterReadOnlyReview(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := runtime.NewManager(t.TempDir())
	sandbox, err := manager.AcquireRun(context.Background(), "reject", domain.SandboxProfile{Driver: "local"}, runtime.SandboxRequirements{}, runtime.RunCredentials{}, runtime.SandboxResources{})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close(context.Background())
	root := sandbox.Workspace()
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	git("init", "-q")
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "base")
	e := New(store, manager)
	e.ConfigureExecution("test", "test", time.Minute, false)
	wf := domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "implement", Type: "script", RetryStrategy: "targeted_fix"}, {ID: "review", Type: "agent"}}, Edges: []domain.WorkflowEdge{{Source: "implement", Target: "review"}}}
	run := domain.Run{ID: "reject", Status: "waiting", Generation: 1, WorkflowSnapshot: wf, Repository: domain.RepositoryContext{RepositoryID: "repo"}, ExecutionContext: domain.TelemetryContext{NodeCheckpointsEnabled: true}, Nodes: map[string]*domain.NodeRun{"implement": {NodeID: "implement", Status: "succeeded", Attempt: 1}, "review": {NodeID: "review", Status: "waiting_approval", Attempt: 1}}}
	start, err := runtime.CreateNodeWorkspaceCheckpoint(context.Background(), root, run.RepositoryContexts(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("implementation"), 0o600); err != nil {
		t.Fatal(err)
	}
	end, err := runtime.CreateNodeWorkspaceCheckpoint(context.Background(), root, run.RepositoryContexts(), 1)
	if err != nil {
		t.Fatal(err)
	}
	start.End = end.Repositories
	review, err := runtime.CreateNodeWorkspaceCheckpoint(context.Background(), root, run.RepositoryContexts(), 1)
	if err != nil {
		t.Fatal(err)
	}
	review.End = review.Repositories
	run.Nodes["implement"].Checkpoints = []domain.NodeCheckpoint{start}
	run.Nodes["review"].Checkpoints = []domain.NodeCheckpoint{review}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := e.Reject(run.ID, domain.RejectRequest{NodeID: "review", TargetID: "implement", RestoreWorkspace: true}); err != nil {
		t.Fatal(err)
	}
	current, _ := store.RunState(run.ID)
	if current.Nodes["review"].Rewound {
		t.Fatal("downstream marked rewound before workspace restore")
	}
	current.Nodes["implement"].Status = "running"
	current.Nodes["implement"].Attempt = 2
	if err := store.PutRun(current); err != nil {
		t.Fatal(err)
	}
	lease, err := store.BeginWorkspaceUse(run.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer store.FinishWorkspaceUse(run.ID, lease, time.Now().UTC(), nil, "")
	ctx := context.WithValue(context.Background(), nodeCheckpointLeaseKey{}, lease)
	if err := e.prepareNodeCheckpoint(ctx, current, wf.Nodes[0], 2, 1, &runEnvironment{sandbox: sandbox}); err != nil {
		t.Fatal(err)
	}
	current, _ = store.RunState(run.ID)
	if !current.Nodes["review"].Rewound {
		t.Fatal("successful restore did not mark downstream rewound")
	}
	got, err := os.ReadFile(file)
	if err != nil || string(got) != "before" {
		t.Fatalf("implementation not restored: %q, %v", got, err)
	}
}

func TestCheckpointRecoveryRequiresMatchingRepositoryHeads(t *testing.T) {
	before := domain.NodeCheckpoint{Repositories: []domain.NodeRepositoryCheckpoint{{RepositoryID: "repo", WorkspaceDir: "src", Head: "one", Branch: "work"}}}
	for _, tc := range []struct {
		name  string
		other domain.NodeCheckpoint
		want  bool
	}{
		{"same", before, true},
		{"head changed", domain.NodeCheckpoint{Repositories: []domain.NodeRepositoryCheckpoint{{RepositoryID: "repo", WorkspaceDir: "src", Head: "two", Branch: "work"}}}, false},
		{"branch changed", domain.NodeCheckpoint{Repositories: []domain.NodeRepositoryCheckpoint{{RepositoryID: "repo", WorkspaceDir: "src", Head: "one", Branch: "other"}}}, false},
		{"repository missing", domain.NodeCheckpoint{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameCheckpointHeads(before, tc.other); got != tc.want {
				t.Fatalf("sameCheckpointHeads = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCheckpointRejectsChangesFromSharedChildRun(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := runtime.NewManager(t.TempDir())
	root := manager.RunWorkspace("parent")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	git("init", "-q")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "base")
	target, err := runtime.CreateNodeWorkspaceCheckpoint(context.Background(), root, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	childStart, err := runtime.CreateNodeWorkspaceCheckpoint(context.Background(), root, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "child-file"), []byte("child"), 0o600); err != nil {
		t.Fatal(err)
	}
	childEnd, err := runtime.CreateNodeWorkspaceCheckpoint(context.Background(), root, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	childStart.End = childEnd.Repositories
	parent := domain.Run{ID: "parent", Nodes: map[string]*domain.NodeRun{"a": {NodeID: "a", Checkpoints: []domain.NodeCheckpoint{target}}}}
	child := domain.Run{ID: "child", ExecutionContext: domain.TelemetryContext{WorkspaceRunID: parent.ID}, Nodes: map[string]*domain.NodeRun{"b": {NodeID: "b", Checkpoints: []domain.NodeCheckpoint{childStart}}}}
	for _, run := range []domain.Run{parent, child} {
		if err := store.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	e := New(store, manager)
	if err := e.checkpointHasNoOtherNodeChanges(context.Background(), parent, "a", target, nil); err == nil {
		t.Fatal("restore accepted modifications made by child SOP")
	}
}

func TestNodeCheckpointDiffOnRemoteWorkspaceOwner(t *testing.T) {
	worker, controller, id := remoteWorkspaceRestoreFixture(t, "sqlite")
	root := worker.RunWorkspace(id)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	git("init", "-q")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "base")
	start, err := runtime.CreateNodeWorkspaceCheckpoint(context.Background(), root, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "change"), []byte("remote change"), 0o600); err != nil {
		t.Fatal(err)
	}
	end, err := runtime.CreateNodeWorkspaceCheckpoint(context.Background(), root, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	serveWorkspaceRestoreWorker(t, worker, http.HandlerFunc(worker.HandleWorkspaceCheckpoint))
	run, _ := worker.repo.RunState(id)
	response, err := controller.DiffNodeCheckpoints(context.Background(), run, start, end)
	if err != nil || response.Error != "" || !strings.Contains(string(response.Payload), "+remote change") {
		t.Fatalf("remote diff: %+v, %v", response, err)
	}
	if err := worker.repo.ResetRunWorkspaceCleanup(id); err != nil {
		t.Fatal(err)
	}
	run, _ = worker.repo.RunState(id)
	run.ExecutionContext.NodeCheckpointsEnabled = true
	start.End = end.Repositories
	run.Nodes["close"].Checkpoints = []domain.NodeCheckpoint{start}
	if err := worker.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := controller.RewindNodeWorkspace(context.Background(), id, "close", 1, "", 7); err != nil {
		t.Fatalf("remote rewind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "change")); !os.IsNotExist(err) {
		t.Fatalf("remote rewind did not restore files: %v", err)
	}
}

func TestNodeRewindPreservesRecoveryAndResetsDownstream(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := runtime.NewManager(t.TempDir())
	e := New(store, manager)
	workspace := manager.RunWorkspace("rewind")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", workspace}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, output, err)
		}
	}
	git("init", "-q")
	file := filepath.Join(workspace, "file")
	if err := os.WriteFile(file, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "base")
	cp, err := runtime.CreateNodeWorkspaceCheckpoint(context.Background(), workspace, []domain.RepositoryContext{{RepositoryID: "repo"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "rewind", Status: "failed", Repository: domain.RepositoryContext{RepositoryID: "repo"}, ExecutionContext: domain.TelemetryContext{NodeCheckpointsEnabled: true}, WorkflowSnapshot: domain.WorkflowDefinition{Edges: []domain.WorkflowEdge{{Source: "a", Target: "b"}}}, Nodes: map[string]*domain.NodeRun{
		"a": {NodeID: "a", Status: "failed", Attempt: 1, Checkpoints: []domain.NodeCheckpoint{cp}}, "b": {NodeID: "b", Status: "succeeded"},
	}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := e.RewindNodeWorkspace(context.Background(), run.ID, "a", 1, "", 42); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "before" {
		t.Fatalf("rewind file = %q, %v", got, err)
	}
	current, ok := store.RunState(run.ID)
	if !ok || current.Nodes["a"].Status != "pending" || current.Nodes["b"].Status != "pending" || len(current.Nodes["a"].RecoveryCheckpoints) != 1 {
		t.Fatalf("rewind state: %+v", current.Nodes)
	}
	if last := current.Events[len(current.Events)-1]; last.Type != "workspace.rewound" || last.ActorID != 42 {
		t.Fatalf("event: %+v", last)
	}
	recoveryID := current.Nodes["a"].RecoveryCheckpoints[0].ID
	if err := e.RewindNodeWorkspace(context.Background(), run.ID, "a", 0, recoveryID, 42); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "after" {
		t.Fatalf("undo file = %q, %v", got, err)
	}
}

func TestTargetedFixPromptContainsPreviousChangedFilesWithoutPlaceholder(t *testing.T) {
	node := domain.WorkflowNode{ID: "implementation", RetryStrategy: "targeted_fix"}
	run := domain.Run{Nodes: map[string]*domain.NodeRun{node.ID: {
		Feedback:    "请修复",
		Checkpoints: []domain.NodeCheckpoint{{Attempt: 1, ChangedFiles: []string{"src/change.go"}}, {Attempt: 2}},
	}}}
	prompt := renderPromptSpec(domain.WorkflowDefinition{}, node, "请实现需求", run)
	for _, expected := range []string{"请修复", "src/change.go"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt lacks %q: %s", expected, prompt)
		}
	}
}
