package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestLifecycleResetSettleAndForceTerminate(t *testing.T) {
	script := domain.WorkflowNode{ID: "script", Type: "script"}
	agent := domain.WorkflowNode{ID: "agent", Type: "agent"}
	wf := domain.WorkflowDefinition{ID: "wf", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{script, agent}}
	run := domain.Run{ID: "life", Status: "running", WorkflowSnapshot: wf, Nodes: map[string]*domain.NodeRun{
		"script": {NodeID: "script", Status: "running", Attempt: 2, SessionID: "script-session"},
		"agent":  {NodeID: "agent", Status: "pending_input", Attempt: 1, SessionID: "agent-session"},
		"done":   {NodeID: "done", Status: "succeeded"},
	}}
	if continuingInterruptedAttempt(nil) || continuingInterruptedAttempt(&domain.NodeRun{Attempt: 1, Error: "x", Activity: "等待恢复"}) || !continuingInterruptedAttempt(&domain.NodeRun{Attempt: 1, Activity: "等待恢复"}) {
		t.Fatal("continuing attempt")
	}
	resetInFlightNodesForResume(&run, "等待恢复")
	if run.Nodes["script"].Status != "pending" || run.Nodes["script"].ResumeSessionID != "" {
		t.Fatalf("script reset = %+v", run.Nodes["script"])
	}
	if run.Nodes["agent"].Status != "pending" || run.Nodes["agent"].ResumeSessionID != "agent-session" {
		t.Fatalf("agent reset = %+v", run.Nodes["agent"])
	}
	resetNodeForResume(&run, nil, "x")
	resetNodeForResume(&run, run.Nodes["done"], "x")
	if !isTerminalRunStatus("cancelled") || isTerminalRunStatus("waiting") || hasRunningNode(run) {
		t.Fatal("status helpers")
	}
	run.Nodes["script"].Status, run.Nodes["agent"].Status = "succeeded", "succeeded"
	status, msg, ok := settleStatusForCompletedGraph(wf, run)
	if !ok || status != "succeeded" || msg == "" {
		t.Fatalf("settle success = %q %q %v", status, msg, ok)
	}
	run.Nodes["agent"].Status = "waiting_approval"
	if _, _, ok := settleStatusForCompletedGraph(wf, run); ok {
		t.Fatal("waiting graph settled")
	}
	run.Nodes["agent"].Status = "failed"
	if status, _, ok := settleStatusForCompletedGraph(wf, run); !ok || status != "failed" {
		t.Fatal("failed graph not settled")
	}
	groupWF := wf
	groupWF.CollaborationMode = "group"
	if _, _, ok := settleStatusForCompletedGraph(groupWF, run); ok {
		t.Fatal("group graph settled")
	}
	scheduled := run
	scheduled.ExecutionContext.ScheduleJobID = "job"
	scheduled.Repositories = []domain.RepositoryContext{{RepositoryID: "repo", WorkBranch: "work", IntegrateBranch: "integrate"}}
	if !scheduledIntegratePending(scheduled) || scheduledIntegratePending(run) {
		t.Fatal("scheduled integration")
	}
	if orphanSettleStatus("用户取消") != "cancelled" || orphanSettleStatus("lost") != "failed" {
		t.Fatal("orphan status")
	}

	e, st := cvEEngine(t)
	run.Status = "running"
	run.Nodes["script"].Status = "running"
	run.Nodes["agent"].Status = "waiting_approval"
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e.forceTerminate(run.ID, "failed", "boom")
	got, _ := st.RunState(run.ID)
	if got.Status != "failed" || got.Nodes["script"].Status != "failed" || got.Nodes["agent"].Status != "failed" {
		t.Fatalf("forced failure = %+v", got)
	}
	run.ID, run.Status = "life-cancel", "running"
	run.Nodes["script"].Status, run.Nodes["agent"].Status = "running", "waiting_approval"
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e.forceTerminate(run.ID, "cancelled", "cancel")
	got, _ = st.RunState(run.ID)
	if got.Status != "cancelled" || got.Nodes["script"].Status != "cancelled" {
		t.Fatalf("forced cancel = %+v", got)
	}
}

