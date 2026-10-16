package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestSOPNodeRunsChildAndHandsOffArtifacts(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	child := domain.WorkflowDefinition{
		ID: "child-prd", Name: "Child PRD", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{
			ID: "write", Name: "Write", Type: "script", ScriptLanguage: "shell",
			Script: "printf '# PRD\\nready\\n' > prd.md", MaxAttempts: 1,
			ArtifactOutputs: []domain.ArtifactContract{{Name: "prd", Path: "prd.md", Required: true}},
		}},
	}
	parent := domain.WorkflowDefinition{
		ID: "req-flow", Name: "Requirement flow", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{
				ID: "prd-sop", Name: "产品 SOP", Type: "sop", SOPWorkflowID: child.ID, MaxAttempts: 1,
				ArtifactOutputs: []domain.ArtifactContract{{Name: "prd", Path: "prd.md", Required: true}},
			},
			{
				ID: "use", Name: "Use", Type: "script", ScriptLanguage: "shell",
				Script: "test -f .agentflow/inputs/prd-sop/prd.md", MaxAttempts: 1,
			},
		},
		Edges: []domain.WorkflowEdge{{ID: "e1", Source: "prd-sop", Target: "use", When: "success"}},
	}
	if err := s.PutWorkflow(child); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkflow(parent); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 2))
	run, err := e.Start(parent.ID, "做徽章")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "succeeded")
	finished, _ := s.Run(run.ID)
	if finished.Nodes["prd-sop"].Status != "succeeded" || strings.TrimSpace(finished.Nodes["prd-sop"].ChildRunID) == "" {
		t.Fatalf("sop node=%#v", finished.Nodes["prd-sop"])
	}
	if finished.Nodes["use"].Status != "succeeded" {
		t.Fatalf("downstream node=%#v", finished.Nodes["use"])
	}
	found := false
	for _, art := range finished.Artifacts {
		if art.NodeID == "prd-sop" && art.Name == "prd" && strings.Contains(art.Content, "ready") {
			found = true
		}
	}
	if !found {
		t.Fatalf("parent did not inherit child artifact: %+v", finished.Artifacts)
	}
}

// 子 SOP 成功不能把整条需求标成已交付；父流程失败时需求仍要能重试。
func TestSOPChildSuccessDoesNotDeliverParentIssue(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	issue := domain.Issue{ID: "issue-sop-child", ProjectID: "p", Title: "t", Status: "todo", Kind: "requirement"}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	child := domain.WorkflowDefinition{
		ID: "child-ok", Name: "Child", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "ok", Name: "OK", Type: "script", ScriptLanguage: "shell", Script: "true", MaxAttempts: 1}},
	}
	parent := domain.WorkflowDefinition{
		ID: "parent-fails", Name: "Parent", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "sub", Name: "Sub", Type: "sop", SOPWorkflowID: child.ID, MaxAttempts: 1},
			{ID: "boom", Name: "Boom", Type: "script", ScriptLanguage: "shell", Script: "exit 1", MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{{ID: "e1", Source: "sub", Target: "boom", When: "success"}},
	}
	for _, wf := range []domain.WorkflowDefinition{child, parent} {
		if err := s.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
	}
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 2))
	run, err := e.StartWithRepositoriesContext(parent.ID, "x", nil, domain.TelemetryContext{IssueID: issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "failed")
	finished, _ := s.Run(run.ID)
	childRun, ok := s.Run(finished.Nodes["sub"].ChildRunID)
	if !ok || childRun.Status != "succeeded" {
		t.Fatalf("child run = %+v", childRun.Status)
	}
	got, _ := s.Issue(issue.ID)
	if got.DeliveryState == "merged" || got.Status == "done" {
		t.Fatalf("child success delivered the issue: status=%s delivery=%s", got.Status, got.DeliveryState)
	}
}

