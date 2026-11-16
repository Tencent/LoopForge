package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func runnerPreparationFixture(t *testing.T) (*Daemon, domain.RunnerWorkspaceRequest, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "runs", "prepared", "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	return &Daemon{cfg: Config{DefaultWorkspaceRoot: base}}, domain.RunnerWorkspaceRequest{
		RunID: "prepared", RunStatus: "waiting", WorkspaceLayout: domain.RunnerWorkspaceLayoutShared,
		Operation: domain.RunnerWorkspaceOperationPrepare,
		Sandbox:   &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{Driver: "local", Environment: map[string]string{"WORKSPACE_OVERLAY": "same-node-environment"}}, Requirements: &domain.RunnerSandboxRequirements{}},
		Workflow:  &domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "prepare", Name: "Prepare", Type: "script", RunOnWorkspaceRebuild: true, Script: "printf x >> preparation-count; printf '%s' \"$WORKSPACE_OVERLAY\" > prepared-environment"}}},
	}, root
}

func TestRunnerWorkspacePreparationAndTerminalShareFrozenEnvironment(t *testing.T) {
	d, req, root := runnerPreparationFixture(t)
	defer d.closeAllRunSandboxes(context.Background())
	for i := 0; i < 2; i++ {
		result := d.readRunnerWorkspace(context.Background(), req)
		if result.Status != 200 {
			t.Fatalf("prepare: %+v", result)
		}
		var state runtime.WorkspacePreparationStatus
		if err := json.Unmarshal(result.Payload, &state); err != nil || state.Status != "ready" {
			t.Fatalf("state: %s %v", result.Payload, err)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(root, "preparation-count")); err != nil || string(raw) != "x" {
		t.Fatalf("preparation reran: %q %v", raw, err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "prepared-environment")); err != nil || string(raw) != "same-node-environment" {
		t.Fatalf("overlay: %q %v", raw, err)
	}
	req.Operation = domain.RunnerWorkspaceOperationTerminal
	req.TerminalOwner = 7
	req.Terminal = &domain.WorkspaceTerminalRequest{Operation: "open", ExpiresAt: time.Now().Add(time.Minute)}
	opened := d.readRunnerWorkspace(context.Background(), req)
	if opened.Status != 200 {
		t.Fatalf("open: %+v", opened)
	}
	defer d.workspaceTerminals.CloseScope("prepared:7")
	var terminal runtime.TerminalResult
	if err := json.Unmarshal(opened.Payload, &terminal); err != nil {
		t.Fatal(err)
	}
	if len(d.runSandboxes) != 1 {
		t.Fatalf("terminal did not reuse run sandbox: %d", len(d.runSandboxes))
	}
	for _, entry := range d.runSandboxes {
		if entry.active != 1 {
			t.Fatalf("terminal sandbox reference: %d", entry.active)
		}
	}
	req.Terminal = &domain.WorkspaceTerminalRequest{Operation: "input", ID: terminal.ID, Data: "printf '%s' \"$WORKSPACE_OVERLAY\" > terminal-environment\n"}
	if result := d.readRunnerWorkspace(context.Background(), req); result.Status != 200 {
		t.Fatalf("input: %+v", result)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(filepath.Join(root, "terminal-environment"))
		if err == nil && string(raw) == "same-node-environment" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "terminal-environment")); err != nil || string(raw) != "same-node-environment" {
		t.Fatalf("terminal overlay: %q %v", raw, err)
	}
	req.Terminal = &domain.WorkspaceTerminalRequest{Operation: "close", ID: terminal.ID}
	if result := d.readRunnerWorkspace(context.Background(), req); result.Status != 200 {
		t.Fatalf("close: %+v", result)
	}
	for _, entry := range d.runSandboxes {
		if entry.active != 0 {
			t.Fatalf("terminal leaked sandbox reference: %d", entry.active)
		}
	}
}

