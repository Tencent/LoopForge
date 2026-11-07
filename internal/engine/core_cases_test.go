package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func cvEEngine(t *testing.T) (*Engine, *repo.Store) {
	t.Helper()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return New(st, nil), st
}

func TestWorkflowGraphValidationAndTraversal(t *testing.T) {
	valid := domain.WorkflowDefinition{
		ID: "cve-wf", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "a", Type: "agent"},
			{ID: "b", Type: "script", ScriptLanguage: "python", Script: "print(1)", ArtifactOutputs: []domain.ArtifactContract{{Name: "report", Path: "out/report.md"}}},
			{ID: "c", Type: "approval"},
		},
		Edges: []domain.WorkflowEdge{{Source: "a", Target: "b"}, {Source: "b", Target: "c"}},
	}
	if err := validate(valid); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{WorkflowSnapshot: valid, Nodes: map[string]*domain.NodeRun{
		"a": {Status: "succeeded"}, "b": {Status: "pending"}, "c": {Status: "pending"},
	}}
	if got := readyNodes(valid, run); len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("ready = %+v", got)
	}
	if got := ancestors(valid, "c"); !got["a"] || !got["b"] {
		t.Fatalf("ancestors = %v", got)
	}
	if got := descendants(valid, "a"); !got["b"] || !got["c"] {
		t.Fatalf("descendants = %v", got)
	}
	if !replayUpstreamSucceeded(run, "b") {
		t.Fatal("succeeded upstream rejected")
	}
	run.Nodes["a"].Status = "failed"
	if replayUpstreamSucceeded(run, "b") {
		t.Fatal("failed upstream accepted")
	}
	if allSucceeded(valid, run) {
		t.Fatal("non-succeeded graph accepted")
	}
	run.Nodes["c"].Status = "waiting_approval"
	if !hasWaiting(valid, run) {
		t.Fatal("waiting not found")
	}
	run.Nodes["a"].Status = "failed"
	if !hasFailed(valid, run) {
		t.Fatal("failure not found")
	}
	if max(2, 1) != 2 || max(1, 2) != 2 {
		t.Fatal("max")
	}

	cases := []domain.WorkflowDefinition{
		{ID: "x", Nodes: valid.Nodes},
		{ID: "x", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "", Type: "agent"}}},
		{ID: "x", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "a", Type: "agent"}, {ID: "a", Type: "agent"}}},
		{ID: "x", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "a", Type: "sop"}}},
		{ID: "x", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "a", Type: "sop", SOPWorkflowID: "x"}}},
		{ID: "x", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "a", Type: "script", ScriptLanguage: "ruby", Script: "x"}}},
		{ID: "x", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "a", Type: "script"}}},
		{ID: "x", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "a", Type: "bad"}}},
		{ID: "x", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "a", Type: "agent", OnFailure: "bad"}}},
		{ID: "x", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "a", Type: "agent", ExecutionScope: "bad"}}},
		{ID: "x", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "a", Type: "agent", RetryStrategy: "bad"}}},
		{ID: "x", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "a", Type: "agent", ArtifactOutputs: []domain.ArtifactContract{{Name: "", Path: "../bad"}}}}},
		{ID: "x", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "a", Type: "agent", ArtifactOutputs: []domain.ArtifactContract{{Name: "same", Path: "a"}, {Name: "same", Path: "b"}}}}},
		{ID: "x", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "a", Type: "agent", RejectTo: "missing"}}},
		{ID: "x", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "a", Type: "agent", OnFailure: "reject"}}},
		{ID: "x", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "a", Type: "agent"}}, Edges: []domain.WorkflowEdge{{Source: "a", Target: "missing"}}},
		{ID: "x", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "a", Type: "agent"}, {ID: "b", Type: "agent"}}, Edges: []domain.WorkflowEdge{{Source: "a", Target: "b"}, {Source: "b", Target: "a"}}},
	}
	for i, wf := range cases {
		if err := validate(wf); err == nil {
			t.Fatalf("case %d accepted: %+v", i, wf)
		}
	}
	command := domain.WorkflowDefinition{ID: "cmd", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "run", Type: "command", Command: "echo ok"}}}
	if err := validate(command); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryPatchParsingSummaryAndApply(t *testing.T) {
	diff := "diff --git a/old.txt b/new.txt\nsimilarity index 90%\nrename from old.txt\nrename to new.txt\n--- a/old.txt\n+++ b/new.txt\n@@ -1 +1,2 @@\n-old\n+new\n+more\n" +
		"diff --git a/add.bin b/add.bin\nnew file mode 100644\nGIT binary patch\n"
	files := parseUnifiedDiffFiles([]byte(diff))
	if len(files) != 2 || files[0].Status != "R" || files[0].RenameFrom != "old.txt" || files[0].Added != 2 || files[0].Deleted != 1 || files[1].Status != "B" || !files[1].Binary {
		t.Fatalf("files = %+v", files)
	}
	src, dst := splitGitDiffHeader(`diff --git a/file.txt b/file.txt`)
	if src != "file.txt" || dst != "file.txt" {
		t.Fatalf("header = %q %q", src, dst)
	}
	if a, b := splitGitDiffHeader("bad"); a != "" || b != "" {
		t.Fatalf("bad header = %q %q", a, b)
	}
	artifact := domain.Artifact{Content: diff}
	if repositoryPatchApplyBody(artifact) == "" || !strings.Contains(repositoryPatchSummary(artifact), "new.txt") {
		t.Fatal("diff not recognized")
	}
	plain := domain.Artifact{Content: " summary "}
	if repositoryPatchApplyBody(plain) != "" || repositoryPatchSummary(plain) != "summary" {
		t.Fatal("plain summary")
	}
	if !looksLikeGitDiff("\ufeff \n--- a/x") || looksLikeGitDiff("hello") {
		t.Fatal("diff detection")
	}

	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "cve@example.com"}, {"config", "user.name", "CvE"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("git", "-C", dir, "add", ".").Run()
	_ = exec.Command("git", "-C", dir, "commit", "-m", "base").Run()
	patch := "diff --git a/x.txt b/x.txt\n--- a/x.txt\n+++ b/x.txt\n@@ -1 +1 @@\n-old\n+new\n"
	if err := applyRepositoryPatch(dir, patch); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "x.txt"))
	if string(body) != "new\n" {
		t.Fatalf("applied = %q", body)
	}
	if err := applyRepositoryPatch(dir, "not a patch"); err != nil {
		t.Fatal(err)
	}
	if err := applyRepositoryPatch(dir, patch); err == nil {
		t.Fatal("invalid second apply succeeded")
	}
}