// 发布（Drain）打断父流程时子 SOP 不能被取消；新 worker 接着等原来的子 SOP，而不是重跑一遍。
func TestSOPNodeReattachesChildAfterDrain(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	marker := filepath.Join(t.TempDir(), "go")
	child := domain.WorkflowDefinition{
		ID: "slow-child", Name: "Slow child", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "wait", Name: "Wait", Type: "command", Command: "while [ ! -e " + marker + " ]; do sleep 0.05; done", MaxAttempts: 3}},
	}
	parent := domain.WorkflowDefinition{
		ID: "parent-drain", Name: "Parent", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "sub", Name: "Sub", Type: "sop", SOPWorkflowID: child.ID, MaxAttempts: 3}},
	}
	for _, wf := range []domain.WorkflowDefinition{child, parent} {
		if err := s.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	a := New(s, runtime.NewManagerWithLimit(root, 2))
	a.ConfigureExecution("worker-a", "shared-host", 2*time.Second, true)
	run, err := a.Start(parent.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var childID string
	waitRun(t, s, run.ID, 3*time.Second, func(got domain.Run) bool {
		if nr := got.Nodes["sub"]; nr != nil && nr.ChildRunID != "" {
			childID = nr.ChildRunID
			c, _ := s.RunState(childID)
			return c.Nodes["wait"] != nil && c.Nodes["wait"].Status == "running"
		}
		return false
	})
	drainCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a.Drain(drainCtx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if c, _ := s.RunState(childID); c.Status == "cancelled" || c.CancelRequested {
		t.Fatalf("drain cancelled the child SOP: %s", c.Status)
	}

	b := New(s, runtime.NewManagerWithLimit(root, 2))
	b.ConfigureExecution("worker-b", "shared-host", 2*time.Second, true)
	if claimed := b.ResumeInterruptedRuns(); len(claimed) != 2 {
		t.Fatalf("claimed %v", claimed)
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got := waitRun(t, s, run.ID, 5*time.Second, func(got domain.Run) bool { return got.Status == "succeeded" })
	if got.Nodes["sub"].ChildRunID != childID {
		t.Fatalf("parent started a new child %s instead of %s", got.Nodes["sub"].ChildRunID, childID)
	}
	children := 0
	for _, r := range s.Runs() {
		if r.ExecutionContext.ParentRunID == run.ID {
			children++
		}
	}
	if children != 1 {
		t.Fatalf("child runs = %d, want 1", children)
	}
	page, err := s.QueryRunTelemetry(run.ID, "sub", "", 0, 0, false, false, 200)
	if err != nil {
		t.Fatal(err)
	}
	reattached := false
	for _, event := range page.Events {
		reattached = reattached || event.Name == "sop.child.reattached"
	}
	if !reattached {
		t.Fatalf("reattaching the child must be visible in the parent trace: %+v", page.Events)
	}
}

func childRunsOf(s *repo.Store, parentID string) int {
	n := 0
	for _, r := range s.Runs() {
		if r.ExecutionContext.ParentRunID == parentID {
			n++
		}
	}
	return n
}

// 暂停父流程要连带暂停子 SOP；子 SOP 在父流程暂停期间跑完，父流程继续时直接用它的结果。
func TestSOPNodePauseCascadesAndReusesFinishedChild(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	marker := filepath.Join(t.TempDir(), "go")
	child := domain.WorkflowDefinition{
		ID: "pause-child", Name: "Child", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "wait", Name: "Wait", Type: "command", Command: "while [ ! -e " + marker + " ]; do sleep 0.05; done", MaxAttempts: 1}},
	}
	parent := domain.WorkflowDefinition{
		ID: "pause-parent", Name: "Parent", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "sub", Name: "Sub", Type: "sop", SOPWorkflowID: child.ID, MaxAttempts: 1}},
	}
	for _, wf := range []domain.WorkflowDefinition{child, parent} {
		if err := s.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
	}
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 2))
	run, err := e.Start(parent.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var childID string
	waitRun(t, s, run.ID, 3*time.Second, func(got domain.Run) bool {
		if nr := got.Nodes["sub"]; nr != nil && nr.ChildRunID != "" {
			childID = nr.ChildRunID
			c, _ := s.RunState(childID)
			return c.Nodes["wait"] != nil && c.Nodes["wait"].Status == "running"
		}
		return false
	})
	if err := e.Pause(run.ID); err != nil {
		t.Fatal(err)
	}
	waitRun(t, s, run.ID, 3*time.Second, func(got domain.Run) bool { return got.Status == "waiting" && got.PauseRequested })
	waitRun(t, s, childID, 3*time.Second, func(got domain.Run) bool { return got.Status == "waiting" && got.PauseRequested })

	// The operator continues only the child; it finishes while the parent is still paused.
	if err := e.ResumePaused(childID, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitRun(t, s, childID, 5*time.Second, func(got domain.Run) bool { return got.Status == "succeeded" })
	if got, _ := s.RunState(run.ID); got.Status != "waiting" {
		t.Fatalf("parent status = %s, want still paused", got.Status)
	}
	if err := e.ResumePaused(run.ID, ""); err != nil {
		t.Fatal(err)
	}
	got := waitRun(t, s, run.ID, 5*time.Second, func(got domain.Run) bool { return got.Status == "succeeded" })
	if got.Nodes["sub"].ChildRunID != childID || childRunsOf(s, run.ID) != 1 {
		t.Fatalf("parent restarted the finished child: node child=%s runs=%d", got.Nodes["sub"].ChildRunID, childRunsOf(s, run.ID))
	}
}

// 暂停期间 SOP 节点改指向了另一条 SOP 并刷新：继续时不能复用旧 SOP 的子运行结果。
func TestSOPNodeRepointedWhilePausedStartsNewChild(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	marker := filepath.Join(t.TempDir(), "go")
	oldChild := domain.WorkflowDefinition{
		ID: "old-child", Name: "Old", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "wait", Name: "Wait", Type: "command", Command: "while [ ! -e " + marker + " ]; do sleep 0.05; done", MaxAttempts: 1}},
	}
	newChild := domain.WorkflowDefinition{
		ID: "new-child", Name: "New", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "ok", Name: "OK", Type: "command", Command: "true", MaxAttempts: 1}},
	}
	parent := domain.WorkflowDefinition{
		ID: "repoint-parent", Name: "Parent", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "sub", Name: "Sub", Type: "sop", SOPWorkflowID: oldChild.ID, MaxAttempts: 1}},
	}
	for _, wf := range []domain.WorkflowDefinition{oldChild, newChild, parent} {
		if err := s.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
	}
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 2))
	run, err := e.Start(parent.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var oldID string
	waitRun(t, s, run.ID, 3*time.Second, func(got domain.Run) bool {
		if nr := got.Nodes["sub"]; nr != nil && nr.ChildRunID != "" {
			oldID = nr.ChildRunID
			c, _ := s.RunState(oldID)
			return c.Nodes["wait"] != nil && c.Nodes["wait"].Status == "running"
		}
		return false
	})
	if err := e.Pause(run.ID); err != nil {
		t.Fatal(err)
	}
	waitRun(t, s, oldID, 3*time.Second, func(got domain.Run) bool { return got.Status == "waiting" && got.PauseRequested })
	waitRun(t, s, run.ID, 3*time.Second, func(got domain.Run) bool { return got.Status == "waiting" })
	if err := e.ResumePaused(oldID, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitRun(t, s, oldID, 5*time.Second, func(got domain.Run) bool { return got.Status == "succeeded" })

	wf, _ := s.Workflow(parent.ID)
	wf.Nodes[0].SOPWorkflowID = newChild.ID
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	if err := e.RefreshRunSOP(run.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.ResumePaused(run.ID, ""); err != nil {
		t.Fatal(err)
	}
	got := waitRun(t, s, run.ID, 5*time.Second, func(got domain.Run) bool { return isTerminalRunStatus(got.Status) })
	if got.Status != "succeeded" {
		t.Fatalf("parent = %s", got.Status)
	}
	child, _ := s.RunState(got.Nodes["sub"].ChildRunID)
	if child.ID == oldID || child.WorkflowID != newChild.ID {
		t.Fatalf("parent reused the old SOP's child %s (workflow %s)", child.ID, child.WorkflowID)
	}
}

// 恢复父流程时，被连带暂停的子 SOP 也要一起继续。
func TestSOPNodeResumeParentResumesPausedChild(t *testing.T) {
	pauseParentThen(t, false)
}

// 父流程暂停后直接取消，被连带暂停的子 SOP 也要取消。
func TestSOPNodeCancelPausedParentCancelsChild(t *testing.T) {
	pauseParentThen(t, true)
}

func pauseParentThen(t *testing.T, cancelInstead bool) {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	marker := filepath.Join(t.TempDir(), "go")
	child := domain.WorkflowDefinition{
		ID: "resume-child", Name: "Child", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "wait", Name: "Wait", Type: "command", Command: "while [ ! -e " + marker + " ]; do sleep 0.05; done", MaxAttempts: 1}},
	}
	parent := domain.WorkflowDefinition{
		ID: "resume-parent", Name: "Parent", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "sub", Name: "Sub", Type: "sop", SOPWorkflowID: child.ID, MaxAttempts: 1}},
	}
	for _, wf := range []domain.WorkflowDefinition{child, parent} {
		if err := s.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
	}
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 2))
	run, err := e.Start(parent.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var childID string
	waitRun(t, s, run.ID, 3*time.Second, func(got domain.Run) bool {
		if nr := got.Nodes["sub"]; nr != nil && nr.ChildRunID != "" {
			childID = nr.ChildRunID
			c, _ := s.RunState(childID)
			return c.Nodes["wait"] != nil && c.Nodes["wait"].Status == "running"
		}
		return false
	})
	if err := e.Pause(run.ID); err != nil {
		t.Fatal(err)
	}
	waitRun(t, s, childID, 3*time.Second, func(got domain.Run) bool { return got.Status == "waiting" && got.PauseRequested })
	waitRun(t, s, run.ID, 3*time.Second, func(got domain.Run) bool { return got.Status == "waiting" })
	if cancelInstead {
		if err := e.Cancel(run.ID); err != nil {
			t.Fatal(err)
		}
		waitRun(t, s, childID, 3*time.Second, func(got domain.Run) bool { return got.Status == "cancelled" })
		return
	}
	if err := e.ResumePaused(run.ID, ""); err != nil {
		t.Fatal(err)
	}
	waitRun(t, s, childID, 3*time.Second, func(got domain.Run) bool { return got.Status == "running" })
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got := waitRun(t, s, run.ID, 5*time.Second, func(got domain.Run) bool { return got.Status == "succeeded" })
	if got.Nodes["sub"].ChildRunID != childID || childRunsOf(s, run.ID) != 1 {
		t.Fatalf("child=%s runs=%d", got.Nodes["sub"].ChildRunID, childRunsOf(s, run.ID))
	}
}