func TestRunnerWorkspacePreparationFailsClosedAndFencesActiveTasks(t *testing.T) {
	d, req, root := runnerPreparationFixture(t)
	req.Sandbox = nil
	result := d.readRunnerWorkspace(context.Background(), req)
	if result.Status == 200 || !strings.Contains(result.Error, "frozen run sandbox") {
		t.Fatalf("missing sandbox accepted: %+v", result)
	}
	req.Operation = domain.RunnerWorkspaceOperationTerminal
	req.TerminalOwner = 7
	req.Terminal = &domain.WorkspaceTerminalRequest{Operation: "open"}
	if result = d.readRunnerWorkspace(context.Background(), req); result.Status == 200 {
		t.Fatal("terminal silently opened host shell")
	}
	req.Sandbox = &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{Driver: "local"}}
	req.Operation = domain.RunnerWorkspaceOperationPrepare
	gate := d.workspaceGate(req.RunID)
	gate.RLock()
	result = d.readRunnerWorkspace(context.Background(), req)
	gate.RUnlock()
	if result.Status != 409 || result.Code != domain.WorkspaceErrorBusy {
		t.Fatalf("active task not fenced as a retryable conflict: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "preparation-count")); !os.IsNotExist(err) {
		t.Fatalf("script ran during task: %v", err)
	}
}

func TestFrozenRunRequirementsKeepAllProvidersInSameSandbox(t *testing.T) {
	d, req, _ := runnerPreparationFixture(t)
	defer d.closeAllRunSandboxes(context.Background())
	var previous runtime.Sandbox
	for _, provider := range []string{"terminal", "codebuddy", "codex"} {
		sb, release, err := d.acquireTaskSandbox(context.Background(), domain.RunnerTask{ID: provider, RunID: req.RunID, Workspace: "runs/prepared/workspace", Provider: provider, Sandbox: req.Sandbox})
		if err != nil {
			t.Fatal(err)
		}
		if previous != nil && previous != sb {
			t.Fatalf("provider %s switched run sandbox", provider)
		}
		previous = sb
		release()
		release()
	}
	for _, entry := range d.runSandboxes {
		if entry.active != 0 {
			t.Fatalf("double release underflow: %d", entry.active)
		}
	}
}

func TestRunnerPreparationCreatesCheckoutAndPreservesDirtyFiles(t *testing.T) {
	d, request, root := runnerPreparationFixture(t)
	remote := bareRemoteWithBaseline(t)
	request.PreparationInvocation = &domain.RunnerInvocation{Repositories: []domain.RunnerRepository{{CloneURL: remote, Dir: "service", Ref: "master"}}, GitUserName: "Workspace User", GitUserEmail: "workspace@example.test"}
	request.Repositories = []domain.RepositoryContext{{Name: "service", WorkspaceDir: "service"}}
	request.Workflow.Nodes[0].Script = "test -f service/README.md; printf x >> preparation-count"
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	first := d.readRunnerWorkspace(context.Background(), request)
	if first.Status != 200 {
		t.Fatalf("first preparation: %+v", first)
	}
	file := filepath.Join(root, "service", "README.md")
	if err := os.WriteFile(file, []byte("human dirty change"), 0600); err != nil {
		t.Fatal(err)
	}
	if result := d.readRunnerWorkspace(context.Background(), request); result.Status != 200 {
		t.Fatalf("repeat preparation: %+v", result)
	}
	if raw, err := os.ReadFile(file); err != nil || string(raw) != "human dirty change" {
		t.Fatalf("preparation overwrote dirty checkout: %q %v", raw, err)
	}
	d.closeAllRunSandboxes(context.Background())
}