func TestExecutionTargetsAndTelemetryHelpers(t *testing.T) {
	if normalizeExecutionScope(" REPOSITORIES ") != domain.ExecutionScopeRepositories || normalizeExecutionScope("x") != domain.ExecutionScopeRun {
		t.Fatal("scope normalization")
	}
	run := domain.Run{Repositories: []domain.RepositoryContext{
		{RepositoryID: "one", Name: "One", WorkspaceDir: "repos/one"},
		{RepositoryID: "two", WorkspaceDir: "repos/two"},
	}}
	targets, err := executionTargets(run, domain.ExecutionScopeRepositories)
	if err != nil || len(targets) != 2 || targets[1].label() != "two" {
		t.Fatalf("targets = %+v %v", targets, err)
	}
	if _, err := executionTargets(domain.Run{}, domain.ExecutionScopeRepositories); err == nil {
		t.Fatal("repository-less run accepted")
	}
	command := bindExecutionTarget("pwd", targets[0])
	if !strings.Contains(command, "AGENTFLOW_REPOSITORY_ID='one'") || !strings.Contains(command, "cd -- 'repos/one'") {
		t.Fatal(command)
	}
	got, err := executeForTargets(run, domain.ExecutionScopeRepositories, func(target executionTarget) (string, error) {
		return target.label(), nil
	})
	if err != nil || !strings.Contains(got, "## One") || !strings.Contains(got, "## two") {
		t.Fatalf("output = %q %v", got, err)
	}
	if got, err := executeForTargets(run, domain.ExecutionScopeRepositories, func(target executionTarget) (string, error) {
		if target.Repository.RepositoryID == "two" {
			return "partial", errors.New("boom")
		}
		return "ok", nil
	}); err == nil || !strings.Contains(got, "partial") {
		t.Fatalf("error output = %q %v", got, err)
	}
	var event domain.TelemetryEvent
	sink := executionTargetSink(func(got domain.TelemetryEvent) { event = got }, targets[0])
	sink(domain.TelemetryEvent{Attributes: map[string]any{"x": 1}})
	if event.Attributes["repositoryId"] != "one" || event.Attributes["x"] != 1 {
		t.Fatalf("event = %+v", event)
	}

	for _, tc := range []struct {
		event domain.TelemetryEvent
		want  bool
	}{
		{domain.TelemetryEvent{Message: "useful", Source: domain.TelemetrySource{Adapter: "worker"}}, true},
		{domain.TelemetryEvent{Message: "x", Category: "reasoning"}, false},
		{domain.TelemetryEvent{Message: "x", Category: "usage"}, false},
		{domain.TelemetryEvent{Message: "x", Category: "output", Name: "text.delta"}, false},
		{domain.TelemetryEvent{Message: "x", Category: "model", Name: "model.request"}, false},
		{domain.TelemetryEvent{Message: "Bash 完成", Source: domain.TelemetrySource{Adapter: "worker"}}, false},
		{domain.TelemetryEvent{Message: "Bash 完成", Level: "error", Source: domain.TelemetrySource{Adapter: "worker"}}, true},
	} {
		if got := shouldRecordLog(tc.event); got != tc.want {
			t.Fatalf("shouldRecordLog(%+v) = %v", tc.event, got)
		}
	}
	for _, msg := range []string{"工具完成", "工具完成 Bash", "Read 完成: ok", "Read 结果 details"} {
		if !toolCompletionMessage(msg) {
			t.Fatalf("completion not detected: %q", msg)
		}
	}
	if toolCompletionMessage("正常消息") || looksLikeToolName("带中文") || !looksLikeToolName("mcp/read-file") {
		t.Fatal("tool name/completion")
	}
	if len(boundedLogBody(strings.Repeat("x", 70000))) <= 65536 || boundedLogBody("x") != "x" {
		t.Fatal("log bounding")
	}
	events := []domain.TelemetryEvent{{NodeID: "n", Category: "output", Name: "text.delta", Phase: "progress", Message: "a", Sequence: 1}}
	if !coalesceProgressTelemetry(&events, domain.TelemetryEvent{NodeID: "n", Category: "output", Name: "text.delta", Phase: "progress", Message: "b", Sequence: 2}) || events[0].Message != "ab" {
		t.Fatalf("coalesce = %+v", events)
	}
	if coalesceProgressTelemetry(&events, domain.TelemetryEvent{NodeID: "x", Category: "output", Name: "text.delta", Phase: "progress"}) {
		t.Fatal("unrelated event coalesced")
	}
	if !strings.Contains(compactProgressText(strings.Repeat("界", maxTelemetryProgressRunes+1)), "truncated") {
		t.Fatal("progress not compacted")
	}
}

