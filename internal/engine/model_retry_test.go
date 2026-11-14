package engine

import (
	"errors"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestModelFailureRetryAndCredentialGate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		class runtime.ModelErrorClass
		want  string
	}{
		{"rate limit", runtime.ModelErrorRateLimited, "waiting_retry"},
		{"overload", runtime.ModelErrorOverloaded, "waiting_retry"},
		{"timeout", runtime.ModelErrorTimeout, "waiting_retry"},
		{"auth", runtime.ModelErrorAuth, "waiting_approval"},
		{"quota", runtime.ModelErrorQuotaExhausted, "waiting_approval"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, store := newRecoveryEngine(t)
			node := domain.WorkflowNode{ID: "n", Type: "agent", MaxAttempts: 1}
			run := domain.Run{ID: "model-failure", Status: "running", Generation: 1, WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf", Nodes: []domain.WorkflowNode{node}}, Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "running", Attempt: 1, ExecutionSequence: 7, SessionID: "session"}}}
			if err := store.PutRun(run); err != nil {
				t.Fatal(err)
			}
			e.nodeFailedAt(run.ID, node, 1, 1, &runtime.ModelError{Class: tc.class, Err: errors.New("provider failure")}, false, 7)
			got, _ := store.RunState(run.ID)
			n := got.Nodes["n"]
			if n.Status != tc.want || n.Attempt != 1 || n.ResumeSessionID != "session" {
				t.Fatalf("node: %+v", n)
			}
			if tc.want == "waiting_retry" {
				if n.TransientRetries != 1 || !n.RetrySameAttempt || n.NextRetryAt.Before(time.Now().Add(11*time.Second)) || n.NextRetryAt.After(time.Now().Add(16*time.Second)) {
					t.Fatalf("retry: %+v", n)
				}
				if modelRetryDue(got, time.Now()) {
					t.Fatal("retry became due early")
				}
				if !promoteDueModelRetries(&got, n.NextRetryAt) || got.Nodes["n"].Status != "pending" {
					t.Fatal("retry did not become pending")
				}
			} else {
				if n.WaitReason != "model_credentials" {
					t.Fatalf("gate: %+v", n)
				}
				got.Status = "waiting"
				if err := store.PutRun(got); err != nil {
					t.Fatal(err)
				}
				if err := e.Approve(run.ID, node.ID, ""); err == nil {
					t.Fatal("credential gate was approved as success")
				}
			}
		})
	}
}

func TestModelRetryExecutionFenceAndExhaustion(t *testing.T) {
	e, store := newRecoveryEngine(t)
	node := domain.WorkflowNode{ID: "n", MaxAttempts: 9}
	run := domain.Run{ID: "model-exhausted", Status: "running", Generation: 1, Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "running", Attempt: 1, ExecutionSequence: 2, TransientRetries: 5}}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	failure := &runtime.ModelError{Class: runtime.ModelErrorRateLimited, Err: errors.New("rate limit")}
	e.nodeFailedAt(run.ID, node, 1, 1, failure, false, 1)
	got, _ := store.RunState(run.ID)
	if got.Nodes["n"].Status != "running" {
		t.Fatal("stale execution changed current node")
	}
	e.nodeFailedAt(run.ID, node, 1, 1, failure, false, 2)
	got, _ = store.RunState(run.ID)
	if got.Nodes["n"].Status != "failed" || got.Nodes["n"].Attempt != 1 {
		t.Fatalf("exhaustion: %+v", got.Nodes["n"])
	}
}

func TestModelRetryHonorsServerDelay(t *testing.T) {
	e, store := newRecoveryEngine(t)
	run := domain.Run{ID: "retry-after", Status: "running", Generation: 1, Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "running", Attempt: 1}}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	e.nodeFailedAt(run.ID, domain.WorkflowNode{ID: "n"}, 1, 1, &runtime.ModelError{Class: runtime.ModelErrorRateLimited, Err: errors.New("limited"), RetryAfter: time.Minute}, false, 0)
	got, _ := store.RunState(run.ID)
	if got.Nodes["n"].NextRetryAt.Before(before.Add(time.Minute)) {
		t.Fatalf("retry scheduled too early: %+v", got.Nodes["n"])
	}
}

