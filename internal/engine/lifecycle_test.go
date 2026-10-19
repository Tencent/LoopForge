package engine

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestContinuingInterruptedAttempt(t *testing.T) {
	if continuingInterruptedAttempt(nil) {
		t.Fatal("nil node")
	}
	first := &domain.NodeRun{Status: "pending"}
	if continuingInterruptedAttempt(first) {
		t.Fatal("first attempt must increment")
	}
	retry := &domain.NodeRun{Attempt: 2, Error: "exit status 1", StartedAt: time.Now().UTC()}
	if continuingInterruptedAttempt(retry) {
		t.Fatal("failed retry must increment")
	}
	parked := &domain.NodeRun{Attempt: 4, StartedAt: time.Now().UTC(), Activity: "执行实例退出，等待恢复"}
	if !continuingInterruptedAttempt(parked) {
		t.Fatal("parked node must keep the same attempt")
	}
	rejected := &domain.NodeRun{Attempt: 1, StartedAt: time.Now().UTC(), Feedback: "needs changes"}
	if continuingInterruptedAttempt(rejected) {
		t.Fatal("reject rework must increment")
	}
}

func TestObserveIgnoredAfterTerminalStatus(t *testing.T) {
	e, s := newRecoveryEngine(t)
	now := time.Now().UTC()
	run := domain.Run{
		ID: "run-terminal", WorkflowID: "wf", Status: "failed", CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*domain.NodeRun{"a": {NodeID: "a", Status: "failed"}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e.observe("run-terminal", "a", domain.TelemetryEvent{Category: "tool", Name: "tool.call", Message: "ghost"})
	got, _ := s.Run("run-terminal")
	if got.TelemetrySequence != 0 {
		t.Fatalf("telemetry sequence = %d, want 0 after terminal guard", got.TelemetrySequence)
	}
}

func TestObserveConsumesPendingSessionResume(t *testing.T) {
	e, s := newRecoveryEngine(t)
	run := domain.Run{
		ID: "run-resume-session", WorkflowID: "wf", Status: "running",
		Nodes: map[string]*domain.NodeRun{"agent": {NodeID: "agent", Status: "running", SessionID: "old", ResumeSessionID: "old"}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e.observe(run.ID, "agent", domain.TelemetryEvent{Category: "process", Name: "process.started", SessionID: "old"})
	e.flushAllObservations()
	got, _ := s.Run(run.ID)
	if got.Nodes["agent"].SessionID != "old" || got.Nodes["agent"].ResumeSessionID != "" {
		t.Fatalf("resume marker was not consumed by CLI initialization: %+v", got.Nodes["agent"])
	}
}

func TestObserveFlushWritesQueuedTelemetry(t *testing.T) {
	e, s := newRecoveryEngine(t)
	run := domain.Run{
		ID: "run-obs-batch", WorkflowID: "wf", Status: "running",
		Nodes: map[string]*domain.NodeRun{"agent": {NodeID: "agent", Status: "running"}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		e.observe(run.ID, "agent", domain.TelemetryEvent{Category: "tool", Name: "tool.call", Message: "Write MonitorModel.php"})
	}
	e.flushAllObservations()
	got, _ := s.Run(run.ID)
	if got.TelemetrySequence != 8 {
		t.Fatalf("telemetry sequence = %d, want 8 after flush", got.TelemetrySequence)
	}
	if got.LogSequence != 8 {
		t.Fatalf("log sequence = %d, want 8 after flush", got.LogSequence)
	}
}

func TestObservePreservesAndDeduplicatesProducerEventID(t *testing.T) {
	e, s := newRecoveryEngine(t)
	run := domain.Run{
		ID: "run-event-id", WorkflowID: "wf", Status: "running",
		Nodes: map[string]*domain.NodeRun{"agent": {NodeID: "agent", Status: "running"}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	success := true
	event := domain.TelemetryEvent{
		ID: "worker-event-42", Category: "tool", Name: "tool.call", Phase: "finish",
		Tool:   &domain.ToolObservation{Name: "read", Success: &success},
		Source: domain.TelemetrySource{Layer: "executor", Adapter: "test"},
	}
	e.observe(run.ID, "agent", event)
	e.flushAllObservations()
	e.observe(run.ID, "agent", event)
	e.flushAllObservations()
	got, _ := s.Run(run.ID)
	if got.TelemetrySequence != 1 || len(got.Telemetry) != 1 || got.Telemetry[0].ID != event.ID {
		t.Fatalf("producer event id was not idempotent: sequence=%d telemetry=%#v", got.TelemetrySequence, got.Telemetry)
	}
	if got.ToolCalls != 1 || got.Nodes["agent"].ToolCalls != 1 {
		t.Fatalf("duplicate tool event changed counters: run=%d node=%d", got.ToolCalls, got.Nodes["agent"].ToolCalls)
	}
}

func TestCancelGhostRunWithRunningNode(t *testing.T) {
	e, s := newRecoveryEngine(t)
	now := time.Now().UTC()
	if err := s.PutRun(domain.Run{
		ID: "run-ghost", WorkflowID: "wf", Status: "failed", CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*domain.NodeRun{"verify": {NodeID: "verify", Status: "running"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.Cancel("run-ghost"); err != nil {
		t.Fatalf("cancel ghost run: %v", err)
	}
	got, _ := s.Run("run-ghost")
	if got.Status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", got.Status)
	}
	if got.Nodes["verify"].Status != "cancelled" {
		t.Fatalf("verify status = %q, want cancelled", got.Nodes["verify"].Status)
	}
}

func TestCancelWaitingRunWithoutScheduler(t *testing.T) {
	e, s := newRecoveryEngine(t)
	now := time.Now().UTC()
	if err := s.PutRun(domain.Run{
		ID: "run-waiting", WorkflowID: "wf", Status: "waiting", CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*domain.NodeRun{"approval": {NodeID: "approval", Status: "waiting_approval"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.Cancel("run-waiting"); err != nil {
		t.Fatalf("cancel waiting run: %v", err)
	}
	got, _ := s.Run("run-waiting")
	if got.Status != "cancelled" || got.Nodes["approval"].Status != "cancelled" {
		t.Fatalf("waiting run was not fully cancelled: %+v", got)
	}
}

func TestFinishStopsScheduler(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "finish-stop", Name: "finish stop", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "wait", Name: "wait", Type: "command", Command: "sleep 30", MaxAttempts: 1},
	}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := s.Run(r.ID)
		if current.Nodes["wait"].Status == "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.finish(r.ID, "failed", "forced stop")
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if e.ActiveRunCount() == 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("scheduler still active after finish, active=%d", e.ActiveRunCount())
}

func TestResumePausedResumesAgentSessionAndRestartsScriptWithoutContext(t *testing.T) {
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("test", "test", time.Second, false)
	wf := domain.WorkflowDefinition{ID: "pause-resume", Name: "pause resume", Nodes: []domain.WorkflowNode{
		{ID: "agent", Name: "agent", Type: "agent"},
		{ID: "script", Name: "script", Type: "script", Script: "echo rerun"},
	}}
	run := domain.Run{ID: "run-pause-resume", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "waiting", PauseRequested: true, Nodes: map[string]*domain.NodeRun{
		"agent":  {NodeID: "agent", Status: "waiting_approval", Activity: "已由用户暂停，等待补充信息", SessionID: "session-123"},
		"script": {NodeID: "script", Status: "waiting_approval", Activity: "已由用户暂停，继续后脚本将从头执行", SessionID: "not-a-script-session"},
	}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := e.ResumePaused(run.ID, "new fact from operator"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Run(run.ID)
	if got.Status != "running" || got.PauseRequested {
		t.Fatalf("run was not resumed: %+v", got)
	}
	if got.Nodes["agent"].ResumeSessionID != "session-123" || !strings.Contains(got.Nodes["agent"].Feedback, "new fact") {
		t.Fatalf("agent did not retain resumable context: %+v", got.Nodes["agent"])
	}
	if got.Nodes["script"].ResumeSessionID != "" || got.Nodes["script"].Feedback != "" {
		t.Fatalf("script must restart without Agent context: %+v", got.Nodes["script"])
	}
	if !strings.Contains(got.HumanContext, "new fact from operator") {
		t.Fatalf("Agent context was not recorded: %q", got.HumanContext)
	}
}

func TestResumePausedScriptIgnoresSubmittedContext(t *testing.T) {
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("test", "test", time.Second, false)
	wf := domain.WorkflowDefinition{ID: "script-pause", Nodes: []domain.WorkflowNode{{ID: "script", Type: "command"}}}
	run := domain.Run{ID: "run-script-pause", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "waiting", PauseRequested: true, Nodes: map[string]*domain.NodeRun{
		"script": {NodeID: "script", Status: "waiting_approval", Activity: "已由用户暂停，继续后脚本将从头执行"},
	}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := e.ResumePaused(run.ID, "must be ignored"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Run(run.ID)
	if got.HumanContext != "" || got.Nodes["script"].Feedback != "" {
		t.Fatalf("script-only resume accepted human context: %+v", got)
	}
}

func TestPassPausedScriptContinuesDownstream(t *testing.T) {
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "pass-paused-script", Name: "pass paused script", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "hard", Name: "hard", Type: "script", ScriptLanguage: "shell", Script: "exit 1", MaxAttempts: 1},
		{ID: "next", Name: "next", Type: "script", ScriptLanguage: "shell", Script: "printf 'ok\\n'", MaxAttempts: 1},
	}, Edges: []domain.WorkflowEdge{{ID: "e1", Source: "hard", Target: "next", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	now := time.Now().UTC()
	run := domain.Run{
		ID: "run-pass-paused-script", WorkflowID: wf.ID, WorkflowSnapshot: wf,
		Status: "waiting", PauseRequested: true, CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*domain.NodeRun{
			// Paused mid-execution by a human (nodeCancelled/pauseAt), not
			// because the script actually failed.
			"hard": {NodeID: "hard", Status: "waiting_approval", Attempt: 1, Activity: "已由用户暂停，继续后脚本将从头执行"},
			"next": {NodeID: "next", Status: "pending"},
		},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := e.Pass(run.ID, "hard", "脚本卡在环境问题上，人工确认跳过"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "succeeded")
	current, _ := s.Run(run.ID)
	if current.Nodes["hard"].Status != "succeeded" {
		t.Fatalf("hard after pass = %+v", current.Nodes["hard"])
	}
	if current.Nodes["hard"].Attempt != 1 {
		t.Fatalf("pass must not re-execute the paused node: attempt = %d", current.Nodes["hard"].Attempt)
	}
	if current.Nodes["next"].Status != "succeeded" {
		t.Fatalf("downstream should continue, next=%+v", current.Nodes["next"])
	}
	if current.PauseRequested {
		t.Fatalf("pause flag must be cleared after passing: %+v", current)
	}
	if current.HumanContext != "脚本卡在环境问题上，人工确认跳过" {
		t.Fatalf("humanContext = %q", current.HumanContext)
	}
}

func TestPassRejectsPausedAgentNode(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC()
	run := domain.Run{
		ID: "run-pass-paused-agent", WorkflowID: "wf", Status: "waiting", PauseRequested: true, CreatedAt: now, UpdatedAt: now,
		WorkflowSnapshot: domain.WorkflowDefinition{
			ID:    "wf",
			Nodes: []domain.WorkflowNode{{ID: "agent", Name: "提交工蜂 MR", Type: "agent"}},
		},
		Nodes: map[string]*domain.NodeRun{"agent": {NodeID: "agent", Status: "waiting_approval", Activity: "已由用户暂停，等待补充信息"}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(t.TempDir()))
	if err := e.Pass(run.ID, "agent", ""); err == nil || !strings.Contains(err.Error(), "script") {
		t.Fatalf("Pass error = %v, want script-only", err)
	}
}

func TestRefreshRunSOPPreservesCompletedNodesAndAddsLatestNodes(t *testing.T) {
	e, s := newRecoveryEngine(t)
	old := domain.WorkflowDefinition{ID: "refresh-paused", Name: "old", Version: 1, Revision: 2, SandboxProfileID: "frozen", Nodes: []domain.WorkflowNode{{ID: "done", Name: "done"}, {ID: "work", Name: "work"}}}
	latest := domain.WorkflowDefinition{ID: old.ID, Name: "latest", Version: 2, Revision: 1, SandboxProfileID: "new-environment", Nodes: []domain.WorkflowNode{{ID: "done", Name: "done latest"}, {ID: "work", Name: "work latest"}, {ID: "verify", Name: "verify"}}, Edges: []domain.WorkflowEdge{{ID: "e", Source: "work", Target: "verify", When: "success"}}}
	for _, sandbox := range []domain.SandboxProfile{{ID: "frozen", Name: "frozen", Driver: "local"}, {ID: "new-environment", Name: "new", Driver: "local"}} {
		if err := s.PutSandbox(sandbox); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutWorkflow(latest); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "run-refresh-paused", WorkflowID: old.ID, WorkflowSnapshot: old, Status: "waiting", PauseRequested: true, Nodes: map[string]*domain.NodeRun{
		"done": {NodeID: "done", Status: "succeeded"},
		"work": {NodeID: "work", Status: "waiting_approval", Activity: "已由用户暂停，等待补充信息"},
	}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := e.RefreshRunSOP(run.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Run(run.ID)
	if got.WorkflowSnapshot.Version != 2 || got.WorkflowSnapshot.Revision != 1 {
		t.Fatalf("workflow snapshot = v%d #%d", got.WorkflowSnapshot.Version, got.WorkflowSnapshot.Revision)
	}
	if got.WorkflowSnapshot.SandboxProfileID != "frozen" {
		t.Fatalf("sandbox profile changed mid-run: %q", got.WorkflowSnapshot.SandboxProfileID)
	}
	if got.Nodes["done"].Status != "succeeded" || got.Nodes["work"].Status != "waiting_approval" || got.Nodes["verify"].Status != "pending" {
		t.Fatalf("refreshed nodes = %+v", got.Nodes)
	}
}

func TestRefreshRunSOPTakesOverExpiredLease(t *testing.T) {
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("replacement-instance", "", 30*time.Second, true)
	old := domain.WorkflowDefinition{ID: "refresh-expired-lease", Name: "refresh expired lease", Version: 1, Revision: 1, Nodes: []domain.WorkflowNode{{ID: "first"}}}
	latest := old
	latest.Version = 2
	if err := s.PutWorkflow(latest); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "run-refresh-expired-lease", WorkflowID: old.ID, WorkflowSnapshot: old, Status: "waiting", OwnerInstanceID: "previous-instance", LeaseUntil: time.Now().Add(-time.Minute), Generation: 2, Nodes: map[string]*domain.NodeRun{
		"first": {NodeID: "first", Status: "waiting_approval"},
	}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := e.RefreshRunSOP(run.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Run(run.ID)
	if got.WorkflowSnapshot.Version != 2 || got.OwnerInstanceID != "replacement-instance" || got.Generation <= run.Generation || got.Nodes["first"].Status != "waiting_approval" {
		t.Fatalf("refreshed run = %+v", got)
	}
}

func TestRefreshWorkflowForStoppedRunStatuses(t *testing.T) {
	for _, tc := range []struct {
		name, status, workStatus string
		wantError                bool
	}{
		{"approval", "waiting", "waiting_approval", false},
		{"failed", "failed", "failed", false},
		{"succeeded", "succeeded", "succeeded", false},
		{"cancelled", "cancelled", "cancelled", false},
		{"running", "running", "running", true},
		{"waiting with active node", "waiting", "running", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, s := newRecoveryEngine(t)
			old := domain.WorkflowDefinition{ID: "refresh-stopped", Name: "refresh stopped", Version: 1, Revision: 1, Nodes: []domain.WorkflowNode{{ID: "done"}, {ID: "work"}}}
			latest := domain.WorkflowDefinition{ID: old.ID, Name: old.Name, Version: 2, Revision: 1, Nodes: []domain.WorkflowNode{{ID: "done"}, {ID: "work"}, {ID: "added"}}}
			if err := s.PutWorkflow(latest); err != nil {
				t.Fatal(err)
			}
			run := domain.Run{ID: "run-refresh-status", WorkflowID: old.ID, WorkflowSnapshot: old, Status: tc.status, Nodes: map[string]*domain.NodeRun{
				"done": {NodeID: "done", Status: "succeeded"},
				"work": {NodeID: "work", Status: tc.workStatus},
			}}
			if err := s.PutRun(run); err != nil {
				t.Fatal(err)
			}
			err := e.RefreshRunSOP(run.ID)
			if (err != nil) != tc.wantError {
				t.Fatalf("refresh error = %v, wantError = %t", err, tc.wantError)
			}
			got, _ := s.Run(run.ID)
			if tc.wantError {
				if got.WorkflowSnapshot.Version != 1 {
					t.Fatalf("active run changed SOP: v%d", got.WorkflowSnapshot.Version)
				}
				return
			}
			if got.WorkflowSnapshot.Version != 2 || got.Nodes["done"].Status != "succeeded" || got.Nodes["work"].Status != tc.workStatus || got.Nodes["added"].Status != "pending" || got.Status != tc.status {
				t.Fatalf("refreshed run = %+v", got)
			}
		})
	}
}

func TestRetryFromChosenNodeInStoppedRun(t *testing.T) {
	for _, tc := range []struct {
		name, status, downstreamStatus string
		cleaned                        bool
		wantError                      bool
	}{
		{"approval", "waiting", "waiting_approval", false, false},
		{"paused", "waiting", "waiting_approval", false, false},
		{"failed", "failed", "failed", false, false},
		{"failed and cleaned", "failed", "failed", true, false},
		{"cancelled and cleaned", "cancelled", "cancelled", true, false},
		{"succeeded", "succeeded", "succeeded", true, false},
		{"running", "running", "running", false, true},
		{"waiting with active node", "waiting", "running", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, s := newRecoveryEngine(t)
			e.executeRuns = false
			wf := domain.WorkflowDefinition{ID: "retry-stopped", Nodes: []domain.WorkflowNode{{ID: "first"}, {ID: "second"}}, Edges: []domain.WorkflowEdge{{ID: "first-second", Source: "first", Target: "second", When: "success"}}}
			run := domain.Run{ID: "run-retry-stopped", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: tc.status, PauseRequested: tc.name == "paused", Nodes: map[string]*domain.NodeRun{
				"first":  {NodeID: "first", Status: "succeeded"},
				"second": {NodeID: "second", Status: tc.downstreamStatus},
			}}
			if tc.cleaned {
				run.WorkspaceCleanedAt = time.Now().UTC()
			}
			if err := s.PutRun(run); err != nil {
				t.Fatal(err)
			}
			err := e.Retry(run.ID, "first", "补充说明", "请重新检查约束")
			if (err != nil) != tc.wantError {
				t.Fatalf("retry error = %v, wantError = %t", err, tc.wantError)
			}
			got, _ := s.Run(run.ID)
			if tc.wantError {
				if got.Status != tc.status || got.Nodes["first"].Status != "succeeded" {
					t.Fatalf("active run changed: %+v", got)
				}
				return
			}
			if got.Status != "running" || got.Nodes["first"].Status != "pending" || got.Nodes["second"].Status != "pending" || got.PauseRequested || !got.WorkspaceCleanedAt.IsZero() || !strings.Contains(got.Nodes["first"].Feedback, "请重新检查约束") {
				t.Fatalf("replayed run = %+v", got)
			}
		})
	}
}

func TestRetryRejectsNodeWithUnfinishedUpstream(t *testing.T) {
	for _, tc := range []struct{ name, runStatus, firstStatus string }{
		{"failed first node", "failed", "failed"},
		{"paused first node", "waiting", "waiting_approval"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, s := newRecoveryEngine(t)
			e.executeRuns = false
			wf := domain.WorkflowDefinition{ID: "upstream-guard", Nodes: []domain.WorkflowNode{{ID: "first"}, {ID: "second"}}, Edges: []domain.WorkflowEdge{{ID: "first-second", Source: "first", Target: "second", When: "success"}}}
			run := domain.Run{ID: "run-upstream-guard", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: tc.runStatus, Nodes: map[string]*domain.NodeRun{
				"first":  {NodeID: "first", Status: tc.firstStatus},
				"second": {NodeID: "second", Status: "pending"},
			}}
			if err := s.PutRun(run); err != nil {
				t.Fatal(err)
			}
			if err := e.Retry(run.ID, "second", "", ""); err == nil || !strings.Contains(err.Error(), "upstream") {
				t.Fatalf("expected upstream error, got %v", err)
			}
			got, _ := s.Run(run.ID)
			if got.Status != tc.runStatus || got.Nodes["first"].Status != tc.firstStatus || got.Nodes["second"].Status != "pending" {
				t.Fatalf("rejected replay changed run: %+v", got)
			}
			if err := e.Retry(run.ID, "first", "", ""); err != nil {
				t.Fatalf("first node should remain replayable: %v", err)
			}
		})
	}
}

func TestFailedRunCanRefreshSOPThenReplayWithContext(t *testing.T) {
	e, s := newRecoveryEngine(t)
	e.executeRuns = false
	old := domain.WorkflowDefinition{ID: "repair-failed-sop", Name: "repair", Version: 1, Revision: 1, Nodes: []domain.WorkflowNode{{ID: "done"}, {ID: "broken", Script: "exit 1"}}, Edges: []domain.WorkflowEdge{{ID: "a-b", Source: "done", Target: "broken", When: "success"}}}
	latest := domain.WorkflowDefinition{ID: old.ID, Name: old.Name, Version: 2, Revision: 1, Nodes: []domain.WorkflowNode{{ID: "done"}, {ID: "broken", Script: "exit 0"}, {ID: "added"}}, Edges: []domain.WorkflowEdge{{ID: "a-b", Source: "done", Target: "broken", When: "success"}, {ID: "b-c", Source: "broken", Target: "added", When: "success"}}}
	if err := s.PutWorkflow(latest); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "run-repair-failed-sop", WorkflowID: old.ID, WorkflowSnapshot: old, Status: "failed", Nodes: map[string]*domain.NodeRun{
		"done":   {NodeID: "done", Status: "succeeded"},
		"broken": {NodeID: "broken", Status: "failed", Error: "SOP script is wrong"},
	}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := e.RefreshRunSOP(run.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.Retry(run.ID, "broken", "SOP 修正后重跑", "按新检查步骤执行"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Run(run.ID)
	if got.Status != "running" || got.WorkflowSnapshot.Version != 2 || got.Nodes["done"].Status != "succeeded" || got.Nodes["broken"].Status != "pending" || got.Nodes["added"].Status != "pending" || !strings.Contains(got.Nodes["broken"].Feedback, "按新检查步骤执行") {
		t.Fatalf("refreshed replay = %+v", got)
	}
}
