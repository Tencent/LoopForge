package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/runtime"
	"strings"
	"testing"
	"time"
)

func TestGroupDispatchDurableScopedAndIdempotent(t *testing.T) {
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("test", "test", time.Minute, false)
	for _, a := range []domain.AgentDefinition{{ID: "dev", Name: "开发", ProjectID: "p", ExecutorID: "mock"}, {ID: "review", Name: "评审", ProjectID: "p", ExecutorID: "mock"}, {ID: "foreign", Name: "foreign", ProjectID: "other"}} {
		if err := s.PutAgent(a); err != nil {
			t.Fatal(err)
		}
	}
	r := domain.Run{ID: "group", Status: "waiting", Generation: 1, WorkflowSnapshot: domain.WorkflowDefinition{CollaborationMode: "group", GroupMembers: []string{"dev", "review"}}, ExecutionContext: domain.TelemetryContext{ProjectID: "p"}, Nodes: map[string]*domain.NodeRun{}}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	req := GroupTaskRequest{AgentID: "dev", Prompt: "实现功能", RequestID: "one", Files: []string{"src/main.go"}}
	n, err := e.DispatchGroupTask(r.ID, "", req)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := e.DispatchGroupTask(r.ID, "", req)
	if err != nil || duplicate.ID != n.ID {
		t.Fatalf("duplicate: %+v %v", duplicate, err)
	}
	req.Prompt = "不同任务"
	if _, err = e.DispatchGroupTask(r.ID, "", req); err == nil {
		t.Fatal("request ID collision accepted")
	}
	if _, err = e.DispatchGroupTask(r.ID, "", GroupTaskRequest{AgentID: "foreign", Prompt: "x", RequestID: "foreign"}); err == nil {
		t.Fatal("foreign role accepted")
	}
	if _, err = e.DispatchGroupTask(r.ID, "", GroupTaskRequest{AgentID: "dev", Prompt: "x", RequestID: "escape", Files: []string{"../x"}}); err == nil {
		t.Fatal("escaping path accepted")
	}
	stored, _ := s.RunState(r.ID)
	stored.Nodes[n.ID].Status = "running"
	stored.Nodes[n.ID].Attempt = 1
	if err = s.PutRun(stored); err != nil {
		t.Fatal(err)
	}
	if err = e.PostGroupMessage(r.ID, n.ID, "我正在修改 src/main.go"); err != nil {
		t.Fatal(err)
	}
	if _, err = e.DispatchGroupTask(r.ID, n.ID, GroupTaskRequest{AgentID: "dev", Prompt: "loop", RequestID: "loop"}); err == nil {
		t.Fatal("delegation loop accepted")
	}
	child, err := e.DispatchGroupTask(r.ID, n.ID, GroupTaskRequest{AgentID: "review", Prompt: "评审变更", RequestID: "review"})
	if err != nil || child.RequestedBy != n.ID {
		t.Fatalf("child: %+v %v", child, err)
	}
	stored, _ = s.RunState(r.ID)
	if len(stored.WorkflowSnapshot.Nodes) != 2 || len(stored.Events) != 3 {
		t.Fatalf("not persisted: %+v", stored)
	}
	t.Setenv("AGENTFLOW_SESSION_SECRET", "test-group-signing-secret")
	e.ConfigurePlatformMCP("http://localhost", "")
	m := e.groupMCPServer(stored, n, 1)
	token := strings.TrimPrefix(m.Headers["Authorization"], "Bearer ")
	if who, err := e.ValidateGroupToken(r.ID, token); err != nil || who != n.ID {
		t.Fatalf("token: %q %v", who, err)
	}
	if _, err = e.ValidateGroupToken("other", token); err == nil {
		t.Fatal("cross-run token accepted")
	}
	stored.Nodes[n.ID].Status = "succeeded"
	if err = s.PutRun(stored); err != nil {
		t.Fatal(err)
	}
	if _, err = e.ValidateGroupToken(r.ID, token); err == nil {
		t.Fatal("finished invocation token accepted")
	}
	if _, _, done := settleStatusForCompletedGraph(stored.WorkflowSnapshot, stored); done {
		t.Fatal("group auto-finished")
	}
}