func TestArtifactFilesValidationCollectionAndRecovery(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "nested", "report.md"), []byte("report"), 0o644); err != nil {
		t.Fatal(err)
	}
	node := domain.WorkflowNode{ID: "make", Type: "agent", ArtifactOutputs: []domain.ArtifactContract{
		{Name: "report", Path: "nested/report.md", Required: true},
		{Name: "optional", Path: "optional.md"},
	}}
	run := domain.Run{ID: "artifact-run", Nodes: map[string]*domain.NodeRun{"make": {NodeID: "make", Attempt: 2}}}
	if err := validateArtifactContracts(workspace, run, node); err != nil {
		t.Fatal(err)
	}
	artifacts, err := collectArtifacts(workspace, run, node, "output")
	if err != nil || len(artifacts) != 1 || artifacts[0].Attempt != 2 || artifacts[0].Content != "report" {
		t.Fatalf("artifacts = %+v %v", artifacts, err)
	}
	result, err := collectArtifacts(t.TempDir(), run, domain.WorkflowNode{ID: "plain"}, "result")
	if err != nil || len(result) != 1 || result[0].Name != "result" {
		t.Fatalf("result artifact = %+v %v", result, err)
	}
	required := node
	required.ArtifactOutputs = []domain.ArtifactContract{{Name: "missing", Path: "missing.md", Required: true}}
	if err := validateArtifactContracts(workspace, run, required); err == nil {
		t.Fatal("missing required artifact accepted")
	}
	invalid := node
	invalid.ArtifactOutputs = []domain.ArtifactContract{{Name: "", Path: "../bad"}}
	if err := validateArtifactContracts(workspace, run, invalid); err == nil {
		t.Fatal("invalid artifact accepted")
	}
	if _, err := collectArtifactsMode(workspaceOutputs(workspace), run, invalid, "", false); err == nil {
		t.Fatal("invalid collection accepted")
	}
	if got, err := collectArtifactsBestEffort(workspace, run, invalid, ""); err != nil || len(got) != 0 {
		t.Fatalf("best effort invalid = %+v %v", got, err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "as-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	dirNode := node
	dirNode.ArtifactOutputs = []domain.ArtifactContract{{Name: "dir", Path: "as-dir", Required: true}}
	if err := validateArtifactContracts(workspace, run, dirNode); err == nil || !strings.Contains(err.Error(), "regular") {
		t.Fatalf("directory artifact = %v", err)
	}
	bigPath := filepath.Join(workspace, "big")
	if err := os.WriteFile(bigPath, make([]byte, maxArtifactBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	bigNode := node
	bigNode.ArtifactOutputs = []domain.ArtifactContract{{Name: "big", Path: "big", Required: true}}
	if err := validateArtifactContracts(workspace, run, bigNode); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("big artifact = %v", err)
	}

	host := t.TempDir()
	if err := os.WriteFile(filepath.Join(host, "recovered.md"), []byte("recovered"), 0o644); err != nil {
		t.Fatal(err)
	}
	recoverNode := domain.WorkflowNode{ID: "recover", ArtifactOutputs: []domain.ArtifactContract{{Name: "r", Path: "out/recovered.md"}}}
	recovered := recoverMisplacedArtifacts(workspace, host, recoverNode)
	if len(recovered) != 1 {
		t.Fatalf("recovered = %v", recovered)
	}
	if body, _ := os.ReadFile(filepath.Join(workspace, "out", "recovered.md")); string(body) != "recovered" {
		t.Fatalf("recovered body = %q", body)
	}
	recoverNode.ExecutionScope = domain.ExecutionScopeRepositories
	if got := recoverMisplacedArtifacts(workspace, host, recoverNode); got != nil {
		t.Fatalf("repository recovery = %v", got)
	}
	if findNamedFile("", "x", 2) != "" || findNamedFile(workspace, "", 2) != "" {
		t.Fatal("empty find")
	}
	if got := findNamedFile(workspace, "report.md", 4); !strings.HasSuffix(got, "report.md") {
		t.Fatal(got)
	}

	artifact := newArtifact(run.ID, "up", 1, "note", "note.md", "text/markdown", []byte("body"))
	artifact.RepositoryDir = "repo"
	if !strings.Contains(artifactInputRelPath(artifact), "repo") {
		t.Fatal(artifactInputRelPath(artifact))
	}
	var manifest strings.Builder
	writeInputManifestEntry(&manifest, artifact, artifactInputRelPath(artifact))
	if !strings.Contains(manifest.String(), artifact.SHA256) {
		t.Fatal(manifest.String())
	}
	direct := map[string]bool{"up": true}
	if !includeArtifact(direct, nil, artifact) || includeArtifact(nil, map[string]bool{"up": true}, domain.Artifact{NodeID: "up", Name: "repository-patch"}) {
		t.Fatal("artifact inclusion")
	}
	items := []domain.Artifact{artifact, artifact}
	items[1].Attempt = 2
	items[1].Content = "new"
	selected := latestInputArtifacts(items, direct, nil)
	if len(selected) != 1 || selected[0].Content != "new" {
		t.Fatalf("selected = %+v", selected)
	}
	wf := domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "up"}, {ID: "down"}}, Edges: []domain.WorkflowEdge{{Source: "up", Target: "down"}}}
	inputRun := domain.Run{Artifacts: []domain.Artifact{artifact}}
	inputManifest, err := materializeInputs(t.TempDir(), wf, wf.Nodes[1], inputRun)
	if err != nil || !strings.Contains(inputManifest, "note") {
		t.Fatalf("materialize = %q %v", inputManifest, err)
	}
}