// 子 SOP 失败导致父流程失败；在子 SOP 上重跑后，父流程要接上这次重跑继续，而不是停在失败或另起一个子 SOP。
func TestSOPChildRetryResumesFailedParent(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	marker := filepath.Join(t.TempDir(), "ok")
	child := domain.WorkflowDefinition{
		ID: "flaky-child", Name: "Child", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "check", Name: "Check", Type: "command", Command: "test -e " + marker, MaxAttempts: 1}},
	}
	parent := domain.WorkflowDefinition{
		ID: "flaky-parent", Name: "Parent", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "sub", Name: "Sub", Type: "sop", SOPWorkflowID: child.ID, MaxAttempts: 1},
			{ID: "after", Name: "After", Type: "command", Command: "true", MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{{ID: "e1", Source: "sub", Target: "after", When: "success"}},
	}
	for _, wf := range []domain.WorkflowDefinition{child, parent} {
		if err := s.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
	}
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 2))
	run, err := e.Start(parent.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	failed := waitRun(t, s, run.ID, 5*time.Second, func(got domain.Run) bool { return got.Status == "failed" })
	childID := failed.Nodes["sub"].ChildRunID
	if c, _ := s.RunState(childID); c.Status != "failed" {
		t.Fatalf("child status = %s", c.Status)
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.Retry(childID, "check", "", ""); err != nil {
		t.Fatal(err)
	}
	got := waitRun(t, s, run.ID, 5*time.Second, func(got domain.Run) bool { return got.Status == "succeeded" })
	if got.Nodes["sub"].ChildRunID != childID || childRunsOf(s, run.ID) != 1 {
		t.Fatalf("child=%s runs=%d", got.Nodes["sub"].ChildRunID, childRunsOf(s, run.ID))
	}
	if got.Nodes["after"].Status != "succeeded" {
		t.Fatalf("downstream = %+v", got.Nodes["after"])
	}
}

// startPausedParentWithChild starts parent -> SOP child (waits for marker), pauses
// the parent and returns once both are paused.
func startPausedParentWithChild(t *testing.T, s *repo.Store, e *Engine, prefix string, before bool) (parentID, childID, marker string) {
	t.Helper()
	marker = filepath.Join(t.TempDir(), "go")
	child := domain.WorkflowDefinition{
		ID: prefix + "-child", Name: "Child", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "wait", Name: "Wait", Type: "command", Command: "while [ ! -e " + marker + " ]; do sleep 0.05; done", MaxAttempts: 1}},
	}
	parent := domain.WorkflowDefinition{
		ID: prefix + "-parent", Name: "Parent", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "sub", Name: "Sub", Type: "sop", SOPWorkflowID: child.ID, MaxAttempts: 1}},
	}
	if before {
		parent.Nodes = append([]domain.WorkflowNode{{ID: "first", Name: "First", Type: "command", Command: "true", MaxAttempts: 1}}, parent.Nodes...)
		parent.Edges = []domain.WorkflowEdge{{ID: "e1", Source: "first", Target: "sub", When: "success"}}
	}
	for _, wf := range []domain.WorkflowDefinition{child, parent} {
		if err := s.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
	}
	run, err := e.Start(parent.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, s, run.ID, 3*time.Second, func(got domain.Run) bool {
		if nr := got.Nodes["sub"]; nr != nil && nr.ChildRunID != "" {
			childID = nr.ChildRunID
			c, _ := s.RunState(childID)
			return c.Nodes["wait"] != nil && c.Nodes["wait"].Status == "running"
		}
		return false
	})
	if err := e.Pause(run.ID); err != nil {
		t.Fatal(err)
	}
	waitRun(t, s, childID, 3*time.Second, func(got domain.Run) bool { return got.Status == "waiting" && got.PauseRequested })
	waitRun(t, s, run.ID, 3*time.Second, func(got domain.Run) bool { return got.Status == "waiting" })
	return run.ID, childID, marker
}

