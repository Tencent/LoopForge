package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestRunOperationsAdditionalValidationAndSettling(t *testing.T) {
	e, st := cvEEngine(t)
	e.mu.Lock()
	e.executeRuns = false
	e.mu.Unlock()
	if err := e.Cancel("missing"); err == nil {
		t.Fatal("missing cancel succeeded")
	}
	if err := e.Pause("missing"); err == nil {
		t.Fatal("missing pause succeeded")
	}
	if err := e.Approve("missing", "node", ""); err == nil {
		t.Fatal("missing approve succeeded")
	}
	if err := e.Pass("missing", "node", ""); err == nil {
		t.Fatal("missing pass succeeded")
	}
	if err := e.Reject("missing", domain.RejectRequest{}); err == nil {
		t.Fatal("missing reject succeeded")
	}
	if err := e.Retry("missing", "node", "", ""); err == nil {
		t.Fatal("missing retry succeeded")
	}
	if err := e.ResumePaused("missing", ""); err == nil {
		t.Fatal("missing resume succeeded")
	}
	if err := e.RefreshRunSOP("missing"); err == nil {
		t.Fatal("missing refresh succeeded")
	}
	if err := e.DeclineRetry("missing", "node"); err == nil {
		t.Fatal("missing decline succeeded")
	}

	terminal := domain.Run{ID: "ops-terminal", Status: "succeeded", Nodes: map[string]*domain.NodeRun{"node": {NodeID: "node", Status: "succeeded"}}}
	if err := st.PutRun(terminal); err != nil {
		t.Fatal(err)
	}
	if err := e.Cancel(terminal.ID); err == nil || !strings.Contains(err.Error(), "not active") {
		t.Fatalf("terminal cancel err=%v", err)
	}
	terminal.ID = "ops-stale-terminal"
	terminal.Nodes["node"] = &domain.NodeRun{NodeID: "node", Status: "running"}
	if err := st.PutRun(terminal); err != nil {
		t.Fatal(err)
	}
	if err := e.Cancel(terminal.ID); err != nil {
		t.Fatal(err)
	}
	stale, _ := st.RunState(terminal.ID)
	if stale.Status != "cancelled" || stale.Nodes["node"].Status != "cancelled" {
		t.Fatalf("stale terminal=%+v", stale)
	}

	waiting := domain.Run{
		ID: "ops-waiting", Status: "waiting",
		WorkflowSnapshot: domain.WorkflowDefinition{
			ID:    "ops-workflow",
			Nodes: []domain.WorkflowNode{{ID: "work", Type: "script"}, {ID: "gate", RejectTo: "work", MaxReworks: 1}},
			Edges: []domain.WorkflowEdge{{Source: "work", Target: "gate"}},
		},
		Nodes: map[string]*domain.NodeRun{
			"work": {NodeID: "work", Status: "succeeded", Attempt: 1},
			"gate": {NodeID: "gate", Status: "waiting_approval", Attempt: 1},
		},
	}
	if err := st.PutRun(waiting); err != nil {
		t.Fatal(err)
	}
	if err := e.Approve(waiting.ID, "missing", ""); err == nil {
		t.Fatal("missing approval node accepted")
	}
	if err := e.Approve(waiting.ID, "gate", "", NodeExpectation{NodeID: "gate", Status: "waiting_approval", Attempt: 9}); err == nil {
		t.Fatal("stale approval accepted")
	}
	if err := e.Approve(waiting.ID, "gate", "approved context"); err != nil {
		t.Fatal(err)
	}
	approved, _ := st.RunState(waiting.ID)
	if approved.Nodes["gate"].Status != "succeeded" || approved.Status != "running" || len(approved.Artifacts) == 0 {
		t.Fatalf("approved=%+v", approved)
	}

	failed := waiting
	failed.ID = "ops-failed"
	failed.Status = "failed"
	failed.Artifacts = nil
	failed.Nodes = map[string]*domain.NodeRun{
		"work": {NodeID: "work", Status: "failed", Attempt: 2},
		"gate": {NodeID: "gate", Status: "pending"},
	}
	if err := st.PutRun(failed); err != nil {
		t.Fatal(err)
	}
	if err := e.Pass(failed.ID, "missing", ""); err == nil {
		t.Fatal("missing pass node accepted")
	}
	if err := e.Pass(failed.ID, "work", "operator pass context"); err != nil {
		t.Fatal(err)
	}
	passed, _ := st.RunState(failed.ID)
	if passed.Nodes["work"].Status != "succeeded" || passed.Status != "running" {
		t.Fatalf("passed=%+v", passed)
	}

	rejectRun := waiting
	rejectRun.ID = "ops-reject"
	rejectRun.Nodes = map[string]*domain.NodeRun{
		"work": {NodeID: "work", Status: "succeeded"},
		"gate": {NodeID: "gate", Status: "waiting_approval"},
	}
	if err := st.PutRun(rejectRun); err != nil {
		t.Fatal(err)
	}
	if err := e.Reject(rejectRun.ID, domain.RejectRequest{NodeID: "other"}); err == nil {
		t.Fatal("reject without target accepted")
	}
	if err := e.Reject(rejectRun.ID, domain.RejectRequest{NodeID: "gate", TargetID: "missing"}); err == nil {
		t.Fatal("missing reject target accepted")
	}
	if err := e.Reject(rejectRun.ID, domain.RejectRequest{NodeID: "gate", TargetID: "work", Reason: "redo", Context: "details"}); err != nil {
		t.Fatal(err)
	}
	rejected, _ := st.RunState(rejectRun.ID)
	if rejected.Nodes["work"].Status != "pending" || rejected.Nodes["work"].ReworkCount != 1 || rejected.Status != "running" {
		t.Fatalf("rejected=%+v", rejected)
	}

	cancelWaiting := waiting
	cancelWaiting.ID = "ops-cancel-waiting"
	if err := st.PutRun(cancelWaiting); err != nil {
		t.Fatal(err)
	}
	if err := e.Cancel(cancelWaiting.ID); err != nil {
		t.Fatal(err)
	}
	canceled, _ := st.RunState(cancelWaiting.ID)
	if canceled.Status != "cancelled" {
		t.Fatalf("waiting cancel=%+v", canceled)
	}

	retryNode := domain.WorkflowNode{ID: "retry", Type: "script", MaxAttempts: 1}
	retryRun := domain.Run{
		ID: "ops-retry-more", Status: "failed", WorkflowSnapshot: domain.WorkflowDefinition{ID: "retry-wf", Nodes: []domain.WorkflowNode{retryNode}},
		Nodes: map[string]*domain.NodeRun{"retry": {NodeID: "retry", Status: "failed", Attempt: 1, Error: "boom"}},
	}
	if err := st.PutRun(retryRun); err != nil {
		t.Fatal(err)
	}
	if err := e.Retry(retryRun.ID, "missing", "", ""); err == nil {
		t.Fatal("missing retry node accepted")
	}
	if err := e.Retry(retryRun.ID, retryNode.ID, "", "ignored for scripts"); err != nil {
		t.Fatal(err)
	}
	retried, _ := st.RunState(retryRun.ID)
	if retried.Status != "running" || retried.Nodes[retryNode.ID].Status != "pending" || retried.WorkflowSnapshot.Nodes[0].MaxAttempts != 2 {
		t.Fatalf("retried=%+v", retried)
	}

	pausedNode := domain.WorkflowNode{ID: "paused", Type: "agent"}
	paused := domain.Run{
		ID: "ops-resume-paused-more", Status: "waiting", PauseRequested: true,
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "paused-wf", Nodes: []domain.WorkflowNode{pausedNode}},
		Nodes:            map[string]*domain.NodeRun{"paused": {NodeID: "paused", Status: "waiting_approval", Activity: "已由用户暂停", SessionID: "session"}},
	}
	if err := st.PutRun(paused); err != nil {
		t.Fatal(err)
	}
	if err := e.ResumePaused(paused.ID, "continue with context"); err != nil {
		t.Fatal(err)
	}
	resumed, _ := st.RunState(paused.ID)
	if resumed.Status != "running" || resumed.Nodes["paused"].Status != "pending" || resumed.Nodes["paused"].ResumeSessionID != "session" {
		t.Fatalf("resumed=%+v", resumed)
	}

	decline := domain.Run{ID: "ops-decline-more", Status: "waiting", Nodes: map[string]*domain.NodeRun{"node": {NodeID: "node", Status: "waiting_approval"}}}
	if err := st.PutRun(decline); err != nil {
		t.Fatal(err)
	}
	if err := e.DeclineRetry(decline.ID, "missing"); err == nil {
		t.Fatal("missing decline node accepted")
	}
	if err := e.DeclineRetry(decline.ID, "node"); err != nil {
		t.Fatal(err)
	}
	declined, _ := st.RunState(decline.ID)
	if declined.Status != "failed" || declined.Nodes["node"].Status != "failed" {
		t.Fatalf("declined=%+v", declined)
	}

	issue := domain.Issue{ID: "ops-relation-issue", ProjectID: "platform", Title: "Issue", Status: "todo"}
	task := domain.Task{ID: "ops-relation-task", Status: "queued"}
	if err := st.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTask(task); err != nil {
		t.Fatal(err)
	}
	markRunRelationsRunning(st, domain.Run{ExecutionContext: domain.TelemetryContext{IssueID: issue.ID, TaskID: task.ID}})
	updatedIssue, _ := st.Issue(issue.ID)
	updatedTask, _ := st.Task(task.ID)
	if updatedIssue.Status != "in_progress" || updatedTask.Status != "running" {
		t.Fatalf("relations issue=%+v task=%+v", updatedIssue, updatedTask)
	}
	issue.Status = "done"
	if err := st.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	markRunRelationsRunning(st, domain.Run{ExecutionContext: domain.TelemetryContext{IssueID: issue.ID}})
	if unchanged, _ := st.Issue(issue.ID); unchanged.Status != "done" {
		t.Fatalf("closed relation changed=%+v", unchanged)
	}
}