func TestModelRetryCapsServerDelay(t *testing.T) {
	e, store := newRecoveryEngine(t)
	maximum := 30
	run := domain.Run{ID: "retry-after-cap", Status: "running", Generation: 1, Nodes: map[string]*domain.NodeRun{"n": {
		NodeID: "n", Status: "running", Attempt: 1, ExecutionSequence: 3,
		ModelRetry: &domain.ModelRetryPolicy{MaxSeconds: &maximum},
	}}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	e.nodeFailedAt(run.ID, domain.WorkflowNode{ID: "n"}, 1, 1, &runtime.ModelError{Class: runtime.ModelErrorRateLimited, Err: errors.New("limited"), RetryAfter: 24 * time.Hour}, false, 3)
	after := time.Now().UTC()
	got, _ := store.RunState(run.ID)
	next := got.Nodes["n"].NextRetryAt
	if next.Before(before.Add(30*time.Second)) || next.After(after.Add(30*time.Second)) {
		t.Fatalf("retry = %s, want capped delay of 30s", next)
	}
}

func TestModelRetryRejectsStaleSuccessAndInterruption(t *testing.T) {
	e, store := newRecoveryEngine(t)
	node := domain.WorkflowNode{ID: "n"}
	run := domain.Run{ID: "stale-success", Status: "running", Generation: 1, Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "running", Attempt: 1, ExecutionSequence: 2}}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e.completeSkippedPreviewBuild(run.ID, node, 1, 1, run, "", 1)
	e.requeueInterruptedNode(run.ID, node.ID, 1, 1, 1)
	got, _ := store.RunState(run.ID)
	if n := got.Nodes[node.ID]; n.Status != "running" || n.Output != "" {
		t.Fatalf("stale execution changed current node: %+v", n)
	}
}