func TestNodeOutcomePromptIntegrationAndHumanHelpers(t *testing.T) {
	if resolveGateOnFailure(" bad ", " HUMAN ") != "human" || resolveGateOnFailure("", "") != "fail" {
		t.Fatal("gate failure resolution")
	}
	if isMaxTurnsErr(nil) || !isMaxTurnsErr(errors.New("executor reached max turns")) {
		t.Fatal("max turns")
	}
	script := domain.WorkflowNode{Type: "script", RejectTo: "fix"}
	if !allowRejectOnFailure(script, errors.New("failed"), false) || allowRejectOnFailure(domain.WorkflowNode{Type: "agent", RejectTo: "fix"}, errors.New("without a terminal result"), false) {
		t.Fatal("reject policy")
	}
	merged := mergeArtifactContracts(
		[]domain.ArtifactContract{{Name: "report", Path: "old"}, {Path: "unnamed"}},
		[]domain.ArtifactContract{{Name: "report", Path: "new"}},
	)
	if len(merged) != 2 || merged[0].Path != "new" {
		t.Fatalf("contracts = %+v", merged)
	}
	run := domain.Run{ID: "r", Input: strings.Repeat("a", 80), Repository: domain.RepositoryContext{WorkBranch: "work"}, ExecutionContext: domain.TelemetryContext{IssueNumber: 42}}
	if got := integrationCommitLabel(run); !strings.Contains(got, "issue #42") || len([]rune(got)) > 90 {
		t.Fatal(got)
	}
	if got := integrateMRTitle(run, domain.RepositoryContext{}, "work", "main"); !strings.Contains(got, "issue #42") {
		t.Fatal(got)
	}
	run.Repository.AlignOnly = true
	if got := integrateMRDescription(run, "work", "main"); !strings.Contains(got, "Align") {
		t.Fatal(got)
	}
	wf := domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "fix", Type: "agent"}, {ID: "review", Type: "approval", RejectTo: "fix"}}}
	if integrationRepairNode(wf) != "fix" {
		t.Fatal("repair node")
	}

	ctxRun := domain.Run{Nodes: map[string]*domain.NodeRun{"n": {Attempt: 3}}}
	note := recordHumanContext(&ctxRun, "n", "reason", "context")
	if !strings.Contains(note, "context") || ctxRun.HumanContext != "context" || len(ctxRun.Artifacts) != 1 {
		t.Fatalf("human context = %+v %q", ctxRun, note)
	}
	if safePathSegment("!@#") != "artifact" || safePathSegment("a/b_c") != "ab_c" {
		t.Fatal("safe segment")
	}
	dir := t.TempDir()
	if err := writeHumanContextFile(dir, " note "); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(filepath.Join(dir, "human-context.md")); !strings.Contains(string(body), "note") {
		t.Fatalf("context file = %q", body)
	}
}

