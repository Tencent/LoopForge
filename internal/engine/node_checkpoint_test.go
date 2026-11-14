package engine

import (
	"context"
	"errors"
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

func TestCheckpointAttemptCanReuseOwnWorkspaceLease(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := New(store, runtime.NewManager(t.TempDir()))
	run := domain.Run{ID: "checkpoint-lease", Status: "running", Nodes: map[string]*domain.NodeRun{}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	token, err := store.BeginWorkspaceUse(run.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.restoreCheckpointBeforeExecution(context.Background(), run); !errors.Is(err, repo.ErrWorkspaceInUse) {
		t.Fatalf("foreign workspace lease: %v", err)
	}
	ctx := context.WithValue(context.Background(), nodeCheckpointLeaseKey{}, token)
	if err := e.restoreCheckpointBeforeExecution(ctx, run); err != nil {
		t.Fatalf("own workspace lease: %v", err)
	}
}

func TestStartNodeCheckpointsProjectAndChildPolicy(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := New(store, runtime.NewManager(t.TempDir()))
	e.ConfigureExecution("control", "control", time.Minute, false)
	wf := domain.WorkflowDefinition{ID: "checkpoint-policy", Name: "Checkpoint policy", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "work", Name: "Work", Type: "script", ScriptLanguage: "shell", Script: "true"}}}
	if err := store.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	if err := store.PutProject(domain.Project{ID: "checkpoints-off", NodeCheckpointsDisabled: true}); err != nil {
		t.Fatal(err)
	}
	repositories := []domain.RepositoryContext{{RepositoryID: "repo"}}
	for _, tc := range []struct {
		name, project string
		repositories  []domain.RepositoryContext
		want          bool
	}{{"default", "", repositories, true}, {"opt-out", "checkpoints-off", repositories, false}, {"no repository", "", nil, false}} {
		t.Run(tc.name, func(t *testing.T) {
			run, err := e.StartWithRepositoriesContext(wf.ID, "", tc.repositories, domain.TelemetryContext{ProjectID: tc.project})
			if err != nil {
				t.Fatal(err)
			}
			if run.ExecutionContext.NodeCheckpointsEnabled != tc.want {
				t.Fatalf("checkpoint policy = %v", run.ExecutionContext.NodeCheckpointsEnabled)
			}
		})
	}
	parent := domain.Run{ID: "checkpoint-policy-parent", Status: "running", ExecutionContext: domain.TelemetryContext{NodeCheckpointsEnabled: true}, Nodes: map[string]*domain.NodeRun{}}
	if err := store.PutRun(parent); err != nil {
		t.Fatal(err)
	}
	child, err := e.StartWithRepositoriesContext(wf.ID, "", repositories, domain.TelemetryContext{ParentRunID: parent.ID, WorkspaceRunID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !child.ExecutionContext.NodeCheckpointsEnabled {
		t.Fatal("child did not inherit parent checkpoint policy")
	}
}

func TestCheckpointAttemptRunsFromFreshCheckout(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "master"}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "base"}} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, output, err)
		}
	}
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node := domain.WorkflowNode{ID: "write", Type: "script", ScriptLanguage: "shell", ExecutionScope: domain.ExecutionScopeRepositories, Script: "printf written > result.txt", MaxAttempts: 1}
	wf := domain.WorkflowDefinition{ID: "fresh-checkpoint", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{node}}
	repository := domain.RepositoryContext{RepositoryID: "repo", Name: "repo", CloneURL: source, BaseBranch: "master", WorkBranch: "checkpoint-test", WorkspaceDir: "repo"}
	run := domain.Run{ID: "fresh-checkpoint", Status: "running", Repository: repository, Repositories: []domain.RepositoryContext{repository}, WorkflowSnapshot: wf, ExecutionContext: domain.TelemetryContext{NodeCheckpointsEnabled: true}, Nodes: map[string]*domain.NodeRun{node.ID: {NodeID: node.ID, Status: "pending"}}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e := New(store, runtime.NewManager(filepath.Join(root, "workspaces")))
	if _, err := e.acquireLease(run); err != nil {
		t.Fatal(err)
	}
	e.executeNode(context.Background(), run.ID, wf, node)
	current, _ := store.RunState(run.ID)
	result := current.Nodes[node.ID]
	if result.Status != "succeeded" || len(result.Checkpoints) != 1 || result.Checkpoints[0].End == nil {
		t.Fatalf("node state = %+v", result)
	}
	if content, err := os.ReadFile(filepath.Join(e.RunWorkspace(run.ID), "repo", "result.txt")); err != nil || string(content) != "written" {
		t.Fatalf("script result = %q, %v", content, err)
	}
}

