package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/observability"
	"agentflow/internal/runtime"
)

func TestIntegrationLabelsRepairAndPatchInventoryBranches(t *testing.T) {
	run := domain.Run{ID: "run", Input: strings.Repeat("题", 80) + "\nrest", ExecutionContext: domain.TelemetryContext{IssueNumber: 42}, Repository: domain.RepositoryContext{WorkBranch: "feature/work"}}
	if got := integrationCommitLabel(run); !strings.HasPrefix(got, "issue #42 ") || len([]rune(got)) > 82 {
		t.Fatalf("label=%q", got)
	}
	noTitle := run
	noTitle.Input = ""
	if integrationCommitLabel(noTitle) != "issue #42" {
		t.Fatal("issue-only label")
	}
	branch := domain.Run{Repository: domain.RepositoryContext{WorkBranch: "feature/x"}}
	if integrationCommitLabel(branch) != "feature/x" || integrationCommitLabel(domain.Run{}) != "scheduled SOP work" {
		t.Fatal("fallback labels")
	}
	if title := integrateMRTitle(run, domain.RepositoryContext{AlignOnly: true, DefaultBranch: "main"}, "work", "integration"); !strings.Contains(title, "main") {
		t.Fatalf("align title=%q", title)
	}
	if title := integrateMRTitle(domain.Run{}, domain.RepositoryContext{}, "work", "integration"); !strings.Contains(title, "work") {
		t.Fatalf("generic title=%q", title)
	}
	if title := integrateMRTitle(run, domain.RepositoryContext{}, "work", "integration"); !strings.Contains(title, "issue #42") {
		t.Fatalf("issue title=%q", title)
	}
	if description := integrateMRDescription(domain.Run{ID: "align", Repository: domain.RepositoryContext{AlignOnly: true}}, "main", "integration"); !strings.Contains(description, "Align") {
		t.Fatalf("align description=%q", description)
	}
	if description := integrateMRDescription(run, "work", "integration"); !strings.Contains(description, "issue: #42") {
		t.Fatalf("issue description=%q", description)
	}

	workflow := domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "writer", Type: "agent"}, {ID: "review", Type: "agent", RejectTo: "writer"}}}
	if integrationRepairNode(workflow) != "writer" {
		t.Fatal("reject target repair node")
	}
	if integrationRepairNode(domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "implementation", Type: "agent"}}}) != "implementation" {
		t.Fatal("named repair node")
	}
	if integrationRepairNode(domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "other", Type: "agent"}}}) != "other" {
		t.Fatal("first agent repair node")
	}
	if integrationRepairNode(domain.WorkflowDefinition{}) != "" {
		t.Fatal("empty workflow repair node")
	}
	if files := patchNewFiles("A\tone.go\nM\ttwo.go\nA\tthree.go"); len(files) != 2 {
		t.Fatalf("name-status files=%v", files)
	}
	diff := "diff --git a/new.go b/new.go\nnew file mode 100644\n--- /dev/null\n+++ b/new.go\n@@ -0,0 +1 @@\n+package new\n"
	if files := patchNewFiles(diff); len(files) != 1 || files[0] != "new.go" {
		t.Fatalf("diff files=%v", files)
	}

	workspace := t.TempDir()
	if out, err := os.Create(filepath.Join(workspace, "not-a-repo")); err != nil {
		t.Fatal(err)
	} else {
		_ = out.Close()
	}
	artifactRun := domain.Run{Artifacts: []domain.Artifact{{Name: "repository-patch", NodeID: "writer", Content: "A\tmissing.go"}}}
	if missing := (&Engine{}).verifyDeliveryCompleteness(artifactRun, workspace); len(missing) != 0 {
		t.Fatalf("non-git completeness=%v", missing)
	}
}