func TestWorkspaceTransportAndBrowserRunnerPaths(t *testing.T) {
	e, _ := cvEEngine(t)
	run := domain.Run{ID: "r", Status: "running", ExecutionContext: domain.TelemetryContext{
		ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: "runner", RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared,
	}}
	if response := e.HandleBrowserRequest(context.Background(), run, nil); response.Status != 400 {
		t.Fatalf("nil browser = %+v", response)
	}
	if response := e.HandleBrowserRequest(context.Background(), run, &domain.BrowserRequest{}); response.Status != 503 {
		t.Fatalf("missing transport = %+v", response)
	}
	e.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		if request.Operation != domain.RunnerWorkspaceOperationBrowser || request.Browser == nil {
			t.Fatalf("request = %+v", request)
		}
		return domain.RunnerWorkspaceResponse{Status: 201}, nil
	}
	if response := e.HandleBrowserRequest(context.Background(), run, &domain.BrowserRequest{}); response.Status != 201 {
		t.Fatalf("browser response = %+v", response)
	}
	request, err := e.BrowserWorkspaceRequest(run, &domain.BrowserRequest{})
	if err != nil || request.RunID != "r" || request.WorkspaceLayout != domain.RunnerWorkspaceLayoutShared {
		t.Fatalf("browser request = %+v %v", request, err)
	}

	var seenToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenToken = r.Header.Get(controlTokenHeader)
		var request domain.RunnerWorkspaceRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Operation == "unauthorized" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		if request.Operation == domain.RunnerWorkspaceOperationCheckpointStatus {
			http.Error(w, "old worker", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(domain.RunnerWorkspaceResponse{Status: 202})
	}))
	defer server.Close()
	response, err := postWorkspaceRequest(context.Background(), server.URL+"/", "token", domain.RunnerWorkspaceRequest{Operation: domain.RunnerWorkspaceOperationTerminal})
	if err != nil || response.Status != 202 || seenToken != "token" {
		t.Fatalf("post = %+v %v token=%q", response, err, seenToken)
	}
	response, err = postWorkspaceRequest(context.Background(), server.URL, "token", domain.RunnerWorkspaceRequest{Operation: domain.RunnerWorkspaceOperationCheckpointStatus})
	if err != nil || response.Status != 400 {
		t.Fatalf("compat status = %+v %v", response, err)
	}
	if _, err = postWorkspaceRequest(context.Background(), server.URL, "token", domain.RunnerWorkspaceRequest{Operation: "unauthorized"}); !errors.Is(err, ErrWorkspaceControlConfiguration) {
		t.Fatalf("unauthorized = %v", err)
	}
}