func TestDueModelRetryRecoversWithHumanGate(t *testing.T) {
	e, store := newRecoveryEngine(t)
	wf := domain.WorkflowDefinition{ID: "wf", Nodes: []domain.WorkflowNode{{ID: "retry", Type: "command", Command: "true"}, {ID: "gate", Type: "approval"}}}
	run := domain.Run{ID: "retry-recovery", Status: "waiting", WorkflowSnapshot: wf, Nodes: map[string]*domain.NodeRun{
		"retry": {NodeID: "retry", Status: "waiting_retry", Attempt: 1, RetrySameAttempt: true, NextRetryAt: time.Now().Add(-time.Second)},
		"gate":  {NodeID: "gate", Status: "waiting_approval"},
	}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	unlock := e.lockNodeExec(run.ID, "retry")
	defer unlock()
	resumed := e.ResumeInterruptedRuns()
	if len(resumed) != 1 || resumed[0] != run.ID {
		t.Fatalf("resumed = %v", resumed)
	}
	got, _ := store.RunState(run.ID)
	if got.Status != "running" || got.Nodes["gate"].Status != "waiting_approval" {
		t.Fatalf("recovered: %+v", got)
	}
}

func TestModelFallbackKeepsWorkspaceAndStartsNewSession(t *testing.T) {
	e, store := newRecoveryEngine(t)
	node := domain.WorkflowNode{ID: "n", MaxAttempts: 1}
	run := domain.Run{ID: "fallback", Status: "running", Generation: 1, Nodes: map[string]*domain.NodeRun{"n": {
		NodeID: "n", Status: "running", Attempt: 1, ExecutionSequence: 1, ActiveModel: "primary", ModelPolicyInitialized: true,
		FallbackModels: []string{"primary", "backup"}, SessionID: "old-session", ResumeSessionID: "old-session",
	}}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e.nodeFailedAt(run.ID, node, 1, 1, &runtime.ModelError{Class: runtime.ModelErrorContextOverflow, Err: errors.New("context too long")}, false, 1)
	got, _ := store.RunState(run.ID)
	n := got.Nodes["n"]
	if n.Status != "pending" || !n.RetrySameAttempt || n.ActiveModel != "backup" || n.SessionID != "" || n.ResumeSessionID != "" || n.Attempt != 1 {
		t.Fatalf("fallback: %+v", n)
	}
	if len(got.Events) == 0 || got.Events[len(got.Events)-1].Type != "model.fallback" {
		t.Fatalf("events: %+v", got.Events)
	}
}

func TestModelRetryPolicyInheritance(t *testing.T) {
	initial, maximum, retries := 3, 50, 2
	p := mergeModelRetryPolicy(&domain.ModelRetryPolicy{InitialSeconds: &initial, MaxSeconds: &maximum}, &domain.ModelRetryPolicy{MaxRetries: &retries})
	if a, b, c := modelRetrySettings(&domain.NodeRun{ModelRetry: p}); a != 3*time.Second || b != 50*time.Second || c != 2 {
		t.Fatalf("merged settings = %s, %s, %d", a, b, c)
	}
}

func TestConsecutiveFailuresSwitchMultipleFallbackModels(t *testing.T) {
	e, store := newRecoveryEngine(t)
	node := domain.WorkflowNode{ID: "n", MaxAttempts: 1}
	run := domain.Run{ID: "fallback-many", Status: "running", Generation: 1, Nodes: map[string]*domain.NodeRun{"n": {
		NodeID: "n", Status: "running", Attempt: 1, ActiveModel: "primary", FallbackModels: uniqueFallbackModels("primary", []string{"primary", "backup", "backup", "last"}),
	}}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"primary", "backup", "backup", "last"} {
		e.nodeFailedAt(run.ID, node, 1, 1, &runtime.ModelError{Class: runtime.ModelErrorRateLimited, Err: errors.New("limited")}, false, 0)
		got, _ := store.RunState(run.ID)
		n := got.Nodes["n"]
		if n.ActiveModel != want || n.TransientRetries != i+1 || n.Attempt != 1 {
			t.Fatalf("failure %d: %+v", i+1, n)
		}
		n.Status = "running"
		if err := store.PutRun(got); err != nil {
			t.Fatal(err)
		}
	}
}

func TestModelRetryResumesAttemptWithoutResettingWorkspace(t *testing.T) {
	e, store := newRecoveryEngine(t)
	if err := store.PutAgent(domain.AgentDefinition{ID: "retry-agent", Name: "Retry Agent", ExecutorID: "mock"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutExecutor(domain.ExecutorDefinition{ID: "mock", Name: "Mock", Kind: "mock"}); err != nil {
		t.Fatal(err)
	}
	initial, maximum := 1, 1
	wf := domain.WorkflowDefinition{ID: "retry-workflow", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "n", Type: "agent", AgentID: "retry-agent", MaxAttempts: 1, ModelRetry: &domain.ModelRetryPolicy{InitialSeconds: &initial, MaxSeconds: &maximum}}}}
	run := domain.Run{ID: "retry-integration", WorkflowID: wf.ID, Status: "running", WorkflowSnapshot: wf, Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "waiting_retry", Attempt: 1, TransientRetries: 2, RetrySameAttempt: true, NextRetryAt: time.Now().Add(-time.Second)}}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if resumed := e.ResumeInterruptedRuns(); len(resumed) != 1 {
		t.Fatalf("resumed: %v", resumed)
	}
	waitFor(t, store, run.ID, "succeeded")
	got, _ := store.RunState(run.ID)
	if got.Nodes["n"].Attempt != 1 || got.Nodes["n"].ExecutionSequence != 1 {
		t.Fatalf("node: %+v", got.Nodes["n"])
	}
}

// Exercise the scheduler and Runner dispatch together: two terminal 429s
// must not spend MaxAttempts or reset the Runner workspace before success.
func TestModelRetryTwoRateLimitsThenRunnerSuccess(t *testing.T) {
	e, store := newRecoveryEngine(t)
	if err := store.PutAgent(domain.AgentDefinition{ID: "recovery-agent", Name: "Recovery Agent", ExecutorID: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutExecutor(domain.ExecutorDefinition{ID: "codex", Name: "Codex", Kind: "codex", Model: "primary"}); err != nil {
		t.Fatal(err)
	}
	cvERunner(t, e, "recovery-runner", domain.RunnerStatusOnline, "terminal", "codex")
	count := 0
	var workspaces []string
	e.SetRunnerDispatchNotify(func(string) {
		tasks := e.repo.RunnerTasks()
		var task domain.RunnerTask
		for _, candidate := range tasks {
			if candidate.Status == domain.RunnerTaskQueued {
				task = candidate
				break
			}
		}
		if task.ID == "" {
			t.Error("missing queued task")
			return
		}
		count++
		workspaces = append(workspaces, task.Workspace)
		task.Attempt = 1
		if count <= 2 {
			task.Status = domain.RunnerTaskFailed
			task.Error = "HTTP 429 Too Many Requests"
			task.ModelErrorClass = "rate_limited"
		} else {
			task.Status = domain.RunnerTaskSuccess
			task.Result = "done"
			task.Outputs = &domain.RunnerTaskOutputs{Files: map[string]string{task.Invocation.GateResultPath: `{"decision":"pass","reason":"complete"}`}}
		}
		if err := e.repo.PutRunnerTask(task); err != nil {
			t.Errorf("complete task: %v", err)
		}
	})
	initial, maximum := 1, 1
	wf := domain.WorkflowDefinition{ID: "real-scheduler", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "n", Type: "agent", AgentID: "recovery-agent", MaxAttempts: 1, ModelRetry: &domain.ModelRetryPolicy{InitialSeconds: &initial, MaxSeconds: &maximum}}}}
	run := domain.Run{ID: "two-limits", WorkflowID: wf.ID, Status: "running", WorkflowSnapshot: wf, ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: "recovery-runner", RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared}, Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "pending"}}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if resumed := e.ResumeInterruptedRuns(); len(resumed) != 1 {
		t.Fatalf("resumed: %v", resumed)
	}
	waitFor(t, store, run.ID, "succeeded")
	got, _ := store.Run(run.ID)
	if count != 3 || got.Nodes["n"].Attempt != 1 || len(workspaces) != 3 || workspaces[0] != workspaces[1] || workspaces[1] != workspaces[2] {
		t.Fatalf("calls=%d workspaces=%v node=%+v", count, workspaces, got.Nodes["n"])
	}
	retries := 0
	for _, ev := range got.Events {
		if ev.Type == "model.retry_waiting" {
			retries++
			t.Logf("retry: %s", ev.Message)
		}
	}
	if retries != 2 {
		t.Fatalf("retry events = %d", retries)
	}
	t.Logf("runner invocations=%d node attempt=%d workspace=%s final=%s", count, got.Nodes["n"].Attempt, workspaces[0], got.Status)
}

