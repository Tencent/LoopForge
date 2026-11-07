package engine

import (
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func cvEPutRun(t *testing.T, e *Engine, run domain.Run) {
	t.Helper()
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
}

func TestRunOpsApproveRejectRetryDecline(t *testing.T) {
	e, _ := cvEEngine(t)
	e.executeRuns = false
	wf := domain.WorkflowDefinition{
		ID: "cve-ops-wf", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "fix", Type: "agent", AgentID: "developer"},
			{ID: "gate", Type: "approval", RejectTo: "fix", MaxReworks: 2},
			{ID: "after", Type: "agent", AgentID: "developer"},
		},
		Edges: []domain.WorkflowEdge{{Source: "fix", Target: "gate"}, {Source: "gate", Target: "after"}},
	}
	base := domain.Run{WorkflowID: wf.ID, WorkflowSnapshot: wf, Nodes: map[string]*domain.NodeRun{
		"fix":   {NodeID: "fix", Status: "succeeded", Attempt: 1},
		"gate":  {NodeID: "gate", Status: "waiting_approval", Attempt: 1},
		"after": {NodeID: "after", Status: "pending"},
	}}

	approve := base
	approve.ID, approve.Status = "ops-approve", "waiting"
	cvEPutRun(t, e, approve)
	if err := e.Approve(approve.ID, "missing", ""); err == nil {
		t.Fatal("missing approval node accepted")
	}
	if err := e.Approve(approve.ID, "gate", "human context"); err != nil {
		t.Fatal(err)
	}
	got, _ := e.repo.RunState(approve.ID)
	if got.Status != "running" || got.Nodes["gate"].Status != "succeeded" || len(got.Artifacts) == 0 {
		t.Fatalf("approved = %+v", got)
	}

	reject := base
	reject.ID, reject.Status = "ops-reject", "waiting"
	reject.Nodes = map[string]*domain.NodeRun{
		"fix":   {NodeID: "fix", Status: "succeeded", Attempt: 1, Output: "old"},
		"gate":  {NodeID: "gate", Status: "waiting_approval", Attempt: 1},
		"after": {NodeID: "after", Status: "succeeded", Output: "stale"},
	}
	cvEPutRun(t, e, reject)
	if err := e.Reject(reject.ID, domain.RejectRequest{NodeID: "gate", TargetID: "missing"}); err == nil {
		t.Fatal("missing reject target accepted")
	}
	if err := e.Reject(reject.ID, domain.RejectRequest{NodeID: "gate", Reason: "redo", Context: "details"}); err != nil {
		t.Fatal(err)
	}
	got, _ = e.repo.RunState(reject.ID)
	if got.Status != "running" || got.Nodes["fix"].Status != "pending" || got.Nodes["after"].Status != "pending" || got.Nodes["fix"].ReworkCount != 1 {
		t.Fatalf("rejected = %+v", got)
	}

	limit := base
	limit.ID, limit.Status = "ops-limit", "waiting"
	limit.Nodes = map[string]*domain.NodeRun{
		"fix":   {NodeID: "fix", Status: "succeeded", ReworkCount: 2},
		"gate":  {NodeID: "gate", Status: "waiting_approval"},
		"after": {NodeID: "after", Status: "pending"},
	}
	cvEPutRun(t, e, limit)
	if err := e.Reject(limit.ID, domain.RejectRequest{NodeID: "gate"}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("rework limit = %v", err)
	}

	retry := base
	retry.ID, retry.Status = "ops-retry", "failed"
	retry.Nodes = map[string]*domain.NodeRun{
		"fix":   {NodeID: "fix", Status: "failed", Attempt: 3, Error: "boom"},
		"gate":  {NodeID: "gate", Status: "pending"},
		"after": {NodeID: "after", Status: "pending"},
	}
	cvEPutRun(t, e, retry)
	if err := e.Retry(retry.ID, "missing", "", ""); err == nil {
		t.Fatal("missing retry node accepted")
	}
	if err := e.Retry(retry.ID, "fix", "", "context ignored for agent?"); err != nil {
		t.Fatal(err)
	}
	got, _ = e.repo.RunState(retry.ID)
	if got.Status != "running" || got.Nodes["fix"].Status != "pending" || got.WorkflowSnapshot.Nodes[0].MaxAttempts != 4 {
		t.Fatalf("retried = %+v", got)
	}

	decline := base
	decline.ID, decline.Status = "ops-decline", "waiting"
	cvEPutRun(t, e, decline)
	if err := e.DeclineRetry(decline.ID, "missing"); err == nil {
		t.Fatal("missing decline node accepted")
	}
	if err := e.DeclineRetry(decline.ID, "gate"); err != nil {
		t.Fatal(err)
	}
	got, _ = e.repo.RunState(decline.ID)
	if got.Status != "failed" || got.Nodes["gate"].Status != "failed" || got.Nodes["gate"].Error == "" {
		t.Fatalf("declined = %+v", got)
	}
}