func TestNativeTaskGroupLifecyclePreservesRolesAndOutputs(t *testing.T) {
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("test", "test", time.Minute, false)
	if err := s.PutProject(domain.Project{ID: "native-group", Name: "自由协作"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgent(domain.AgentDefinition{ID: "native-dev", ProjectID: "native-group", Name: "开发", ExecutorID: "mock"}); err != nil {
		t.Fatal(err)
	}
	before := len(s.Workflows())
	r, err := e.StartTaskGroup("", "跨仓库协作", []domain.RepositoryContext{{RepositoryID: "a", CloneURL: "https://example.invalid/a.git"}, {RepositoryID: "b", CloneURL: "https://example.invalid/b.git"}}, domain.TelemetryContext{ProjectID: "native-group", ResponsibleUserID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Workflows()) != before || r.Status != "waiting" || r.WorkflowSnapshot.CollaborationMode != "group" || len(r.WorkflowSnapshot.Nodes) != 0 || len(r.WorkflowSnapshot.GroupMembers) != 0 || len(r.Repositories) != 2 {
		t.Fatalf("native group: %+v", r)
	}
	if err := e.InviteGroupMember(r.ID, "native-dev"); err != nil {
		t.Fatal(err)
	}
	n, err := e.DispatchGroupTask(r.ID, "", GroupTaskRequest{AgentID: "native-dev", Prompt: "实现功能", RequestID: "native-task"})
	if err != nil {
		t.Fatal(err)
	}
	r, _ = s.RunState(r.ID)
	r.Status = "waiting"
	r.Nodes[n.ID].Status, r.Nodes[n.ID].Output, r.Nodes[n.ID].Attempt = "succeeded", "实现与验证已完成", 1
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteGroupSOP(r.ID); err != nil {
		t.Fatal(err)
	}
	r, _ = s.RunState(r.ID)
	if r.Status != "succeeded" || r.WorkflowSnapshot.CollaborationMode != "group" || r.Nodes[n.ID].Output != "实现与验证已完成" || !r.WorkflowSnapshot.HasGroupMember("native-dev") {
		t.Fatalf("completion lost history: %+v", r)
	}
	if err := e.EnableTaskGroup(r.ID); err != nil {
		t.Fatal(err)
	}
	r, _ = s.RunState(r.ID)
	if r.Status != "waiting" || r.Nodes[n.ID].Output != "实现与验证已完成" {
		t.Fatalf("reopen lost history: %+v", r)
	}
	if _, err := e.DispatchGroupTask(r.ID, "", GroupTaskRequest{AgentID: "native-dev", Prompt: "继续改进", RequestID: "native-next"}); err != nil {
		t.Fatal(err)
	}
}

// Both scripts wait for the other's write: serialization cannot pass this test.
func TestGroupWorkersOverlapAndAcceptNewWorkAfterIdle(t *testing.T) {
	e, s := newRecoveryEngine(t)
	e.sandboxes = runtime.NewManagerWithLimit(t.TempDir(), 4)
	wf := domain.WorkflowDefinition{ID: "group-template", Name: "group", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "base", Type: "script", Script: "true", MaxAttempts: 1}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	r, err := e.StartTaskGroup(wf.ID, "并行测试", nil, domain.TelemetryContext{ProjectID: "p"})
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"a", "b"}, {"b", "a"}} {
		n := domain.WorkflowNode{ID: pair[0], Name: pair[0], Type: "script", MaxAttempts: 1, RequestID: pair[0], Script: "touch " + pair[0] + "; i=0; while [ ! -f " + pair[1] + " ]; do i=$((i+1)); [ $i -lt 100 ] || exit 1; sleep 0.05; done"}
		r.WorkflowSnapshot.Nodes = append(r.WorkflowSnapshot.Nodes, n)
		r.Nodes[n.ID] = &domain.NodeRun{NodeID: n.ID, Status: "pending"}
	}
	r.Status = "running"
	if err = s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if _, err = e.acquireLease(r); err != nil {
		t.Fatal(err)
	}
	e.resume(r.ID)
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := s.RunState(r.ID)
		if got.Status == "waiting" && got.Nodes["a"].Status == "succeeded" && got.Nodes["b"].Status == "succeeded" {
			r = got
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if r.Status != "waiting" || r.Nodes["a"].Status != "succeeded" || r.Nodes["b"].Status != "succeeded" {
		got, _ := s.RunState(r.ID)
		t.Fatalf("workers did not overlap: a=%+v b=%+v status=%s", got.Nodes["a"], got.Nodes["b"], got.Status)
	}
	for e.isRunSchedulerActive(r.ID) {
		time.Sleep(20 * time.Millisecond)
	}
	n := domain.WorkflowNode{ID: "later", Type: "script", Script: "test -f a && test -f b", MaxAttempts: 1, RequestID: "later"}
	r.WorkflowSnapshot.Nodes = append(r.WorkflowSnapshot.Nodes, n)
	r.Nodes[n.ID] = &domain.NodeRun{NodeID: n.ID, Status: "pending"}
	r.Status = "running"
	if err = s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if _, err = e.acquireLease(r); err != nil {
		t.Fatal(err)
	}
	e.resume(r.ID)
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := s.RunState(r.ID)
		if got.Status == "waiting" && got.Nodes[n.ID].Status == "succeeded" {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	got, _ := s.RunState(r.ID)
	t.Fatalf("idle group lost shared workspace: %+v", got.Nodes[n.ID])
}

func TestConvertCompletedAndFailedSOPKeepsHistory(t *testing.T) {
	for _, status := range []string{"succeeded", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			e, s := newRecoveryEngine(t)
			r := domain.Run{ID: "convert-" + status, Status: status, Input: "原任务", CancelRequested: status == "cancelled", Nodes: map[string]*domain.NodeRun{"old": {NodeID: "old", Status: status, Attempt: 2, Output: "原执行输出"}}, WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf", Nodes: []domain.WorkflowNode{{ID: "old", Type: "agent", AgentID: "dev"}}, Edges: []domain.WorkflowEdge{{Source: "old", Target: "next"}}}}
			if err := s.PutRun(r); err != nil {
				t.Fatal(err)
			}
			if err := e.EnableTaskGroup(r.ID); err != nil {
				t.Fatal(err)
			}
			got, _ := s.RunState(r.ID)
			if got.Status != "waiting" || got.CancelRequested || got.WorkflowSnapshot.CollaborationMode != "group" || len(got.WorkflowSnapshot.Edges) != 0 || got.Nodes["old"].Output != "原执行输出" || got.Nodes["old"].Attempt != 2 {
				t.Fatalf("conversion lost history: %+v", got)
			}
		})
	}
}

func TestGroupInvitationAndSOPRoundTrip(t *testing.T) {
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("test", "test", time.Minute, false)
	wf := domain.WorkflowDefinition{ID: "sop", Name: "SOP", Version: 1, Revision: 1, Nodes: []domain.WorkflowNode{{ID: "a", Type: "agent", AgentID: "dev", MaxAttempts: 1}, {ID: "b", Type: "agent", AgentID: "dev", MaxAttempts: 1}}, Edges: []domain.WorkflowEdge{{ID: "ab", Source: "a", Target: "b", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	for _, a := range []domain.AgentDefinition{{ID: "dev", Name: "开发", ProjectID: "p"}, {ID: "review", Name: "评审", ProjectID: "p"}, {ID: "foreign", Name: "foreign", ProjectID: "other"}} {
		if err := s.PutAgent(a); err != nil {
			t.Fatal(err)
		}
	}
	r := domain.Run{ID: "roundtrip", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "failed", Input: "改进", ExecutionContext: domain.TelemetryContext{ProjectID: "p"}, Nodes: map[string]*domain.NodeRun{"a": {NodeID: "a", Status: "succeeded", Attempt: 1, Output: "上游产物"}, "b": {NodeID: "b", Status: "failed", Attempt: 2}}}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if err := e.EnableTaskGroup(r.ID); err != nil {
		t.Fatal(err)
	}
	req := GroupTaskRequest{AgentID: "review", Prompt: "评审", RequestID: "review"}
	if _, err := e.DispatchGroupTask(r.ID, "", req); err == nil {
		t.Fatal("uninvited agent accepted")
	}
	if err := e.InviteGroupMember(r.ID, "foreign"); err == nil {
		t.Fatal("foreign invitation accepted")
	}
	if err := e.InviteGroupMember(r.ID, "review"); err != nil {
		t.Fatal(err)
	}
	if err := e.InviteGroupMember(r.ID, "review"); err != nil {
		t.Fatal(err)
	}
	node, err := e.DispatchGroupTask(r.ID, "", req)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.RestoreGroupSOP(r.ID, "b"); err == nil {
		t.Fatal("active task was not rejected")
	}
	current, _ := s.RunState(r.ID)
	current.Status = "waiting"
	current.Nodes[node.ID].Status = "failed"
	current.Nodes[node.ID].Output = "真实角色输出"
	if err := s.PutRun(current); err != nil {
		t.Fatal(err)
	}
	if err := e.RestoreGroupSOP(r.ID, "unknown"); err == nil {
		t.Fatal("unknown resume node accepted")
	}
	if err := e.RestoreGroupSOP(r.ID, "b"); err != nil {
		t.Fatal(err)
	}
	current, _ = s.RunState(r.ID)
	if current.WorkflowSnapshot.CollaborationMode != "" || !current.PauseRequested || current.Status != "waiting" || len(current.WorkflowSnapshot.Edges) != 1 || current.Nodes["a"].Output != "上游产物" || current.Nodes["b"].Attempt != 2 || current.Nodes["b"].Status != "pending" || current.Nodes[node.ID] != nil || len(current.WorkflowSnapshot.GroupHistory) != 1 {
		t.Fatalf("invalid restored state: %+v", current)
	}
	if err := e.EnableTaskGroup(r.ID); err != nil {
		t.Fatal(err)
	}
	current, _ = s.RunState(r.ID)
	if current.PauseRequested || current.Nodes[node.ID] == nil || current.Nodes[node.ID].Output != "真实角色输出" || !current.WorkflowSnapshot.HasGroupMember("review") || len(current.WorkflowSnapshot.GroupMemberIDs()) != 2 {
		t.Fatal("history, members or flags lost on second conversion")
	}
}

func TestGroupDispatchReportsMissingRuntimeConfiguration(t *testing.T) {
	e, _ := newRecoveryEngine(t)
	e.ConfigureExecution("group-config-test", "localhost", time.Minute, true)
	t.Setenv("AGENTFLOW_SESSION_SECRET", "")
	t.Setenv("AGENTFLOW_PLATFORM_MCP_TOKEN", "")
	req := GroupTaskRequest{AgentID: "dev", Prompt: "只读检查", RequestID: "retry-same-request"}
	e.ConfigurePlatformMCP("", "")
	if _, err := e.DispatchGroupTask("missing", "", req); err == nil || !strings.Contains(err.Error(), "AGENTFLOW_PLATFORM_MCP_URL") {
		t.Fatalf("missing platform address not identified: %v", err)
	}
	e.ConfigurePlatformMCP("http://localhost", "")
	if _, err := e.DispatchGroupTask("missing", "", req); err == nil || !strings.Contains(err.Error(), "AGENTFLOW_SESSION_SECRET") {
		t.Fatalf("missing signing secret not identified: %v", err)
	}
	t.Setenv("AGENTFLOW_SESSION_SECRET", "local-config-test-secret")
	if _, err := e.DispatchGroupTask("missing", "", req); err == nil || !strings.Contains(err.Error(), "当前任务不是自由协作") {
		t.Fatalf("configured request should reach normal run validation: %v", err)
	}
}

func TestRestoredSOPCanResumePendingCheckpoint(t *testing.T) {
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("test", "test", time.Minute, false)
	wf := domain.WorkflowDefinition{ID: "restore-resume", Nodes: []domain.WorkflowNode{{ID: "a", Type: "agent"}, {ID: "b", Type: "script"}, {ID: "c", Type: "agent"}}, Edges: []domain.WorkflowEdge{{Source: "a", Target: "b", When: "success"}, {Source: "b", Target: "c", When: "success"}}}
	r := domain.Run{ID: "restore-resume", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "failed", Nodes: map[string]*domain.NodeRun{"a": {NodeID: "a", Status: "succeeded", Output: "upstream"}, "b": {NodeID: "b", Status: "failed", Attempt: 1}, "c": {NodeID: "c", Status: "pending"}}}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if err := e.EnableTaskGroup(r.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.RestoreGroupSOP(r.ID, "b"); err != nil {
		t.Fatal(err)
	}
	if err := e.ResumePaused(r.ID, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := s.RunState(r.ID)
	if got.PauseRequested || got.Status != "running" || got.Nodes["a"].Output != "upstream" || got.Nodes["b"].Status != "pending" || got.Nodes["c"].Status != "pending" {
		t.Fatalf("invalid resumed checkpoint: %+v", got)
	}
	// A normal approval gate is never treated as a restored checkpoint.
	got.Status = "waiting"
	got.PauseRequested = true
	got.Nodes["b"].Status = "waiting_approval"
	got.Nodes["b"].Activity = "人工门禁"
	if err := s.PutRun(got); err != nil {
		t.Fatal(err)
	}
	if err := e.ResumePaused(r.ID, ""); err == nil {
		t.Fatal("ordinary approval was bypassed")
	}
}

func TestDeliveredGroupSOPRestartsIssueAndCanComplete(t *testing.T) {
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("test", "test", time.Minute, false)
	wf := domain.WorkflowDefinition{ID: "delivered-restore", Nodes: []domain.WorkflowNode{{ID: "a", Type: "script", MaxAttempts: 1}, {ID: "b", Type: "script", MaxAttempts: 1}}, Edges: []domain.WorkflowEdge{{Source: "a", Target: "b", When: "success"}}}
	issue := domain.Issue{ID: "delivered-restore", ProjectID: "p", Status: "done", DeliveryState: "merged", DeliveryMRURL: "https://example.test/team/repo/merge_requests/1", DeliveryMRState: "merged", LatestTaskID: "restore-task", AutomationState: "ignored"}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	r := domain.Run{ID: "delivered-restore", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "p", IssueID: issue.ID}, Nodes: map[string]*domain.NodeRun{"a": {NodeID: "a", Status: "succeeded", Attempt: 1, Output: "upstream artifact"}, "b": {NodeID: "b", Status: "succeeded", Attempt: 1, Output: "prior delivery"}}}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTask(domain.Task{ID: issue.LatestTaskID, IssueID: issue.ID, RunID: r.ID, Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := e.EnableTaskGroup(r.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.RestoreGroupSOP(r.ID, "missing"); err == nil {
		t.Fatal("invalid node accepted")
	}
	unchanged, _ := s.Issue(issue.ID)
	if !unchanged.DeliveryLocked() || unchanged.Status != "done" {
		t.Fatal("invalid restore changed issue")
	}
	if err := e.RestoreGroupSOP(r.ID, "b"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Issue(issue.ID)
	task, _ := s.Task(issue.LatestTaskID)
	if got.Status != "in_progress" || got.DeliveryState != "reopened" || got.ExecutionBlockedReason() != "" || got.DeliveryMRURL != issue.DeliveryMRURL || task.Status != "running" {
		t.Fatalf("issue/task were not reopened: %+v %+v", got, task)
	}
	if err := s.MarkIssueDelivered(issue.ID, issue.DeliveryMRURL, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Issue(issue.ID)
	if got.DeliveryLocked() {
		t.Fatal("old MR polling relocked reopened issue")
	}
	if err := e.ResumePaused(r.ID, ""); err != nil {
		t.Fatal(err)
	}
	restored, _ := s.RunState(r.ID)
	if restored.Nodes["a"].Output != "upstream artifact" || restored.Nodes["b"].Status != "pending" || restored.PauseRequested {
		t.Fatalf("bad resumed run: %+v", restored)
	}
	// Stop this isolated run before converting it back into a group.
	restored.Status = "waiting"
	if err := s.PutRun(restored); err != nil {
		t.Fatal(err)
	}
	if err := e.EnableTaskGroup(r.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteGroupSOP(r.ID); err != nil {
		t.Fatal(err)
	}
	completed, _ := s.RunState(r.ID)
	got, _ = s.Issue(issue.ID)
	task, _ = s.Task(issue.LatestTaskID)
	if completed.Status != "succeeded" || completed.PauseRequested || completed.WorkflowSnapshot.CollaborationMode != "" || completed.Nodes["a"].Output != "upstream artifact" || completed.Nodes["b"].Status != "skipped" || completed.Nodes["b"].Attempt != 1 || got.Status != "done" || !got.DeliveryLocked() || task.Status != "completed" {
		t.Fatalf("bad explicit completion: run=%+v issue=%+v task=%+v", completed, got, task)
	}
	if completed.Events[len(completed.Events)-1].Type != "group.sop_completed" {
		t.Fatal("explicit completion not audited")
	}
}

func TestGroupSOPCompletionPreservesGroupHistory(t *testing.T) {
	e, s := newRecoveryEngine(t)
	wf := domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "a", Type: "agent"}}}
	r := domain.Run{ID: "group-complete-history", Status: "waiting", WorkflowSnapshot: domain.WorkflowDefinition{CollaborationMode: "group", SOPCheckpoint: &domain.SOPCheckpoint{Workflow: wf, Nodes: map[string]*domain.NodeRun{"a": {NodeID: "a", Status: "failed", Attempt: 2, Output: "original failure"}}}, Nodes: []domain.WorkflowNode{{ID: "a"}, {ID: "group-task", RequestID: "request"}}}, Nodes: map[string]*domain.NodeRun{"a": {NodeID: "a", Status: "failed"}, "group-task": {NodeID: "group-task", Status: "running", Output: "group output"}}}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteGroupSOP(r.ID); err == nil {
		t.Fatal("active role completion accepted")
	}
	r.Nodes["group-task"].Status = "succeeded"
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteGroupSOP(r.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.RunState(r.ID)
	if len(got.WorkflowSnapshot.GroupHistory) != 1 || got.WorkflowSnapshot.GroupHistory[0].State.Output != "group output" || got.Nodes["a"].Output != "original failure" || got.Nodes["a"].Attempt != 2 || got.Nodes["a"].Status != "skipped" {
		t.Fatalf("history lost or execution fabricated: %+v", got)
	}
}