// 父流程暂停时子 SOP 被单独取消：继续父流程应如实失败，而不是悄悄另起一个子 SOP。
func TestSOPNodeFollowsCancelledChildOnResume(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 2))
	parentID, childID, _ := startPausedParentWithChild(t, s, e, "cancelled", false)
	if err := e.Cancel(childID); err != nil {
		t.Fatal(err)
	}
	waitRun(t, s, childID, 3*time.Second, func(got domain.Run) bool { return got.Status == "cancelled" })
	if err := e.ResumePaused(parentID, ""); err != nil {
		t.Fatal(err)
	}
	got := waitRun(t, s, parentID, 5*time.Second, func(got domain.Run) bool { return isTerminalRunStatus(got.Status) })
	if got.Status != "failed" || childRunsOf(s, parentID) != 1 {
		t.Fatalf("parent=%s children=%d, want failed with the one cancelled child", got.Status, childRunsOf(s, parentID))
	}
}

// 从更早的节点重跑暂停中的父流程：旧的子 SOP 不再被引用，必须被取消，而不是留着占用工作区。
func TestSOPRetryEarlierNodeCancelsLiveChild(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 2))
	parentID, oldChild, marker := startPausedParentWithChild(t, s, e, "orphan", true)
	if err := e.Retry(parentID, "first", "", ""); err != nil {
		t.Fatal(err)
	}
	waitRun(t, s, oldChild, 3*time.Second, func(got domain.Run) bool { return got.Status == "cancelled" })
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got := waitRun(t, s, parentID, 5*time.Second, func(got domain.Run) bool { return isTerminalRunStatus(got.Status) })
	if got.Status != "succeeded" || got.Nodes["sub"].ChildRunID == oldChild {
		t.Fatalf("parent=%s child=%s", got.Status, got.Nodes["sub"].ChildRunID)
	}
}