func TestRunOpsPassResumePausedRefreshAndHelpers(t *testing.T) {
	e, st := cvEEngine(t)
	e.executeRuns = false
	wf := domain.WorkflowDefinition{
		ID: "cve-pass-wf", Name: "v1", Version: 1, Revision: 1, SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "script", Type: "script", Script: "exit 1"}, {ID: "next", Type: "agent", AgentID: "developer"}},
		Edges: []domain.WorkflowEdge{{Source: "script", Target: "next"}},
	}
	failed := domain.Run{ID: "ops-pass", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "failed", Nodes: map[string]*domain.NodeRun{
		"script": {NodeID: "script", Status: "failed", Attempt: 1, Error: "boom"},
		"next":   {NodeID: "next", Status: "pending"},
	}}
	cvEPutRun(t, e, failed)
	if err := e.Pass(failed.ID, "next", ""); err == nil {
		t.Fatal("agent pass accepted")
	}
	if err := e.Pass(failed.ID, "script", "accepted risk"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.RunState(failed.ID)
	if got.Status != "running" || got.Nodes["script"].Status != "succeeded" {
		t.Fatalf("passed = %+v", got)
	}

	paused := domain.Run{ID: "ops-paused", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "running", Nodes: map[string]*domain.NodeRun{
		"script": {NodeID: "script", Status: "waiting_approval", Attempt: 1, Activity: "已由用户暂停，继续后脚本将从头执行"},
		"next":   {NodeID: "next", Status: "pending"},
	}}
	cvEPutRun(t, e, paused)
	if _, err := st.RequestRunPause(paused.ID); err != nil {
		t.Fatal(err)
	}
	current, _ := st.RunState(paused.ID)
	current.Status = "waiting"
	if err := st.PutRun(current); err != nil {
		t.Fatal(err)
	}
	if err := e.ResumePaused(paused.ID, "continue"); err != nil {
		t.Fatal(err)
	}
	got, _ = st.RunState(paused.ID)
	if got.Status != "running" || got.Nodes["script"].Status != "pending" || got.PauseRequested {
		t.Fatalf("resumed = %+v", got)
	}

	idle := domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "a", Type: "agent"}}}, Nodes: map[string]*domain.NodeRun{"a": {Status: "pending"}}, PauseRequested: true, Status: "waiting"}
	if resumed, err := resumeUnpausedGraph(&idle); err != nil || !resumed || idle.Status != "running" {
		t.Fatalf("resume unpaused = %+v %v %v", idle, resumed, err)
	}
	if resumed, err := resumeUnpausedGraph(nil); err == nil || resumed {
		t.Fatal("nil graph resumed")
	}
	waiting := idle
	waiting.Nodes = map[string]*domain.NodeRun{"a": {Status: "waiting_approval"}}
	if resumed, err := resumeUnpausedGraph(&waiting); err != nil || resumed {
		t.Fatalf("human gate resumed = %v %v", resumed, err)
	}
	if !isUserPausedNode(&domain.NodeRun{Status: "waiting_approval", Activity: "用户暂停"}) || isUserPausedNode(nil) {
		t.Fatal("paused-node helper")
	}
	children := childRunIDs(domain.Run{Nodes: map[string]*domain.NodeRun{"b": {ChildRunID: "two"}, "a": {ChildRunID: "one"}, "nil": nil}})
	if len(children) != 2 || children[0] != "one" {
		t.Fatalf("children = %v", children)
	}
	if resumeNodeID(domain.Run{WorkflowSnapshot: wf, Nodes: map[string]*domain.NodeRun{"script": {Status: "failed"}, "next": {Status: "pending"}}}) != "script" {
		t.Fatal("resume node")
	}

	latest := wf
	latest.Name, latest.Version, latest.Revision = "v2", 2, 2
	latest.Nodes = append(latest.Nodes, domain.WorkflowNode{ID: "added", Type: "approval"})
	if err := st.PutWorkflow(latest); err != nil {
		t.Fatal(err)
	}
	refresh := domain.Run{ID: "ops-refresh", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "failed", Nodes: map[string]*domain.NodeRun{
		"script": {NodeID: "script", Status: "succeeded"}, "next": {NodeID: "next", Status: "failed"},
	}}
	cvEPutRun(t, e, refresh)
	if err := e.RefreshRunSOP(refresh.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = st.RunState(refresh.ID)
	if got.WorkflowSnapshot.Version != 2 || got.Nodes["added"] == nil {
		t.Fatalf("refreshed = %+v", got.WorkflowSnapshot)
	}
	if err := e.RefreshRunSOP(refresh.ID); err != nil {
		t.Fatal(err)
	}

	active := refresh
	active.ID, active.Status, active.UpdatedAt = "ops-refresh-active", "running", time.Now()
	cvEPutRun(t, e, active)
	if err := e.RefreshRunSOP(active.ID); err == nil {
		t.Fatal("active refresh accepted")
	}
}