func TestObservabilityFilteringCoalescingAndFormattingBranches(t *testing.T) {
	base := domain.TelemetryEvent{Name: "event", Category: "process", Phase: "finish"}
	if attrs := logAttributes(base); len(attrs) != 3 {
		t.Fatalf("base attrs=%v", attrs)
	}
	base.Attributes = map[string]any{"input": "x", "status": nil, "ignored": true}
	if attrs := logAttributes(base); attrs["input"] != "x" {
		t.Fatalf("attrs=%v", attrs)
	}
	for _, event := range []domain.TelemetryEvent{
		{Source: domain.TelemetrySource{Adapter: observability.AdapterHooks}, Message: "x"},
		{Source: domain.TelemetrySource{Adapter: observability.AdapterOTEL}, Message: "x"},
		{Category: "reasoning", Message: "x"},
		{Category: "usage", Message: "x"},
		{Category: "output", Name: "text.delta", Message: "x"},
		{Category: "model", Name: "model.request", Message: "x"},
		{Category: "tool", Phase: "finish", Message: "x"},
		{Message: ""},
	} {
		if shouldRecordLog(event) {
			t.Fatalf("noisy event recorded: %+v", event)
		}
	}
	if !shouldRecordLog(domain.TelemetryEvent{Category: "process", Message: "useful"}) {
		t.Fatal("useful event dropped")
	}
	if isNoisyWorkerLog(domain.TelemetryEvent{Level: "error", Category: "tool", Phase: "finish"}) {
		t.Fatal("error log marked noisy")
	}
	for _, message := range []string{"工具完成", "工具完成 Bash", "Bash 完成", "Read 结果: ok"} {
		if !toolCompletionMessage(message) {
			t.Fatalf("completion not detected: %q", message)
		}
	}
	for _, message := range []string{"", "ordinary message", "中文 完成"} {
		if toolCompletionMessage(message) {
			t.Fatalf("false completion: %q", message)
		}
	}
	if !looksLikeToolName("mcp/tool-1") || looksLikeToolName(strings.Repeat("a", 65)) || looksLikeToolName("工具") {
		t.Fatal("tool name validation")
	}
	long := strings.Repeat("x", 64*1024+1)
	if got := boundedLogBody(long); !strings.Contains(got, "truncated") {
		t.Fatal("long log not bounded")
	}
	if boundedLogBody("short") != "short" {
		t.Fatal("short log changed")
	}

	events := []domain.TelemetryEvent{{NodeID: "node", Category: "output", Name: "text", Phase: "progress", Message: "a", Sequence: 1}}
	if !coalesceProgressTelemetry(&events, domain.TelemetryEvent{NodeID: "node", Category: "output", Name: "text", Phase: "progress", Message: "b", Sequence: 2}) || events[0].Message != "ab" {
		t.Fatalf("coalesced=%v", events)
	}
	if coalesceProgressTelemetry(&events, domain.TelemetryEvent{NodeID: "other", Category: "output", Name: "text", Phase: "progress", Message: "x"}) {
		t.Fatal("cross-node telemetry coalesced")
	}
	if coalesceProgressTelemetry(&events, domain.TelemetryEvent{NodeID: "node", Category: "usage", Phase: "progress", Message: "x"}) {
		t.Fatal("usage telemetry coalesced")
	}
	if got := compactProgressText(strings.Repeat("界", maxTelemetryProgressRunes+10)); !strings.Contains(got, "stream truncated") {
		t.Fatal("progress not compacted")
	}
	run := domain.Run{ID: "run"}
	appendControlTelemetry(&run, "node", "approved", "ok")
	if run.TelemetrySequence != 1 || len(run.Telemetry) != 1 {
		t.Fatalf("control telemetry=%v", run.Telemetry)
	}
}