func TestModelRecoveryResetOnManualRetry(t *testing.T) {
	for _, credentials := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "credentials"}[credentials], func(t *testing.T) {
			e, store := cvEEngine(t)
			e.executeRuns = false
			maximum := 30
			n := &domain.NodeRun{NodeID: "n", Status: "failed", Attempt: 1, WaitReason: "other", ModelErrorClass: "rate_limited", NextRetryAt: time.Now(), TransientRetries: 2, ModelTransientErrors: 1, RetrySameAttempt: true, ModelPolicyInitialized: true, ModelRetry: &domain.ModelRetryPolicy{MaxSeconds: &maximum}, FallbackModels: []string{"backup"}, FallbackModelIndex: 1, ActiveModel: "primary"}
			status := "failed"
			if credentials {
				status, n.Status, n.WaitReason = "waiting", "waiting_approval", "model_credentials"
			}
			wf := domain.WorkflowDefinition{ID: "reset-wf", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "n", Type: "agent"}}}
			run := domain.Run{ID: "reset", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: status, Nodes: map[string]*domain.NodeRun{"n": n}}
			if err := store.PutRun(run); err != nil {
				t.Fatal(err)
			}
			if err := e.Retry(run.ID, "n", "", ""); err != nil {
				t.Fatal(err)
			}
			got, _ := store.RunState(run.ID)
			n = got.Nodes["n"]
			if n.Status != "pending" || n.WaitReason != "" || n.ModelErrorClass != "" || !n.NextRetryAt.IsZero() || n.TransientRetries != 0 || n.ModelTransientErrors != 0 || n.RetrySameAttempt != credentials {
				t.Fatalf("retry state: %+v", n)
			}
			if credentials {
				if !n.ModelPolicyInitialized || n.ModelRetry == nil || len(n.FallbackModels) != 1 || n.FallbackModelIndex != 1 || n.ActiveModel != "primary" {
					t.Fatalf("credential retry lost policy: %+v", n)
				}
			} else if n.ModelPolicyInitialized || n.ModelRetry != nil || len(n.FallbackModels) != 0 || n.FallbackModelIndex != 0 || n.ActiveModel != "" {
				t.Fatalf("ordinary retry retained policy: %+v", n)
			}
		})
	}
}