func TestSandboxSnapshotsPathsAndMisc(t *testing.T) {
	e, st := cvEEngine(t)
	profile := domain.SandboxProfile{ID: "bad", Driver: "docker"}
	if _, err := resolveSandboxResources(st, profile); err == nil {
		t.Fatal("docker without file accepted")
	}
	profile.DockerfileID = "missing"
	if _, err := resolveSandboxResources(st, profile); err == nil {
		t.Fatal("missing dockerfile accepted")
	}
	profile = domain.SandboxProfile{ID: "local", Driver: "local"}
	resources, err := resolveSandboxResources(st, profile)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := resolvedSandboxSnapshot(profile, resources)
	if snapshot.Profile.ID != "local" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if got := sandboxResourcesFromSnapshot(nil); got.Dockerfile.ID != "" {
		t.Fatalf("nil resources = %+v", got)
	}
	if safeRunnerPath(" a/b c ") != "a-b-c" || safeRunnerPath(" ") != "node" {
		t.Fatal("runner path")
	}
	run := domain.Run{ID: "r", ExecutionContext: domain.TelemetryContext{RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared}}
	if got := runnerPreviewWorkspaceRel(run); !strings.Contains(got, "workspace") {
		t.Fatal(got)
	}
	for _, operation := range []string{
		domain.RunnerWorkspaceOperationCheckpointStatus, domain.RunnerWorkspaceOperationCheckpointExport,
		domain.RunnerWorkspaceOperationCheckpointUpload, domain.RunnerWorkspaceOperationRestore,
		domain.RunnerWorkspaceOperationReclaim, domain.RunnerWorkspaceOperationPrepare,
		domain.RunnerWorkspaceOperationPreparationStatus, domain.RunnerWorkspaceOperationPreparationRecord,
		domain.RunnerWorkspaceOperationArtifacts, domain.RunnerWorkspaceOperationBrowser,
		domain.RunnerWorkspaceOperationTerminal, "unknown",
	} {
		if workspaceOperationLabel(operation) == "" {
			t.Fatalf("empty label for %q", operation)
		}
	}
	if e.defaultExecutionRunner(domain.Run{ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeCloud}}) != "" {
		t.Fatal("cloud runner")
	}
}