func TestRepositoryPatchParsingAndGateResultErrors(t *testing.T) {
	diff := "diff --git a/old.txt b/new.txt\nsimilarity index 100%\nrename from old.txt\nrename to new.txt\ndiff --git a/add.txt b/add.txt\nnew file mode 100644\n--- /dev/null\n+++ b/add.txt\n@@ -0,0 +1 @@\n+x\n"
	files := parseUnifiedDiffFiles([]byte(diff))
	if len(files) != 2 {
		t.Fatalf("parsed files=%+v", files)
	}
	if summary := summarizeRepositoryPatch([]byte(diff)); !strings.Contains(summary, "add.txt") {
		t.Fatalf("summary=%q", summary)
	}
	if left, right := splitGitDiffHeader(`diff --git a/file-one b/file-one`); left == "" || right == "" {
		t.Fatalf("split=%q %q", left, right)
	}
	if left, right := splitGitDiffHeader("invalid"); left != "" || right != "" {
		t.Fatal("invalid diff header split")
	}
	workspace := t.TempDir()
	if result, err := readGateResultFrom(workspaceOutputs(workspace), "missing"); err != nil || result != nil {
		t.Fatalf("missing gate result=%v err=%v", result, err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "gate.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readGateResultFrom(workspaceOutputs(workspace), "gate.json"); err == nil {
		t.Fatal("invalid gate JSON accepted")
	}
	if formatElapsedCN(2*time.Hour) != "120 分钟" {
		t.Fatal("long elapsed formatting")
	}
}

func TestObservationQueuesRunStateAndFlushLoopBranches(t *testing.T) {
	var nilEngine *Engine
	nilEngine.enqueueObservation("run", "node", domain.TelemetryEvent{})
	nilEngine.signalObservationFlush()
	nilEngine.flushAllObservations()
	nilEngine.observationFlushLoop(context.Background())

	e, _ := cvEEngine(t)
	e.obsPending = []pendingObservation{
		{runID: "one", nodeID: "a"},
		{runID: "one", nodeID: "b"},
		{runID: "two", nodeID: "c"},
	}
	if ids := e.pendingObservationRunIDs(); len(ids) != 2 {
		t.Fatalf("pending ids=%v", ids)
	}
	if taken := e.takePendingObservations("one"); len(taken) != 2 || len(e.obsPending) != 1 {
		t.Fatalf("taken=%v kept=%v", taken, e.obsPending)
	}
	if taken := e.takePendingObservations("missing"); len(taken) != 0 {
		t.Fatalf("unexpected observations=%v", taken)
	}
	e.obsKick = make(chan struct{}, 1)
	e.signalObservationFlush()
	e.signalObservationFlush()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.observationFlushLoop(ctx)
	if updated, err := e.updateRunAt("missing", false, nil); updated || err != nil {
		t.Fatalf("missing run update returned updated=%t, err=%v", updated, err)
	}
	if _, _, err := e.Subscribe("missing"); err == nil {
		t.Fatal("missing subscription succeeded")
	}
	stop := make(chan struct{})
	close(stop)
	e.pollRun("missing", stop, 0)
	e.publish("missing")
	ch := make(chan domain.Run, 1)
	ch <- domain.Run{ID: "old"}
	e.subs["stream"] = map[chan domain.Run]struct{}{ch: {}}
	e.publishRun(domain.Run{ID: "stream", Revision: 2})
	if got := <-ch; got.Revision != 2 {
		t.Fatalf("published=%+v", got)
	}
	run := domain.Run{ID: "terminal", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
	e.applyObservation(&run, "", domain.TelemetryEvent{Name: "ignored"})
	if len(run.Telemetry) != 0 {
		t.Fatal("terminal observation applied")
	}
	e.applyObservation(&run, "", domain.TelemetryEvent{Name: "sandbox.closed", Message: "closed"})
	if len(run.Telemetry) != 1 {
		t.Fatal("sandbox close observation missing")
	}
}

func TestNodeOutcomeDecisionAndTransitionBranches(t *testing.T) {
	run := domain.Run{Generation: 2}
	nodeRun := &domain.NodeRun{Status: "running", Attempt: 3}
	if shouldCommitNodeOutcome(run, nodeRun, 1, 3, true) || shouldCommitNodeOutcome(run, nil, 2, 3, true) {
		t.Fatal("stale outcome accepted")
	}
	nodeRun.Status = "succeeded"
	if shouldCommitNodeOutcome(run, nodeRun, 2, 3, true) {
		t.Fatal("completed outcome accepted")
	}
	nodeRun.Status = "running"
	if !shouldCommitNodeOutcome(run, nodeRun, 2, 9, true) || shouldCommitNodeOutcome(run, nodeRun, 2, 2, false) || !shouldCommitNodeOutcome(run, nodeRun, 2, 3, false) {
		t.Fatal("attempt outcome fencing")
	}
	if shouldRetryFailedAttempt(domain.WorkflowNode{MaxAttempts: 3}, 1, ErrWorkspaceControlConfiguration) {
		t.Fatal("configuration failure retried")
	}
	if shouldRetryFailedAttempt(domain.WorkflowNode{MaxAttempts: 3}, 1, runtime.ErrMaxTurns) {
		t.Fatal("max turns retried")
	}
	if shouldRetryFailedAttempt(domain.WorkflowNode{MaxAttempts: 3}, 1, errors.New("verification failed")) {
		t.Fatal("verification failure retried")
	}
	if !shouldRetryFailedAttempt(domain.WorkflowNode{MaxAttempts: 3}, 1, errors.New("ordinary")) {
		t.Fatal("ordinary failure not retried")
	}
	nodeRun.GateResult = &domain.GateResult{Decision: " ReWork "}
	if !gateReworkReject(true, nodeRun) || gateReworkReject(false, nodeRun) || gateReworkReject(true, nil) {
		t.Fatal("gate rework decision")
	}
	if resolveGateOnFailure("invalid", " HUMAN ") != "human" || resolveGateOnFailure("", "") != "fail" {
		t.Fatal("gate failure resolution")
	}
	if !isMaxTurnsErr(errors.New("executor reached max turns")) || isMaxTurnsErr(nil) {
		t.Fatal("max-turn detection")
	}
	script := domain.WorkflowNode{Type: "script", RejectTo: "upstream"}
	if !nodeRejects(script) || !allowRejectOnFailure(script, errors.New("boom"), false) || allowRejectOnFailure(domain.WorkflowNode{}, errors.New("boom"), false) {
		t.Fatal("reject-on-failure decision")
	}
	agent := domain.WorkflowNode{Type: "agent", RejectTo: "upstream"}
	if !allowRejectOnFailure(agent, nil, true) || allowRejectOnFailure(agent, errors.New("without a terminal result"), false) || allowRejectOnFailure(agent, runtime.ErrMaxTurns, false) || !allowRejectOnFailure(agent, errors.New("verification gate failed"), false) {
		t.Fatal("agent reject-on-failure decision")
	}
	merged := mergeArtifactContracts(
		[]domain.ArtifactContract{{Name: "same", Path: "old"}, {Path: "path-only"}},
		[]domain.ArtifactContract{{Name: "same", Path: "new"}},
	)
	if len(merged) != 2 || merged[0].Path != "new" {
		t.Fatalf("merged contracts=%v", merged)
	}

	e, st := cvEEngine(t)
	e.mu.Lock()
	e.executeRuns = false
	e.mu.Unlock()
	closedIssue := domain.Issue{
		ID: "closed-gongfeng-more", ProjectID: "platform", Title: "Closed", Source: "gongfeng",
		ExternalRef: &domain.ExternalRef{ExternalState: "closed"},
	}
	if err := st.PutIssue(closedIssue); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(forbidClosedGongfengIssue(st, domain.Run{ExecutionContext: domain.TelemetryContext{IssueID: closedIssue.ID}}), errGongfengIssueClosed) {
		t.Fatal("closed Gongfeng issue accepted")
	}
	if err := forbidClosedGongfengIssue(st, domain.Run{ExecutionContext: domain.TelemetryContext{IssueID: "missing"}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, typ string
		pause   bool
		want    string
	}{
		{id: "cancel-node", typ: "agent", want: "cancelled"},
		{id: "pause-agent", typ: "agent", pause: true, want: "waiting_approval"},
		{id: "pause-script", typ: "script", pause: true, want: "waiting_approval"},
	} {
		stored := domain.Run{ID: tc.id, Status: "running", PauseRequested: tc.pause, Nodes: map[string]*domain.NodeRun{"node": {NodeID: "node", Status: "running"}}}
		if err := st.PutRun(stored); err != nil {
			t.Fatal(err)
		}
		e.nodeCancelled(tc.id, domain.WorkflowNode{ID: "node", Name: "Node", Type: tc.typ})
		got, _ := st.RunState(tc.id)
		if got.Nodes["node"].Status != tc.want {
			t.Fatalf("%s node=%+v", tc.id, got.Nodes["node"])
		}
	}
	interrupted := domain.Run{ID: "interrupted-node", Status: "running", Generation: 0, Nodes: map[string]*domain.NodeRun{"node": {NodeID: "node", Status: "running", Attempt: 2}}}
	if err := st.PutRun(interrupted); err != nil {
		t.Fatal(err)
	}
	e.requeueInterruptedNode(interrupted.ID, "node", 2, 0)
	interruptedGot, _ := st.RunState(interrupted.ID)
	if interruptedGot.Nodes["node"].Status != "pending" {
		t.Fatalf("interrupted node=%+v", interruptedGot.Nodes["node"])
	}
}

func TestRepositoryPatchRunnerQueueAndArtifactBranches(t *testing.T) {
	workspace := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if out, err := exec.Command("git", append([]string{"-C", workspace}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", workspace, "add", ".").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", workspace, "commit", "-qm", "base").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	patch := "diff --git a/base.txt b/base.txt\n--- a/base.txt\n+++ b/base.txt\n@@ -1 +1 @@\n-old\n+new\n"
	if err := applyRepositoryPatch(workspace, patch); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(workspace, "base.txt")); string(data) != "new\n" {
		t.Fatalf("patched content=%q", data)
	}
	if err := applyRepositoryPatch(workspace, "not a patch"); err != nil {
		t.Fatal(err)
	}
	if err := applyRepositoryPatch(workspace, patch); err == nil {
		t.Fatal("duplicate patch unexpectedly applied")
	}

	artifactRoot := t.TempDir()
	contracts := []domain.ArtifactContract{{Name: "result", Path: "nested/result.txt"}, {Name: "ignored"}}
	if err := restoreRunnerArtifacts(artifactRoot, contracts, map[string]string{"result": "ok", "unknown": "skip", "ignored": "skip"}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(artifactRoot, "nested", "result.txt")); err != nil || string(data) != "ok" {
		t.Fatalf("restored artifact=%q err=%v", data, err)
	}
	if err := restoreRunnerArtifacts(artifactRoot, nil, map[string]string{"x": "x"}); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := restoreRunnerArtifacts(blocked, []domain.ArtifactContract{{Name: "x", Path: "child/out"}}, map[string]string{"x": "x"}); err == nil {
		t.Fatal("artifact under file unexpectedly restored")
	}

	e, st := cvEEngine(t)
	var notified []string
	e.SetRunnerDispatchNotify(func(provider string) { notified = append(notified, provider) })
	runner := cvERunner(t, e, "queue-runner-more", domain.RunnerStatusOnline, "terminal")
	run := cvERunnerRun("queue-run-more", runner.ID)
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := e.queueRunnerRunCleanup(run); err != nil {
		t.Fatal(err)
	}
	if err := e.queueRunnerRunCleanup(run); err != nil {
		t.Fatal(err)
	}
	if task, ok := st.RunnerTask("rt-cleanup-" + safeRunnerPath(run.ID)); !ok || task.Action != domain.RunnerTaskActionCleanupRun {
		t.Fatalf("cleanup task=%+v ok=%v", task, ok)
	}
	if err := e.queueRunnerPreviewArchive(run, domain.RunnerPreviewArchiveSpec{}); err != nil {
		t.Fatal(err)
	}
	if err := e.queueRunnerPreviewArchive(run, domain.RunnerPreviewArchiveSpec{}); err != nil {
		t.Fatal(err)
	}
	if len(notified) != 2 {
		t.Fatalf("dispatch notifications=%v", notified)
	}
	noRunner := cvERunnerRun("no-runner-more", "")
	if err := e.queueRunnerRunCleanup(noRunner); err != nil {
		t.Fatal(err)
	}
	if err := e.queueRunnerPreviewArchive(noRunner, domain.RunnerPreviewArchiveSpec{}); err == nil {
		t.Fatal("preview archive without runner accepted")
	}
	missingRunner := cvERunnerRun("missing-runner-more", "gone")
	if err := e.queueRunnerRunCleanup(missingRunner); err != nil {
		t.Fatal(err)
	}
	if err := e.queueRunnerPreviewArchive(missingRunner, domain.RunnerPreviewArchiveSpec{}); err == nil {
		t.Fatal("preview archive missing runner accepted")
	}

	if err := e.requireRunnerProvider("missing", "terminal"); err == nil {
		t.Fatal("missing provider runner accepted")
	}
	runner.Status = domain.RunnerStatusOffline
	if err := st.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	if err := e.requireRunnerProvider(runner.ID, "terminal"); err == nil {
		t.Fatal("offline runner accepted")
	}
	runner.Status = domain.RunnerStatusOnline
	runner.Runtimes = nil
	if err := st.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	if err := e.requireRunnerProvider(runner.ID, "terminal"); err == nil {
		t.Fatal("unsupported provider accepted")
	}
	runner.Runtimes = []domain.RunnerRuntime{{Provider: "terminal", Status: "ready", AuthMode: domain.RuntimeAuthLocal}}
	if err := st.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	if err := e.requireRunnerProvider(runner.ID, "terminal"); err != nil {
		t.Fatal(err)
	}

	done := domain.RunnerTask{ID: "await-done-more", RunnerID: runner.ID, Provider: "terminal", Status: domain.RunnerTaskSuccess}
	if err := st.PutRunnerTask(done); err != nil {
		t.Fatal(err)
	}
	if got, err := e.awaitRunnerTask(context.Background(), done.ID, func(domain.TelemetryEvent) {}); err != nil || got.Status != domain.RunnerTaskSuccess {
		t.Fatalf("await task=%+v err=%v", got, err)
	}
	if _, err := e.awaitRunnerTask(context.Background(), "missing-task-more", nil); err == nil {
		t.Fatal("missing awaited task accepted")
	}
	pending := domain.RunnerTask{ID: "await-cancel-more", RunnerID: runner.ID, Provider: "terminal", Status: domain.RunnerTaskLeased}
	if err := st.PutRunnerTask(pending); err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	var statusEvents int
	if _, err := e.awaitRunnerTask(cancelCtx, pending.ID, func(domain.TelemetryEvent) { statusEvents++ }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled await err=%v", err)
	}
	if statusEvents == 0 {
		t.Fatal("leased runner status was not reported")
	}
}

func TestPromptObservabilityAlignAndResumeRemainingBranches(t *testing.T) {
	node := domain.WorkflowNode{ID: "review", RejectTo: "writer"}
	role := &domain.AgentDefinition{Name: "评审", Prompt: "{{pipelineUrl}}\n{{humanContext}}\n{{feedback}}"}
	run := domain.Run{
		Input:        "input",
		HumanContext: "human note",
		Repository: domain.RepositoryContext{
			RepositoryID: "repo", Name: "Repo", PipelineURLPattern: "https://ci/{build_id}",
		},
		Nodes: map[string]*domain.NodeRun{"review": {Feedback: "fix"}},
	}
	prompt := applyPromptPlaceholders(nil, domain.WorkflowDefinition{ID: "issue-flow"}, node, role, run)
	if !strings.Contains(prompt, "https://ci/current") || !strings.Contains(prompt, "人工补充上下文") {
		t.Fatalf("prompt=%q", prompt)
	}
	run.Repository.PipelineURLPattern = ""
	run.Repository.PipelineProjectID, run.Repository.PipelineDefID = 12, 34
	if prompt = applyPromptPlaceholders(nil, domain.WorkflowDefinition{}, node, role, run); !strings.Contains(prompt, "未配置 Pipeline") {
		t.Fatalf("pipeline prompt=%q", prompt)
	}
	continued := domain.Run{
		Repository: domain.RepositoryContext{RepositoryID: "repo", Name: "Repo", RequireExistingWorkBranch: true, WorkBranch: "feature", BaseBranch: "main"},
		Artifacts:  []domain.Artifact{{NodeID: "mr", Name: "mr-body", Content: "merge_requests/1"}},
	}
	if blurb := existingMRBlurb(nil, continued); !strings.Contains(blurb, "merge_requests/1") {
		t.Fatalf("continued MR=%q", blurb)
	}
	longMR := domain.Run{Artifacts: []domain.Artifact{
		{NodeID: "mr", Name: "mr-body", Attempt: 1, CreatedAt: time.Now().Add(-time.Hour), Content: "merge_requests/old"},
		{NodeID: "mr", Name: "mr-body", Attempt: 1, CreatedAt: time.Now(), Content: "merge_requests/" + strings.Repeat("x", 3000)},
	}}
	if blurb := existingMRBlurb(nil, longMR); !strings.Contains(blurb, "…") || strings.Contains(blurb, "old") {
		t.Fatalf("latest clipped MR=%q", blurb)
	}

	var event *domain.TelemetryEvent
	clarifyProcessLog(event)
	r := domain.Run{}
	nr := domain.NodeRun{SessionID: "session"}
	applyUsage(nil, &nr, "", domain.TokenUsage{})
	applyUsage(&r, nil, "", domain.TokenUsage{})
	applyUsage(&r, &nr, "", domain.TokenUsage{CostStatus: "known"})
	if nr.UsageCursor.CostStatus != "known" {
		t.Fatalf("usage cursor=%+v", nr.UsageCursor)
	}
	telemetry := []domain.TelemetryEvent{
		{NodeID: "node", Name: "agent.completed"},
		{NodeID: "node", Category: "process", Name: "process.finished", Source: domain.TelemetrySource{Layer: "executor"}},
		{NodeID: "node", Category: "verification", Name: "process.finished"},
		{NodeID: "node", Category: "verification", Name: "process.finished", Level: "error"},
		{NodeID: "node", Name: "agent.loop", Message: "again"},
		{NodeID: "node", Category: "tool", Name: "tool.call", Phase: "finish"},
	}
	retroRun := domain.Run{Telemetry: telemetry}
	for _, tc := range []struct {
		err      error
		strategy string
	}{
		{errors.New("without a terminal result"), "loop"},
		{errors.New("No such file"), "reject"},
		{errors.New("ordinary"), "fail"},
		{errors.New("ordinary"), "human"},
		{nil, ""},
	} {
		if got := buildLoopRetro(retroRun, "node", 2, tc.err, tc.strategy); !strings.Contains(got, "Loop 复盘") {
			t.Fatalf("retro=%q", got)
		}
	}
	missingResult := domain.Run{Telemetry: []domain.TelemetryEvent{{NodeID: "node", Category: "process", Name: "process.finished", Source: domain.TelemetrySource{Layer: "executor"}}}}
	_ = buildLoopRetro(missingResult, "node", 1, errors.New("without a terminal result"), "loop")
	_ = buildLoopRetro(missingResult, "node", 1, errors.New("ordinary"), "loop")

	align := domain.Run{
		Repositories: []domain.RepositoryContext{{AlignOnly: true}},
		WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{
			{ID: "missing", Type: "approval"}, {ID: "done", Type: "approval"}, {ID: "wait", Type: "approval"},
		}},
		Nodes: map[string]*domain.NodeRun{"done": {Status: "succeeded"}, "wait": {Status: "waiting_approval"}},
	}
	if !alignOnlyRun(align) || !alignRunWaitingApproval(align) {
		t.Fatal("align-only run not recognized")
	}
	if !autoSucceedAlignApprovals(&align, time.Now()) || align.Nodes["wait"].Status != "succeeded" {
		t.Fatal("align approval not completed")
	}
	if alignRunWaitingApproval(align) {
		t.Fatal("completed align still waiting")
	}
	if (&AlignConflictError{Integrate: "i", Default: "d"}).Error() == "" {
		t.Fatal("empty conflict error")
	}
	if (&AlignConflictError{}).RenderBlocks() != "" {
		t.Fatal("empty blocks rendered")
	}
	if ShouldAlignIntegrate(domain.RepositoryContext{IntegrateBranch: "main", DefaultBranch: "main"}) {
		t.Fatal("equal branches require alignment")
	}

	if got := resumeNodeID(domain.Run{Nodes: map[string]*domain.NodeRun{"failed": {Status: "failed"}}}); got != "failed" {
		t.Fatalf("unordered failed resume=%q", got)
	}
	wf := domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "a"}, {ID: "b"}}, Edges: []domain.WorkflowEdge{{Source: "a", Target: "b"}}}
	if got := resumeNodeID(domain.Run{WorkflowSnapshot: wf, Nodes: map[string]*domain.NodeRun{"a": {Status: "succeeded"}, "b": {Status: "pending"}}}); got != "b" {
		t.Fatalf("ready resume=%q", got)
	}
	if got := resumeNodeID(domain.Run{WorkflowSnapshot: wf, Nodes: map[string]*domain.NodeRun{"a": {Status: "running"}, "b": {Status: "pending"}}}); got != "a" {
		t.Fatalf("fallback resume=%q", got)
	}
}