func TestStartedRunCheckpointProjectPolicy(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "master"}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "base"}} {
		if out, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s: %v", out, err)
		}
	}
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	wf := domain.WorkflowDefinition{ID: "checkpoint-e2e", Name: "Checkpoint e2e", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "write", Name: "Write", Type: "script", ScriptLanguage: "shell", ExecutionScope: domain.ExecutionScopeRepositories, Script: "printf changed > result.txt", MaxAttempts: 1}}}
	if err := store.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	if err := store.PutProject(domain.Project{ID: "checkpoints-off", NodeCheckpointsDisabled: true}); err != nil {
		t.Fatal(err)
	}
	e := New(store, runtime.NewManager(filepath.Join(root, "workspaces")))
	defer e.Drain(context.Background())
	repository := domain.RepositoryContext{RepositoryID: "repo", Name: "repo", CloneURL: source, BaseBranch: "master", WorkBranch: "test", WorkspaceDir: "repo"}
	for _, tc := range []struct {
		name, project string
		checkpoints   int
	}{{"default", "", 1}, {"opt-out", "checkpoints-off", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			run, err := e.StartWithRepositoriesContext(wf.ID, "", []domain.RepositoryContext{repository}, domain.TelemetryContext{ProjectID: tc.project})
			if err != nil {
				t.Fatal(err)
			}
			for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				current, ok := store.RunState(run.ID)
				if !ok || current.Status == "running" || current.Status == "queued" {
					continue
				}
				node := current.Nodes["write"]
				if current.Status != "succeeded" || node == nil || len(node.Checkpoints) != tc.checkpoints || (tc.checkpoints > 0 && node.Checkpoints[0].End == nil) {
					t.Fatalf("run did not follow checkpoint policy: status=%s node=%+v", current.Status, node)
				}
				return
			}
			t.Fatal("run did not finish")
		})
	}
}

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
	response, err := controller.DiffNodeCheckpoints(context.Background(), run, start, &end)
	if err != nil || response.Error != "" || !strings.Contains(string(response.Payload), "+remote change") {
		t.Fatalf("remote diff: %+v, %v", response, err)
	}
	response, err = controller.DiffNodeCheckpoints(context.Background(), run, start, nil)
	if err != nil || response.Error != "" || !strings.Contains(string(response.Payload), `"live":true`) || !strings.Contains(string(response.Payload), "+remote change") {
		t.Fatalf("remote live diff: %+v, %v", response, err)
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

func TestAutomaticNodeRestoreOnRemoteWorkspaceOwner(t *testing.T) {
	worker, controller, id := remoteWorkspaceRestoreFixture(t, "sqlite")
	root := worker.RunWorkspace(id)
	if err := os.MkdirAll(root, 0700); err != nil {
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
	file := filepath.Join(root, "change")
	if err := os.WriteFile(file, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	recovery, err := runtime.CreateNodeWorkspaceCheckpoint(context.Background(), root, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	serveWorkspaceRestoreWorker(t, worker, http.HandlerFunc(worker.HandleWorkspaceCheckpoint))
	run, _ := worker.repo.RunState(id)
	token, err := worker.repo.BeginWorkspaceUse(id, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer worker.repo.FinishWorkspaceUse(id, token, time.Now().UTC(), nil, "")
	if err := worker.repo.MarkNodeCheckpointRestorePending(id, id, token, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	response, err := controller.requestWorkspaceCheckpoint(context.Background(), run, domain.RunnerWorkspaceRequest{RunID: id, Operation: domain.RunnerWorkspaceOperationNodeCheckpointRestore, NodeCheckpoint: &start, NodeCheckpointEnd: &recovery, CheckpointLease: token})
	if err != nil || response.Error != "" || response.Status >= 400 {
		t.Fatalf("remote automatic restore: %+v, %v", response, err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("remote restore left new file: %v", err)
	}
}

func TestFailedChildRestoreStopsParentScheduler(t *testing.T) {
	e, store := newRecoveryEngine(t)
	e.ConfigureExecution("parent-worker", "parent-host", time.Minute, true)
	parent := domain.Run{ID: "parent-restore-failure", Status: "running", Generation: 1, OwnerInstanceID: "parent-worker", LeaseUntil: time.Now().Add(time.Minute), ExecutionContext: domain.TelemetryContext{NodeCheckpointsEnabled: true}, WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "sop", Type: "sop"}}}, Nodes: map[string]*domain.NodeRun{"sop": {NodeID: "sop", Status: "running"}}}
	child := domain.Run{ID: "child-restore-failure", Status: "running", Generation: 1, ExecutionContext: domain.TelemetryContext{NodeCheckpointsEnabled: true, WorkspaceRunID: parent.ID, ParentRunID: parent.ID}, Nodes: map[string]*domain.NodeRun{"node": {NodeID: "node", Status: "running", Attempt: 2}}}
	for _, run := range []domain.Run{parent, child} {
		if err := store.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	token, err := store.BeginWorkspaceUse(parent.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkNodeCheckpointRestorePending(parent.ID, child.ID, token, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.FailInterruptedNodeCheckpointRestore(child.ID, parent.ID, token, "node", 2, 1, "restore interrupted"); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishWorkspaceUse(parent.ID, token, time.Now().UTC(), nil, ""); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { e.execute(context.Background(), parent.ID); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("parent scheduler did not stop")
	}
	parent, _ = store.RunState(parent.ID)
	if parent.Status != "failed" || parent.Nodes["sop"].Status != "failed" || parent.OwnerInstanceID != "" {
		t.Fatalf("parent did not settle: %+v", parent)
	}
	if _, err := store.BeginSharedNodeRewind(child.ID, parent.ID, time.Now().UTC()); err != nil {
		t.Fatalf("child recovery remains blocked: %v", err)
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
	// A child task can still be exiting even if its own run is no longer
	// active. It shares this checkout and must fence the parent's rewind.
	if err := store.PutRunnerTask(domain.RunnerTask{ID: "rewind-child-task", RunID: "child", Provider: "codex", NodeCheckpointRunID: run.ID}); err != nil {
		t.Fatal(err)
	}
	if err := e.RewindNodeWorkspace(context.Background(), run.ID, "a", 1, "", 42); err == nil {
		t.Fatal("rewind accepted a live task in the shared workspace")
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "after" {
		t.Fatalf("busy rewind changed file = %q, %v", got, err)
	}
	if err := store.CancelRunnerTask("rewind-child-task", time.Now().UTC()); err != nil {
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

func TestChildNodeRewindRestoresSharedWorkspace(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := runtime.NewManager(t.TempDir())
	e := New(store, manager)
	root := manager.RunWorkspace("parent-rewind")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
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
	checkpoint, err := runtime.CreateNodeWorkspaceCheckpoint(context.Background(), root, []domain.RepositoryContext{{RepositoryID: "repo"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := domain.Run{ID: "parent-rewind", Status: "failed", ExecutionContext: domain.TelemetryContext{NodeCheckpointsEnabled: true}, Nodes: map[string]*domain.NodeRun{"sop": {NodeID: "sop", Status: "failed", ChildRunID: "child-rewind"}}}
	child := domain.Run{ID: "child-rewind", Status: "failed", Repository: domain.RepositoryContext{RepositoryID: "repo"}, ExecutionContext: domain.TelemetryContext{NodeCheckpointsEnabled: true, ParentRunID: parent.ID, WorkspaceRunID: parent.ID}, Nodes: map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "failed", Attempt: 1, Checkpoints: []domain.NodeCheckpoint{checkpoint}}}}
	for _, run := range []domain.Run{parent, child} {
		if err := store.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.RewindNodeWorkspace(context.Background(), child.ID, "work", 1, "", 42); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(file); err != nil || string(content) != "before" {
		t.Fatalf("child rewind = %q, %v", content, err)
	}
	current, _ := store.RunState(child.ID)
	if current.Nodes["work"].Status != "pending" || len(current.Nodes["work"].RecoveryCheckpoints) != 1 || len(current.Events) == 0 || current.Events[len(current.Events)-1].ActorID != 42 {
		t.Fatalf("child rewind state = %+v", current)
	}
	recovery := current.Nodes["work"].RecoveryCheckpoints[0].ID
	if err := e.RewindNodeWorkspace(context.Background(), child.ID, "work", 0, recovery, 42); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(file); err != nil || string(content) != "after" {
		t.Fatalf("child undo = %q, %v", content, err)
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