func TestSOPHandoffArchiveValidationFallbacks(t *testing.T) {
	if _, err := readSOPHandoffTranscript(nil); err == nil {
		t.Fatal("empty transcript accepted")
	}
	if _, err := readSOPHandoffTranscript([]domain.Artifact{{}, {Content: "{"}}); err == nil {
		t.Fatal("invalid transcript accepted")
	}
	invalidManifest, _ := json.Marshal(sopHandoffTranscriptManifest{Format: "wrong", Parts: []sopHandoffTranscriptPart{{}, {}}})
	if _, err := readSOPHandoffTranscript([]domain.Artifact{{}, {Content: string(invalidManifest)}}); err == nil {
		t.Fatal("invalid manifest accepted")
	}
	if manifest, files := sopHandoffInputs(domain.Run{}); manifest != "" || files != nil {
		t.Fatalf("empty inputs=%q %v", manifest, files)
	}
	if got := sopHandoffInputPath(&domain.GroupSOPHandoff{ID: "handoff"}, "sop-handoff-conversation"); !strings.HasSuffix(got, ".json") {
		t.Fatalf("conversation input=%q", got)
	}
	if got := sopHandoffInputPath(&domain.GroupSOPHandoff{ID: "handoff"}, "sop-handoff-conversation-part-001"); !strings.HasSuffix(got, ".txt") {
		t.Fatalf("part input=%q", got)
	}
	if err := validateSOPHandoffArtifacts(domain.Run{}); err != nil {
		t.Fatal(err)
	}
	if manifest, err := materializeSOPHandoffInputs(t.TempDir(), domain.Run{}); err != nil || manifest != "" {
		t.Fatalf("empty materialization=%q err=%v", manifest, err)
	}
	ref := &domain.GroupSOPHandoff{ID: "handoff", SummaryArtifactID: "summary", SummaryPath: "handoff.md", TranscriptArtifactID: "transcript", TranscriptPath: "conversation.json"}
	missing := domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{GroupSOPHandoff: ref}}
	if _, err := archivedSOPHandoffArtifacts(missing); err == nil {
		t.Fatal("missing archive accepted")
	}
	if err := validateSOPHandoffArtifacts(missing); err == nil {
		t.Fatal("missing archive validation succeeded")
	}
	child := domain.Run{ID: "child", ExecutionContext: domain.TelemetryContext{ProjectID: "other"}}
	missing.ExecutionContext.ProjectID = "project"
	if err := inheritSOPHandoff(missing, &child); err == nil {
		t.Fatal("cross-project inheritance accepted")
	}
	if err := validateSOPHandoffRunnerPayload(domain.Run{}, domain.RunnerTask{}); err != nil {
		t.Fatal(err)
	}
	hugeRun := domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{GroupSOPHandoff: ref}}
	if err := validateSOPHandoffRunnerPayload(hugeRun, domain.RunnerTask{Prompt: strings.Repeat("x", (8 << 20))}); err == nil {
		t.Fatal("oversized runner payload accepted")
	}
	artifacts, err := buildSOPHandoffArtifacts("run", "handoff", "summary", []byte(strings.Repeat("界", maxArtifactBytes/3+10)))
	if err != nil || len(artifacts) < 3 {
		t.Fatalf("multipart artifacts=%d err=%v", len(artifacts), err)
	}
	if _, err := readSOPHandoffTranscript(artifacts); err != nil {
		t.Fatal(err)
	}
	artifacts[len(artifacts)-1].Content += "tamper"
	if _, err := readSOPHandoffTranscript(artifacts); err == nil {
		t.Fatal("tampered multipart transcript accepted")
	}
}