func TestObservationIngressWorkspaceStatusAndStateTransitions(t *testing.T) {
	e, st := cvEEngine(t)
	wf := domain.WorkflowDefinition{
		ID: "cve-state-wf", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "script", Name: "Script", Type: "script", Script: "echo ok"}, {ID: "gate", Name: "Gate", Type: "approval"}},
		Edges: []domain.WorkflowEdge{{Source: "script", Target: "gate"}},
	}
	run := domain.Run{ID: "cve-state", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "running", Nodes: map[string]*domain.NodeRun{
		"script": {NodeID: "script", Status: "running", Attempt: 1},
		"gate":   {NodeID: "gate", Status: "pending"},
	}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	events := []domain.TelemetryEvent{
		{Name: "worker.started", RunID: run.ID, NodeID: "script", Message: "started", Source: domain.TelemetrySource{Layer: "executor", Adapter: "worker"}},
		{Name: "ignored"},
		{Name: "missing", RunID: "not-a-run"},
	}
	if accepted := e.IngestOTLPTraces(events); accepted != 1 {
		t.Fatalf("accepted = %d", accepted)
	}
	if err := e.IngestWorkerEnvelope(run.ID, domain.WorkerEnvelope{}); err == nil {
		t.Fatal("bad spec accepted")
	}
	base := domain.WorkerEnvelope{SpecVersion: domain.ObservabilitySpecVersion, Type: "observation"}
	if err := e.IngestWorkerEnvelope(run.ID, base); err == nil {
		t.Fatal("nil observation accepted")
	}
	obs := domain.TelemetryEvent{Name: "x", Source: domain.TelemetrySource{Layer: "executor", Adapter: "worker"}, Context: domain.TelemetryContext{NodeID: "missing"}}
	base.Observation = &obs
	if err := e.IngestWorkerEnvelope(run.ID, base); err == nil {
		t.Fatal("unknown node accepted")
	}
	logEnvelope := domain.WorkerEnvelope{SpecVersion: domain.ObservabilitySpecVersion, Type: "log"}
	if err := e.IngestWorkerEnvelope(run.ID, logEnvelope); err == nil {
		t.Fatal("nil log accepted")
	}
	logEnvelope.Log = &domain.LogRecord{Body: "hello", Source: domain.TelemetrySource{Layer: "executor", Adapter: "worker"}, Context: domain.TelemetryContext{NodeID: "script"}}
	if err := e.IngestWorkerEnvelope(run.ID, logEnvelope); err != nil {
		t.Fatal(err)
	}
	if err := e.IngestWorkerEnvelope(run.ID, domain.WorkerEnvelope{SpecVersion: domain.ObservabilitySpecVersion, Type: "other"}); err == nil {
		t.Fatal("unknown envelope accepted")
	}
	if _, _, err := e.Subscribe("missing"); err == nil {
		t.Fatal("missing subscription accepted")
	}
	ch, cancel, err := e.Subscribe(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	default:
		t.Fatal("initial snapshot missing")
	}
	cancel()
	cancel()

	if _, err := e.WorkspacePreparationStatus(context.Background(), "missing"); err == nil {
		t.Fatal("missing workspace status accepted")
	}
	if _, err := e.EnsureWorkspaceReady(context.Background(), "missing"); err == nil {
		t.Fatal("missing workspace preparation accepted")
	}
	if _, err := e.TrustedWorkspaceRequest("missing", domain.RunnerWorkspaceRequest{}); err == nil {
		t.Fatal("missing trusted request accepted")
	}
	planRun := run
	planRun.WorkflowSnapshot.Nodes[0].RunOnWorkspaceRebuild = true
	planRun.Nodes["script"].Status = "failed"
	plan := workspacePreparationPlan(planRun)
	if plan.Nodes[0].RunOnWorkspaceRebuild {
		t.Fatal("failed preparation node retained")
	}
	if !workspaceCanRefreshRepositories(run) {
		t.Fatal("fresh workspace cannot refresh")
	}
	run.Nodes["gate"].Attempt = 2
	if workspaceCanRefreshRepositories(run) {
		t.Fatal("used workspace can refresh")
	}

	if _, err := st.RequestRunPause(run.ID); err != nil {
		t.Fatal(err)
	}
	e.nodeCancelled(run.ID, wf.Nodes[0])
	paused, _ := st.RunState(run.ID)
	if paused.Nodes["script"].Status != "waiting_approval" || !strings.Contains(paused.Nodes["script"].Activity, "脚本") {
		t.Fatalf("paused node = %+v", paused.Nodes["script"])
	}
	paused.Nodes["script"].Status = "running"
	if err := st.PutRun(paused); err != nil {
		t.Fatal(err)
	}
	if err := st.ClearRunControl(run.ID); err != nil {
		t.Fatal(err)
	}
	e.nodeCancelled(run.ID, wf.Nodes[0])
	cancelled, _ := st.RunState(run.ID)
	if cancelled.Nodes["script"].Status != "cancelled" {
		t.Fatalf("cancelled node = %+v", cancelled.Nodes["script"])
	}
}