func TestRunnerConversationWorkspaceWaveReusesReadinessWithoutPreparingDuringActiveExecution(t *testing.T) {
	d, request, root := runnerPreparationFixture(t)
	defer d.closeAllRunSandboxes(context.Background())
	if result := d.readRunnerWorkspace(context.Background(), request); result.Status != 200 {
		t.Fatal(result)
	}
	task := domain.RunnerTask{ID: "active-role", RunID: request.RunID, Workspace: "runs/prepared/workspace", Provider: "codebuddy", Sandbox: request.Sandbox}
	_, release, err := d.acquireTaskSandbox(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	gate := d.workspaceGate(request.RunID)
	gate.RLock()
	if err := os.WriteFile(filepath.Join(root, "dirty-during-execution.txt"), []byte("member writing"), 0600); err != nil {
		t.Fatal(err)
	}
	request.Operation = domain.RunnerWorkspaceOperationPreparationStatus
	result := d.readRunnerWorkspace(context.Background(), request)
	var state runtime.WorkspacePreparationStatus
	if err := json.Unmarshal(result.Payload, &state); err != nil || state.Status != "ready" {
		t.Fatalf("active wave readiness: %s %v", result.Payload, err)
	}
	request.Operation = domain.RunnerWorkspaceOperationPrepare
	// A parallel SOP branch starting next to a running sibling reuses the live
	// stamp exactly like a group role; neither reruns preflight scripts.
	if result := d.readRunnerWorkspace(context.Background(), request); result.Status != 200 {
		t.Fatal("parallel SOP branch failed to reuse active wave", result)
	}
	request.RunKind = domain.RunKindWorkspace
	if result := d.readRunnerWorkspace(context.Background(), request); result.Status != 200 {
		t.Fatal("healthy group preparation failed to reuse active wave", result)
	}
	gate.RUnlock()
	if raw, err := os.ReadFile(filepath.Join(root, "preparation-count")); err != nil || string(raw) != "x" {
		t.Fatalf("active wave reran scripts: %q %v", raw, err)
	}
	request.Operation = domain.RunnerWorkspaceOperationPreparationStatus
	request.Workflow.Nodes[0].Script += "\nprintf new"
	result = d.readRunnerWorkspace(context.Background(), request)
	if err := json.Unmarshal(result.Payload, &state); err != nil || state.Status != "unprepared" {
		t.Fatalf("changed configuration was reused: %s %v", result.Payload, err)
	}
	request.Workflow.Nodes[0].Script = strings.TrimSuffix(request.Workflow.Nodes[0].Script, "\nprintf new")
	release()
	d.closeRunSandboxes(context.Background(), request.RunID)
	result = d.readRunnerWorkspace(context.Background(), request)
	if err := json.Unmarshal(result.Payload, &state); err != nil || state.Status != "unprepared" {
		t.Fatalf("lost environment reused: %s %v", result.Payload, err)
	}
	if len(d.runSandboxes) != 0 {
		t.Fatal("readiness read created replacement environment")
	}
}

func TestRunnerConversationWorkspaceTerminalJoinsPreparedActiveWave(t *testing.T) {
	d, request, root := runnerPreparationFixture(t)
	defer d.closeAllRunSandboxes(context.Background())
	if result := d.readRunnerWorkspace(context.Background(), request); result.Status != 200 {
		t.Fatal(result)
	}
	gate := d.workspaceGate(request.RunID)
	gate.RLock()
	defer gate.RUnlock()
	request.Operation = domain.RunnerWorkspaceOperationTerminal
	request.RunKind = domain.RunKindWorkspace
	request.RunStatus = "running"
	request.TerminalOwner = 7
	request.Terminal = &domain.WorkspaceTerminalRequest{Operation: "open", ExpiresAt: time.Now().Add(time.Minute)}
	opened := d.readRunnerWorkspace(context.Background(), request)
	if opened.Status != 200 {
		t.Fatalf("healthy group terminal blocked by active role: %+v", opened)
	}
	defer d.workspaceTerminals.CloseScope("prepared:7")
	if raw, err := os.ReadFile(filepath.Join(root, "preparation-count")); err != nil || string(raw) != "x" {
		t.Fatalf("group terminal reran preparation: %q %v", raw, err)
	}
	request.Workflow.Nodes[0].Script += "\nprintf changed"
	request.TerminalOwner = 8
	if result := d.readRunnerWorkspace(context.Background(), request); result.Status == 200 {
		d.workspaceTerminals.CloseScope("prepared:8")
		t.Fatal("changed recipe allowed into busy environment")
	}
}

func TestRunnerRebuildScriptsIgnoreOrdinaryEditsAndReplayAfterRestoration(t *testing.T) {
	d, request, root := runnerPreparationFixture(t)
	defer d.closeAllRunSandboxes(context.Background())
	request.Workflow.Nodes = append(request.Workflow.Nodes, domain.WorkflowNode{ID: "future", Name: "Not yet completed", Type: "script", Script: "printf x >> future-count"})
	if result := d.readRunnerWorkspace(context.Background(), request); result.Status != 200 {
		t.Fatal(result)
	}
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("a normal Main Bot edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if result := d.readRunnerWorkspace(context.Background(), request); result.Status != 200 {
		t.Fatal(result)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "preparation-count")); err != nil || string(raw) != "x" {
		t.Fatalf("ordinary edit replayed rebuild script: %q %v", raw, err)
	}
	archive, err := runtime.CreateWorkspaceCheckpoint(root)
	if err != nil {
		t.Fatal(err)
	}
	d.closeRunSandboxes(context.Background(), request.RunID)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := runtime.RestoreWorkspaceCheckpoint(root, archive); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if result := d.readRunnerWorkspace(context.Background(), request); result.Status != 200 {
			t.Fatal(result)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(root, "preparation-count")); err != nil || string(raw) != "xx" {
		t.Fatalf("restored environment did not replay exactly once: %q %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(root, "future-count")); !os.IsNotExist(err) {
		t.Fatalf("unselected future script was executed: %v", err)
	}
}

func TestRunnerRecordsNormalSOPScriptWithoutReexecutingIt(t *testing.T) {
	d, request, root := runnerPreparationFixture(t)
	defer d.closeAllRunSandboxes(context.Background())
	task := domain.RunnerTask{ID: "normal-sop-script", RunID: request.RunID, Workspace: "runs/prepared/workspace", Provider: "terminal", Sandbox: request.Sandbox}
	sb, release, err := d.acquireTaskSandbox(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sb.Exec(context.Background(), runtime.CommandSpec{Argv: []string{"sh", "-lc", request.Workflow.Nodes[0].Script}, Provider: "script"}, nil); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	request.Operation = domain.RunnerWorkspaceOperationPreparationRecord
	request.PreparationNodeID = "prepare"
	request.PreparationOutput = "normal script finished"
	if result := d.readRunnerWorkspace(context.Background(), request); result.Status != 200 {
		t.Fatalf("record: %+v", result)
	}
	request.Operation = domain.RunnerWorkspaceOperationPrepare
	if result := d.readRunnerWorkspace(context.Background(), request); result.Status != 200 {
		t.Fatalf("prepare after normal script: %+v", result)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "preparation-count")); err != nil || string(raw) != "x" {
		t.Fatalf("normal script was executed twice: %q %v", raw, err)
	}
	d.closeRunSandboxes(context.Background(), request.RunID)
	request.Operation = domain.RunnerWorkspaceOperationPreparationRecord
	if result := d.readRunnerWorkspace(context.Background(), request); result.Status == 200 {
		t.Fatal("record created an unexecuted replacement environment")
	}
	if len(d.runSandboxes) != 0 {
		t.Fatal("record created a replacement sandbox")
	}
}

func TestRunnerPreparationRecordsMergeWhileFinishingWorkersHoldReadFences(t *testing.T) {
	d, request, root := runnerPreparationFixture(t)
	defer d.closeAllRunSandboxes(context.Background())
	request.Workflow.Nodes = append(request.Workflow.Nodes, domain.WorkflowNode{ID: "second", Name: "Second", Type: "script", RunOnWorkspaceRebuild: true, Script: "printf second"})
	task := domain.RunnerTask{ID: "finishing", RunID: request.RunID, Workspace: "runs/prepared/workspace", Provider: "terminal", Sandbox: request.Sandbox}
	sb, release, err := d.acquireTaskSandbox(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	gate := d.workspaceGate(request.RunID)
	gate.RLock()
	defer gate.RUnlock()
	start := make(chan struct{})
	results := make(chan domain.RunnerWorkspaceResponse, 2)
	for _, nodeID := range []string{"prepare", "second"} {
		go func(nodeID string) {
			<-start
			record := request
			record.Operation = domain.RunnerWorkspaceOperationPreparationRecord
			record.PreparationNodeID = nodeID
			record.PreparationOutput = nodeID + " finished"
			results <- d.readRunnerWorkspace(context.Background(), record)
		}(nodeID)
	}
	close(start)
	for i := 0; i < 2; i++ {
		if result := <-results; result.Status != 200 {
			t.Fatalf("finishing worker blocked record: %+v", result)
		}
	}
	state := runtime.ReadWorkspacePreparationStatus(root, sb.ID())
	found := map[string]bool{}
	for _, step := range state.Steps {
		found[step.NodeID] = true
	}
	if !found["prepare"] || !found["second"] {
		t.Fatalf("concurrent record lost a successful script: %+v", state)
	}
	if _, err := os.Stat(filepath.Join(root, "preparation-count")); !os.IsNotExist(err) {
		t.Fatalf("record executed a script: %v", err)
	}
}

func TestRunnerLegacyWorkspaceRejectsPreparationRecordAndTerminalWithoutSideEffects(t *testing.T) {
	d, request, sharedRoot := runnerPreparationFixture(t)
	request.WorkspaceLayout = ""
	request.TerminalOwner = 7
	request.Terminal = &domain.WorkspaceTerminalRequest{Operation: "open", ExpiresAt: time.Now().Add(time.Minute)}
	request.PreparationNodeID = "prepare"
	for _, operation := range []string{domain.RunnerWorkspaceOperationPrepare, domain.RunnerWorkspaceOperationPreparationRecord, domain.RunnerWorkspaceOperationTerminal} {
		request.Operation = operation
		result := d.readRunnerWorkspace(context.Background(), request)
		if result.Status == 200 || !strings.Contains(result.Error, "legacy per-node workspace") {
			t.Fatalf("legacy %s was not explicitly rejected: %+v", operation, result)
		}
	}
	for _, root := range []string{sharedRoot, filepath.Dir(sharedRoot)} {
		if _, err := os.Stat(filepath.Join(root, "preparation-count")); !os.IsNotExist(err) {
			t.Fatalf("legacy operation ran script in %s: %v", root, err)
		}
	}
	if len(d.runSandboxes) != 0 || d.workspaceTerminals.HasRunScope(request.RunID) {
		t.Fatal("legacy request created an execution environment")
	}
	request.Operation = domain.RunnerWorkspaceOperationTree
	if result := d.readRunnerWorkspace(context.Background(), request); result.Status != 200 {
		t.Fatalf("legacy file browsing was blocked: %+v", result)
	}
}

func TestChildWorkspacePreparationUsesParentCheckoutAndIsolatedReceipts(t *testing.T) {
	d, parent, root := runnerPreparationFixture(t)
	defer d.closeAllRunSandboxes(context.Background())
	if err := os.WriteFile(filepath.Join(root, "parent-source.txt"), []byte("parent checkout"), 0600); err != nil {
		t.Fatal(err)
	}
	parent.Workflow.Nodes[0].Script = "test \"$(cat parent-source.txt)\" = 'parent checkout'; printf x >> preparation-count"
	if result := d.readRunnerWorkspace(context.Background(), parent); result.Status != 200 {
		t.Fatalf("parent prepare: %+v", result)
	}
	child := parent
	child.RunID = "child-run"
	child.WorkspaceRunID = parent.RunID
	child.Operation = domain.RunnerWorkspaceOperationPreparationStatus
	checkStatus := func(request domain.RunnerWorkspaceRequest, want string) {
		t.Helper()
		response := d.readRunnerWorkspace(context.Background(), request)
		var state runtime.WorkspacePreparationStatus
		if err := json.Unmarshal(response.Payload, &state); err != nil || response.Status != 200 || state.Status != want {
			t.Fatalf("status %s want %s: %+v %v", request.RunID, want, response, err)
		}
	}
	checkStatus(child, "unprepared")
	task := domain.RunnerTask{ID: "child-script", RunID: child.RunID, Workspace: "runs/" + parent.RunID + "/workspace", Provider: "terminal", Sandbox: child.Sandbox}
	sb, release, err := d.acquireTaskSandbox(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if sb.Workspace() != root {
		release()
		t.Fatalf("child sandbox = %s, want parent %s", sb.Workspace(), root)
	}
	checkStatus(child, "unprepared")
	if _, err := sb.Exec(context.Background(), runtime.CommandSpec{Argv: []string{"sh", "-lc", child.Workflow.Nodes[0].Script}, Provider: "script"}, nil); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	child.Operation = domain.RunnerWorkspaceOperationPreparationRecord
	child.PreparationNodeID = "prepare"
	child.PreparationOutput = "child completed in parent checkout"
	if result := d.readRunnerWorkspace(context.Background(), child); result.Status != 200 {
		t.Fatalf("child record: %+v", result)
	}
	child.Operation = domain.RunnerWorkspaceOperationPreparationStatus
	checkStatus(child, "ready")
	parent.Operation = domain.RunnerWorkspaceOperationPreparationStatus
	checkStatus(parent, "ready")
	child.Operation = domain.RunnerWorkspaceOperationPrepare
	if result := d.readRunnerWorkspace(context.Background(), child); result.Status != 200 {
		t.Fatalf("child warm prepare: %+v", result)
	}
	again, releaseAgain, err := d.acquireTaskSandbox(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if again != sb || again.Workspace() != root {
		releaseAgain()
		t.Fatal("child follow-up task reused a different checkout or sandbox")
	}
	releaseAgain()
	if raw, err := os.ReadFile(filepath.Join(root, "preparation-count")); err != nil || string(raw) != "xx" {
		t.Fatalf("child script was skipped or replayed: %q %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(d.cfg.DefaultWorkspaceRoot, "runs", child.RunID)); !os.IsNotExist(err) {
		t.Fatalf("child preparation created its own checkout: %v", err)
	}
	d.closeRunSandboxes(context.Background(), child.RunID)
	child.Operation = domain.RunnerWorkspaceOperationPreparationStatus
	checkStatus(child, "unprepared")
	checkStatus(parent, "ready")
}

func TestRunnerWorkspaceOwnerRejectsUnsafePathsBeforePreparing(t *testing.T) {
	d, request, root := runnerPreparationFixture(t)
	for _, owner := range []string{"../parent", ".", "..", "/absolute", "one/two", " ", "unsafe\x00"} {
		request.WorkspaceRunID = owner
		response := d.readRunnerWorkspace(context.Background(), request)
		if response.Status == 200 || !strings.Contains(response.Error, "invalid workspace owner run id") {
			t.Fatalf("owner %q accepted: %+v", owner, response)
		}
	}
	if len(d.runSandboxes) != 0 {
		t.Fatal("unsafe owner created an environment")
	}
	if _, err := os.Stat(filepath.Join(root, "preparation-count")); !os.IsNotExist(err) {
		t.Fatalf("unsafe owner executed a script: %v", err)
	}
}

// A chat workspace on a cloud runner has no checkout until the platform asks
// for one. Drive the whole path: engine -> control websocket -> real daemon
// -> git clone, then make sure asking again never resets the user's edits.
func TestConversationWorkspaceMaterializesOnConnectedRunner(t *testing.T) {
	server, s, e := newDaemonTestServer(t)
	remote := bareRemoteWithBaseline(t)
	workspaceRoot := filepath.Join(t.TempDir(), "runner-workspace")
	daemon, err := New(Config{
		ServerURL:            server.URL,
		BootstrapToken:       "boot-secret",
		StatePath:            filepath.Join(t.TempDir(), "state.json"),
		Name:                 "chat-runner",
		MaxConcurrency:       1,
		HeartbeatInterval:    time.Second,
		LongPollTimeout:      2 * time.Second,
		DefaultWorkspaceRoot: workspaceRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = daemon.Run(ctx) }()
	waitFor(t, 5*time.Second, "runner registration", func() bool { return len(s.Runners()) == 1 })
	runnerID := s.Runners()[0].ID
	run := domain.Run{
		ID: "chat-materialize", Kind: domain.RunKindWorkspace, Status: "waiting", Nodes: map[string]*domain.NodeRun{},
		WorkflowSnapshot: domain.WorkflowDefinition{ID: domain.ConversationWorkspaceWorkflowID, SandboxProfileID: "local"},
		ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: runnerID, RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared},
		Repositories:     []domain.RepositoryContext{{RepositoryID: "web", Name: "web", CloneURL: remote, BaseBranch: "master", WorkBranch: "feature/chat", WorkspaceDir: "web"}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(workspaceRoot, "runs", run.ID, "workspace", "web")
	if _, err := os.Stat(checkout); !os.IsNotExist(err) {
		t.Fatalf("checkout exists before the first open: %v", err)
	}
	var lastErr error
	waitFor(t, 15*time.Second, "checkout through the runner control channel", func() bool {
		lastErr = e.MaterializeConversationWorkspace(ctx, run.ID)
		return lastErr == nil
	})
	if raw, err := os.ReadFile(filepath.Join(checkout, "README.md")); err != nil || string(raw) != "baseline\n" {
		t.Fatalf("checkout = %q, %v (last error %v)", raw, err, lastErr)
	}
	if branch := runGitT(t, checkout, "rev-parse", "--abbrev-ref", "HEAD"); branch != "feature/chat" {
		t.Fatalf("checked out %q, want the work branch", branch)
	}
	head := runGitT(t, checkout, "rev-parse", "HEAD")

	// Opening again (a later turn, or a retry) must keep local edits even if
	// the base branch moved: this request never refreshes a checkout.
	if err := os.WriteFile(filepath.Join(checkout, "README.md"), []byte("edited in chat\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	advanceCheckoutRemote(t, remote)
	if err := e.MaterializeConversationWorkspace(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(checkout, "README.md")); string(raw) != "edited in chat\n" {
		t.Fatalf("second open replaced the user's edit: %q", raw)
	}
	if got := runGitT(t, checkout, "rev-parse", "HEAD"); got != head {
		t.Fatal("second open moved the checkout")
	}
}

// A SOP without preparation steps never stamps readiness: its nodes skip
// preparation. While a node runs, the IDE still joins that live environment.
func TestRunnerIDEJoinsRunningSOPWithoutPreparationSteps(t *testing.T) {
	d, request, root := runnerPreparationFixture(t)
	defer d.closeAllRunSandboxes(context.Background())
	request.RunStatus = "running"
	request.Workflow = &domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "agent", Type: "agent"}}}
	task := domain.RunnerTask{ID: "running-node", RunID: request.RunID, Workspace: "runs/prepared/workspace", Provider: "codebuddy", Sandbox: request.Sandbox}
	_, release, err := d.acquireTaskSandbox(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	gate := d.workspaceGate(request.RunID)
	gate.RLock()
	defer gate.RUnlock()

	request.Operation = domain.RunnerWorkspaceOperationPrepare
	result := d.readRunnerWorkspace(context.Background(), request)
	var state runtime.WorkspacePreparationStatus
	if err := json.Unmarshal(result.Payload, &state); result.Status != 200 || err != nil || state.Status != "ready" {
		t.Fatalf("prepare beside running node: %+v %v", result, err)
	}
	request.Operation = domain.RunnerWorkspaceOperationWrite
	request.ConcurrentEdit = true
	request.Path, request.Content, request.ExpectedSHA256 = "ide.txt", "edited while running\n", "missing"
	if result := d.readRunnerWorkspace(context.Background(), request); result.Status != 200 {
		t.Fatalf("write beside running node: %+v", result)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "ide.txt")); err != nil || string(raw) != "edited while running\n" {
		t.Fatalf("write landed: %q %v", raw, err)
	}
	request.Operation = domain.RunnerWorkspaceOperationTerminal
	request.TerminalOwner = 7
	request.Terminal = &domain.WorkspaceTerminalRequest{Operation: "open", ExpiresAt: time.Now().Add(time.Minute)}
	opened := d.readRunnerWorkspace(context.Background(), request)
	if opened.Status != 200 {
		t.Fatalf("terminal beside running node: %+v", opened)
	}
	d.workspaceTerminals.CloseScope("prepared:7")
}

// IDE edits share the workspace with running agents (gate readers) but stay
// excluded from preparation, restore and checkpoints (the exclusive holder).
func TestRunnerConcurrentEditSharesOnlyTheAgentFence(t *testing.T) {
	d, request, root := runnerPreparationFixture(t)
	defer d.closeAllRunSandboxes(context.Background())
	request.RunStatus = "running"
	request.ConcurrentEdit = true
	gate := d.workspaceGate(request.RunID)
	write := func(content string) domain.RunnerWorkspaceResponse {
		request.Operation = domain.RunnerWorkspaceOperationWrite
		request.Path, request.Content, request.ExpectedSHA256 = "ide.txt", content, "missing"
		return d.readRunnerWorkspace(context.Background(), request)
	}
	artifacts := func() domain.RunnerWorkspaceResponse {
		request.Operation = domain.RunnerWorkspaceOperationArtifacts
		return d.readRunnerWorkspace(context.Background(), request)
	}

	gate.Lock()
	if result := write("during preparation\n"); result.Status != 409 || result.Code != domain.WorkspaceErrorBusy {
		t.Fatalf("write during an exclusive hold: %+v", result)
	}
	if result := artifacts(); result.Status != 409 || result.Code != domain.WorkspaceErrorBusy {
		t.Fatalf("artifacts during an exclusive hold: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "ide.txt")); !os.IsNotExist(err) {
		t.Fatalf("write landed during an exclusive hold: %v", err)
	}
	gate.Unlock()

	gate.RLock()
	if result := write("beside a running agent\n"); result.Status != 200 {
		t.Fatalf("write beside a running agent: %+v", result)
	}
	if result := artifacts(); result.Status != 200 {
		t.Fatalf("artifacts beside a running agent: %+v", result)
	}
	gate.RUnlock()
	if raw, err := os.ReadFile(filepath.Join(root, "ide.txt")); err != nil || string(raw) != "beside a running agent\n" {
		t.Fatalf("write landed: %q %v", raw, err)
	}
	// The edit released its read hold: an exclusive holder can take the gate.
	if !gate.TryLock() {
		t.Fatal("concurrent edit leaked its gate hold")
	}
	gate.Unlock()
}