func TestSOPHandoffArchiveInputsInheritanceAndDiffs(t *testing.T) {
	transcript := []byte(strings.Repeat("界", maxArtifactBytes/3+100))
	artifacts, err := buildSOPHandoffArtifacts("parent", "handoff", "# summary", transcript)
	if err != nil || len(artifacts) < 4 {
		t.Fatalf("built archive count=%d err=%v", len(artifacts), err)
	}
	ref := &domain.GroupSOPHandoff{
		ID: "handoff", NodeID: "resume", SummaryPath: artifacts[0].Path, TranscriptPath: artifacts[1].Path,
		SummaryArtifactID: artifacts[0].ID, TranscriptArtifactID: artifacts[1].ID,
	}
	parent := domain.Run{ID: "parent", ExecutionContext: domain.TelemetryContext{ProjectID: "p"}, WorkflowSnapshot: domain.WorkflowDefinition{GroupSOPHandoff: ref}, Artifacts: artifacts}
	selected, err := archivedSOPHandoffArtifacts(parent)
	if err != nil || len(selected) != len(artifacts) {
		t.Fatalf("archive selection = %d %v", len(selected), err)
	}
	full, err := readSOPHandoffTranscript(selected)
	if err != nil || string(full) != string(transcript) {
		t.Fatalf("read transcript len=%d %v", len(full), err)
	}
	manifest, files := sopHandoffInputs(parent)
	if len(files) != len(artifacts) || !strings.Contains(manifest, "sop-handoff") {
		t.Fatalf("inputs = %d %q", len(files), manifest)
	}
	dir := t.TempDir()
	if got, err := materializeSOPHandoffInputs(dir, parent); err != nil || !strings.Contains(got, "sop-handoff") {
		t.Fatalf("materialize handoff = %q %v", got, err)
	}
	if !strings.HasSuffix(sopHandoffInputPath(ref, "sop-handoff-conversation"), ".json") || !strings.HasSuffix(sopHandoffInputPath(ref, "sop-handoff-conversation-part-001"), ".txt") {
		t.Fatal("handoff input extensions")
	}
	child := domain.Run{ID: "child", ExecutionContext: domain.TelemetryContext{ProjectID: "p"}, WorkflowSnapshot: domain.WorkflowDefinition{}}
	if err := inheritSOPHandoff(parent, &child); err != nil || child.WorkflowSnapshot.GroupSOPHandoff == nil || len(child.Artifacts) != len(artifacts) {
		t.Fatalf("inherit = %+v %v", child.WorkflowSnapshot.GroupSOPHandoff, err)
	}
	foreign := child
	foreign.ExecutionContext.ProjectID = "other"
	if err := inheritSOPHandoff(parent, &foreign); err == nil {
		t.Fatal("cross-project handoff inherited")
	}
	if err := validateSOPHandoffRunnerPayload(parent, domain.RunnerTask{Prompt: strings.Repeat("x", 8<<20)}); err == nil {
		t.Fatal("oversized runner payload accepted")
	}
	if err := validateSOPHandoffRunnerPayload(domain.Run{}, domain.RunnerTask{}); err != nil {
		t.Fatal(err)
	}

	corrupt := parent
	corrupt.Artifacts = append([]domain.Artifact(nil), parent.Artifacts...)
	corrupt.Artifacts[0].SHA256 = "bad"
	if _, err := archivedSOPHandoffArtifacts(corrupt); err == nil {
		t.Fatal("corrupt summary accepted")
	}
	if _, err := readSOPHandoffTranscript(nil); err == nil {
		t.Fatal("empty transcript accepted")
	}
	if err := validateSOPHandoffArtifacts(domain.Run{}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceCleanupDecisionHelpers(t *testing.T) {
	now := time.Now().UTC()
	run := domain.Run{Status: "failed", UpdatedAt: now.Add(-idleFailedRunContainerTTL - time.Minute), Nodes: map[string]*domain.NodeRun{"n": {Status: "failed"}}}
	if !runContainerIdle(run, now) {
		t.Fatal("old failed run not idle")
	}
	run.UpdatedAt = now
	if runContainerIdle(run, now) {
		t.Fatal("fresh failed run idle")
	}
	run.Status, run.UpdatedAt = "waiting", now.Add(-idleWaitingRunContainerTTL-time.Minute)
	if !runContainerIdle(run, now) {
		t.Fatal("old waiting run not idle")
	}
	run.Nodes["n"].Status = "running"
	if runContainerIdle(run, now) {
		t.Fatal("running node idle")
	}
	run.Nodes["n"].Status = "failed"
	run.Status = "succeeded"
	if runContainerIdle(run, now) {
		t.Fatal("succeeded run idle")
	}
	run.Status = "failed"
	run.WorkspaceCleanedAt = now
	if runContainerIdle(run, now) {
		t.Fatal("cleaned run idle")
	}
	if !isLiveStatus("running") || !isLiveStatus("waiting") || isLiveStatus("failed") {
		t.Fatal("live status")
	}

	e, st := cvEEngine(t)
	if err := e.DeleteRun("missing"); err == nil {
		t.Fatal("missing run deleted")
	}
	active := domain.Run{ID: "active-delete", Status: "running", Nodes: map[string]*domain.NodeRun{}}
	if err := st.PutRun(active); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteRun(active.ID); err == nil {
		t.Fatal("active run deleted")
	}
	task := domain.Task{ID: "task-delete", RunID: "terminal-delete", Status: "running"}
	if err := st.PutTask(task); err != nil {
		t.Fatal(err)
	}
	terminal := domain.Run{ID: "terminal-delete", Status: "failed", ExecutionContext: domain.TelemetryContext{TaskID: task.ID}, Nodes: map[string]*domain.NodeRun{}}
	if err := st.PutRun(terminal); err != nil {
		t.Fatal(err)
	}
	if err := e.settleDeletedRunTasks(terminal); err != nil {
		t.Fatal(err)
	}
	tasks := st.Tasks()
	found := false
	for _, got := range tasks {
		if got.ID == task.ID {
			found = got.Status == "failed"
		}
	}
	if !found {
		t.Fatalf("task not settled: %+v", tasks)
	}
	terminal.Status = "unknown"
	if err := e.settleDeletedRunTasks(terminal); err != nil {
		t.Fatal(err)
	}
}

func TestSafeGitRefAndNodeFormattingHelpers(t *testing.T) {
	for in, want := range map[string]string{
		" feature/hello world ": "feature-hello-world",
		"..bad.lock":            "bad-lock",
		"":                      "run",
	} {
		if got := safeGitRefSegment(in); got != want {
			t.Fatalf("safeGitRefSegment(%q)=%q want %q", in, got, want)
		}
	}
	if dockerImageBuildLabel(nil) != "沙箱镜像" {
		t.Fatal("nil docker label")
	}
	snap := &domain.RunnerSandboxSpec{Dockerfile: &domain.DockerfileDefinition{ID: "id", Name: " Name "}}
	if dockerImageBuildLabel(snap) != "Name" || dockerfileID(snap) != "id" {
		t.Fatal("docker helpers")
	}
	snap.Dockerfile.Name = ""
	if dockerImageBuildLabel(snap) != "id" || dockerfileID(nil) != "" {
		t.Fatal("docker ID label")
	}
	for d, want := range map[time.Duration]string{-time.Second: "0 秒", 5 * time.Second: "5 秒", time.Minute: "1 分钟", 61 * time.Second: "1 分 1 秒"} {
		if got := formatElapsedCN(d); got != want {
			t.Fatalf("elapsed %v = %q", d, got)
		}
	}
	if scriptLanguageLabel("python") != "Python" || scriptLanguageLabel("shell") != "Shell" {
		t.Fatal("language labels")
	}
	if lostCLISession("", nil) || !lostCLISession("", errors.New("Session not found")) || !lostCLISession("No conversation found", nil) {
		t.Fatal("lost session")
	}
	if len(clipHumanGateDetail(strings.Repeat("x ", 400))) > 303 || clipHumanGateDetail(" a \n b ") != "a b" {
		t.Fatal("gate clipping")
	}
	if joinGateCommands([]string{" ", "echo one"}, nil) != "echo one" || !strings.HasPrefix(joinGateCommands([]string{"echo one", "echo two"}, nil), "set -e") {
		t.Fatal("gate command joining")
	}
}
