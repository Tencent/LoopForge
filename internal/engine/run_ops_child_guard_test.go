package engine

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// 并行分支还在跑时，等待人工决定的节点也能「补充信息重跑」，只要重跑范围不碰正在跑的分支。
func TestRetryWaitingGateBesideActiveBranch(t *testing.T) {
	e, _ := cvEEngine(t)
	e.executeRuns = false
	wf := domain.WorkflowDefinition{
		ID: "gate-beside-branch", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "start", Type: "agent", AgentID: "developer"},
			{ID: "knot", Type: "agent", AgentID: "developer"},
			{ID: "e2e", Type: "agent", AgentID: "developer"},
			{ID: "done", Type: "agent", AgentID: "developer"},
		},
		Edges: []domain.WorkflowEdge{
			{Source: "start", Target: "knot"}, {Source: "start", Target: "e2e"},
			{Source: "knot", Target: "done"}, {Source: "e2e", Target: "done"},
		},
	}
	run := domain.Run{ID: "gate-beside", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "running", Nodes: map[string]*domain.NodeRun{
		"start": {NodeID: "start", Status: "succeeded", Attempt: 1},
		"knot":  {NodeID: "knot", Status: "waiting_approval", Attempt: 1},
		"e2e":   {NodeID: "e2e", Status: "running", Attempt: 1},
		"done":  {NodeID: "done", Status: "pending"},
	}}
	cvEPutRun(t, e, run)
	if err := e.Retry(run.ID, "knot", "补充信息", "已开启开关"); err != nil {
		t.Fatalf("retry waiting gate beside a running branch: %v", err)
	}
	got, _ := e.repo.RunState(run.ID)
	if got.Nodes["knot"].Status != "pending" || got.Nodes["e2e"].Status != "running" || got.Status != "running" {
		t.Fatalf("after retry: run=%s knot=%s e2e=%s", got.Status, got.Nodes["knot"].Status, got.Nodes["e2e"].Status)
	}

	// Rerunning from the shared upstream would reset the running branch.
	blocked := run
	blocked.ID = "gate-resets-branch"
	blocked.Nodes = map[string]*domain.NodeRun{
		"start": {NodeID: "start", Status: "waiting_approval", Attempt: 1},
		"knot":  {NodeID: "knot", Status: "pending"},
		"e2e":   {NodeID: "e2e", Status: "running", Attempt: 1},
		"done":  {NodeID: "done", Status: "pending"},
	}
	cvEPutRun(t, e, blocked)
	if err := e.Retry(blocked.ID, "start", "", ""); err == nil || !strings.Contains(err.Error(), "still active") {
		t.Fatalf("retry resetting a running branch = %v, want still active", err)
	}
}

// 父流程已打回并另起子 SOP 后，旧的失败子运行不能再被重试或手动通过，否则同一子 SOP 会双跑。
func TestAbandonedChildRunCannotBeResumed(t *testing.T) {
	e, _ := cvEEngine(t)
	e.executeRuns = false
	childWF := domain.WorkflowDefinition{
		ID: "guard-child", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "check", Type: "script", ScriptLanguage: "shell", Script: "false"}},
	}
	parentWF := domain.WorkflowDefinition{
		ID: "guard-parent", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "sub", Type: "sop", SOPWorkflowID: childWF.ID}},
	}
	parent := domain.Run{ID: "guard-parent-run", WorkflowID: parentWF.ID, WorkflowSnapshot: parentWF, Status: "running", Nodes: map[string]*domain.NodeRun{
		"sub": {NodeID: "sub", Status: "running", ChildRunID: "guard-child-new"},
	}}
	cvEPutRun(t, e, parent)
	child := func(id string) domain.Run {
		return domain.Run{
			ID: id, WorkflowID: childWF.ID, WorkflowSnapshot: childWF, Status: "failed",
			ExecutionContext: domain.TelemetryContext{ParentRunID: parent.ID, ParentNodeID: "sub"},
			Nodes:            map[string]*domain.NodeRun{"check": {NodeID: "check", Status: "failed", Attempt: 1}},
		}
	}
	cvEPutRun(t, e, child("guard-child-old"))
	cvEPutRun(t, e, child("guard-child-new"))

	if err := e.Retry("guard-child-old", "check", "", ""); err == nil || !strings.Contains(err.Error(), "不再跟随") {
		t.Fatalf("retry abandoned child = %v", err)
	}
	if err := e.Pass("guard-child-old", "check", ""); err == nil || !strings.Contains(err.Error(), "不再跟随") {
		t.Fatalf("pass abandoned child = %v", err)
	}
	if err := e.Retry("guard-child-new", "check", "", ""); err != nil {
		t.Fatalf("retry followed child: %v", err)
	}
}

// 取消父流程时，节点已不再引用的存活子 SOP 也要一并取消。
func TestCancelParentCancelsUnreferencedLiveChild(t *testing.T) {
	e, _ := cvEEngine(t)
	e.executeRuns = false
	wf := domain.WorkflowDefinition{
		ID: "cancel-orphan", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "sub", Type: "sop", SOPWorkflowID: "cancel-orphan-child"}},
	}
	parent := domain.Run{ID: "cancel-orphan-parent", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "running", Nodes: map[string]*domain.NodeRun{
		"sub": {NodeID: "sub", Status: "pending"},
	}}
	cvEPutRun(t, e, parent)
	orphan := domain.Run{
		ID: "cancel-orphan-child-run", WorkflowID: "cancel-orphan-child", Status: "waiting",
		ExecutionContext: domain.TelemetryContext{ParentRunID: parent.ID, ParentNodeID: "sub"},
		Nodes:            map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "waiting_approval"}},
	}
	cvEPutRun(t, e, orphan)
	if ids := e.repo.LiveChildRunIDs(parent.ID); len(ids) != 1 || ids[0] != orphan.ID {
		t.Fatalf("LiveChildRunIDs = %v", ids)
	}
	if err := e.Cancel(parent.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.repo.RunState(orphan.ID)
	if !got.CancelRequested && got.Status != "cancelled" {
		t.Fatalf("orphan child after parent cancel: status=%s cancelRequested=%v", got.Status, got.CancelRequested)
	}
}