// 子 SOP 失败导致父流程失败；在子 SOP 上「手动通过」失败节点后，父流程也要接着跑。
func TestSOPChildPassResumesFailedParent(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	child := domain.WorkflowDefinition{
		ID: "pass-child", Name: "Child", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "check", Name: "Check", Type: "command", Command: "false", MaxAttempts: 1}},
	}
	parent := domain.WorkflowDefinition{
		ID: "pass-parent", Name: "Parent", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "sub", Name: "Sub", Type: "sop", SOPWorkflowID: child.ID, MaxAttempts: 1},
			{ID: "after", Name: "After", Type: "command", Command: "true", MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{{ID: "e1", Source: "sub", Target: "after", When: "success"}},
	}
	for _, wf := range []domain.WorkflowDefinition{child, parent} {
		if err := s.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
	}
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 2))
	run, err := e.Start(parent.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	failed := waitRun(t, s, run.ID, 5*time.Second, func(got domain.Run) bool { return got.Status == "failed" })
	childID := failed.Nodes["sub"].ChildRunID
	waitRun(t, s, childID, 3*time.Second, func(got domain.Run) bool { return got.Status == "failed" })
	if err := e.Pass(childID, "check", ""); err != nil {
		t.Fatal(err)
	}
	got := waitRun(t, s, run.ID, 5*time.Second, func(got domain.Run) bool { return got.Status == "succeeded" })
	if got.Nodes["sub"].ChildRunID != childID || childRunsOf(s, run.ID) != 1 || got.Nodes["after"].Status != "succeeded" {
		t.Fatalf("child=%s runs=%d after=%s", got.Nodes["sub"].ChildRunID, childRunsOf(s, run.ID), got.Nodes["after"].Status)
	}
}

func TestSOPNodeRejectsSelfReference(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wf := domain.WorkflowDefinition{
		ID: "loop", Name: "Loop", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "self", Name: "Self", Type: "sop", SOPWorkflowID: "loop", MaxAttempts: 1}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 1))
	if _, err := e.Start(wf.ID, ""); err == nil || !strings.Contains(err.Error(), "own workflow") {
		t.Fatalf("want self-reference error, got %v", err)
	}
}

// 子 SOP 的节点被打回重做过，父流程要拿最新一版产物，而不是被否决的第一版。
func TestImportChildArtifactsTakesNewestAttempt(t *testing.T) {
	t0 := time.Now().UTC()
	child := domain.Run{Artifacts: []domain.Artifact{
		{Name: "plan", Content: "v2", Attempt: 2, CreatedAt: t0.Add(time.Minute)},
		{Name: "plan", Content: "v1", Attempt: 1, CreatedAt: t0},
		{Name: "repository-patch", Content: "diff", CreatedAt: t0},
	}}
	got, err := importChildArtifacts("parent", domain.WorkflowNode{ID: "sub"}, 1, child)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "v2" {
		t.Fatalf("imported %+v, want only the newest plan", got)
	}
}
