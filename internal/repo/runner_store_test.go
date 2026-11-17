package repo

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestRunnerTaskRunGenerationFencesCallbacksAndLateDispatch(t *testing.T) {
	s := newTestRunner(t)
	run := domain.Run{ID: "runner-generation", Status: "running"}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.TryAcquireRunLease(run.ID, "worker-a", 0, time.Minute); err != nil {
		t.Fatal(err)
	}
	owned, _ := s.RunState(run.ID)
	task := domain.RunnerTask{ID: "task-generation", RunnerID: "runner", Provider: "terminal", RunID: run.ID, RunGeneration: owned.Generation}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	claim, ok, err := s.ClaimRunnerTask("runner", []string{"terminal"}, now, time.Minute, true)
	if err != nil || !ok {
		t.Fatalf("claim: %v, found=%v", err, ok)
	}
	if err := s.ReleaseRunLease(run.ID, "worker-a", owned.Generation); err != nil {
		t.Fatal(err)
	}
	if err := s.TryAcquireRunLease(run.ID, "worker-b", owned.Generation, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RenewRunnerTaskLease(task.ID, "runner", time.Minute, now, claim.Attempt); !errors.Is(err, ErrRunnerTaskLeaseLost) {
		t.Fatalf("stale renew = %v", err)
	}
	if _, err := s.AppendLeasedRunnerTaskEvents(task.ID, "runner", claim.Attempt, now, []domain.RunnerTaskEvent{{Seq: 1, Type: "stdout"}}); !errors.Is(err, ErrRunnerTaskLeaseLost) {
		t.Fatalf("stale event = %v", err)
	}
	if err := s.CompleteRunnerTask(task.ID, "runner", domain.RunnerTaskSuccess, nil, "stale", "", nil, nil, now, claim.Attempt); !errors.Is(err, ErrRunnerTaskLeaseLost) {
		t.Fatalf("stale completion = %v", err)
	}
	if _, err := s.LeasedRunnerTaskControl(task.ID, "runner", claim.Attempt, now); !errors.Is(err, ErrRunnerTaskLeaseLost) {
		t.Fatalf("stale control = %v", err)
	}
	if err := s.CancelRunnerTask(task.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRunnerTask(task.ID, "runner", domain.RunnerTaskCanceled, nil, "stale output", "", map[string]string{"stale": "content"}, nil, now, claim.Attempt); err != nil {
		t.Fatalf("stale cancellation acknowledgement: %v", err)
	}
	finished, _ := s.RunnerTask(task.ID)
	if finished.Status != domain.RunnerTaskCanceled || finished.Result != "" || len(finished.Artifacts) != 0 {
		t.Fatalf("stale cancellation retained outputs: %+v", finished)
	}
	task.ID = "late-dispatch"
	if err := s.PutRunnerTask(task); !errors.Is(err, ErrRunnerTaskLeaseLost) {
		t.Fatalf("stale dispatch = %v", err)
	}
}

func TestRunnerClaimSkipsTaskFromExpiredRunLease(t *testing.T) {
	s := newTestRunner(t)
	run := domain.Run{ID: "expired-run", Status: "running"}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.TryAcquireRunLease(run.ID, "worker", 0, time.Minute); err != nil {
		t.Fatal(err)
	}
	owned, _ := s.RunState(run.ID)
	task := domain.RunnerTask{ID: "expired-task", RunnerID: "runner", Provider: "terminal", RunID: run.ID, RunGeneration: owned.Generation}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseRunLease(run.ID, "worker", owned.Generation); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimRunnerTask("runner", []string{"terminal"}, time.Now().UTC(), time.Minute, true); err != nil || ok {
		t.Fatalf("stale claim: found=%v err=%v", ok, err)
	}
	stored, _ := s.RunnerTask(task.ID)
	if stored.Status != domain.RunnerTaskCanceled {
		t.Fatalf("stale task status = %s", stored.Status)
	}
}

func newTestRunner(t *testing.T) *Store {
	t.Helper()
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func putQueuedTask(t *testing.T, s *Store, id, provider, runnerID string, createdAgo time.Duration) domain.RunnerTask {
	t.Helper()
	task := domain.RunnerTask{
		ID:          id,
		RunnerID:    runnerID,
		Provider:    provider,
		Prompt:      "do something",
		Status:      domain.RunnerTaskQueued,
		MaxAttempts: 1,
	}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	return task
}

func TestRunnerTaskClaimIsExclusive(t *testing.T) {
	s := newTestRunner(t)
	putQueuedTask(t, s, "rt-1", "codex", "", 0)

	now := time.Now().UTC()
	first, claimed, err := s.ClaimRunnerTask("runner-a", []string{"codex"}, now, 2*time.Minute)
	if err != nil || !claimed {
		t.Fatalf("first claim failed: claimed=%v err=%v", claimed, err)
	}
	if first.ID != "rt-1" || first.Status != domain.RunnerTaskLeased || first.LeaseOwner != "runner-a" {
		t.Fatalf("unexpected claimed task: %+v", first)
	}
	if first.Attempt != 1 {
		t.Fatalf("attempt = %d, want 1", first.Attempt)
	}

	_, claimed, err = s.ClaimRunnerTask("runner-b", []string{"codex"}, now, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("a queued task was leased twice")
	}
}

func TestRunnerHeartbeatPreservesRuntimeAuthMode(t *testing.T) {
	s := newTestRunner(t)
	now := time.Now().UTC()
	if err := s.PutRunner(domain.Runner{ID: "runner-auth", Status: domain.RunnerStatusOnline, RunnerVersion: "0.1.0", Runtimes: []domain.RunnerRuntime{{Provider: "codex", Status: domain.RuntimeStatusOnline}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunnerRuntimeAuthMode("runner-auth", "codex", domain.RuntimeAuthPersonal, now); err != nil {
		t.Fatal(err)
	}
	if err := s.RunnerHeartbeat("runner-auth", []domain.RunnerRuntime{{Provider: "codex", Status: domain.RuntimeStatusOnline}}, "", now.Add(time.Second), now.Add(30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	runner, _ := s.Runner("runner-auth")
	if got := runner.RuntimeAuthMode("codex"); got != domain.RuntimeAuthPersonal {
		t.Fatalf("auth mode after heartbeat = %q, want personal", got)
	}
	if runner.RunnerVersion != "0.1.0" {
		t.Fatalf("legacy heartbeat erased runner version: %q", runner.RunnerVersion)
	}
	if runner.SessionExpiresAt == nil || !runner.SessionExpiresAt.After(now.Add(29*24*time.Hour)) {
		t.Fatalf("runner session was not renewed: %v", runner.SessionExpiresAt)
	}
	if err := s.SetRunnerRuntimeAuthMode("runner-auth", "codebuddy", domain.RuntimeAuthLocal, now); err == nil {
		t.Fatal("CodeBuddy local auth should be rejected")
	}
}

func TestAgentCLIHeartbeatForcesPersonalAuth(t *testing.T) {
	s := newTestRunner(t)
	now := time.Now().UTC()
	if err := s.PutRunner(domain.Runner{ID: "runner-agy", Status: domain.RunnerStatusOnline, Runtimes: []domain.RunnerRuntime{{Provider: "opencode", Status: domain.RuntimeStatusOnline, AuthMode: domain.RuntimeAuthLocal}}}); err != nil {
		t.Fatal(err)
	}
	runner, _ := s.Runner("runner-agy")
	if got := runner.RuntimeAuthMode("opencode"); got != domain.RuntimeAuthPersonal {
		t.Fatalf("stored local agent CLI auth = %q", got)
	}
	if err := s.SetRunnerRuntimeAuthMode("runner-agy", "opencode", domain.RuntimeAuthLocal, now); err == nil {
		t.Fatal("local auth accepted for opencode")
	}
	if err := s.RunnerHeartbeat("runner-agy", []domain.RunnerRuntime{{Provider: "opencode", Status: domain.RuntimeStatusOnline}}, "1", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	runner, _ = s.Runner("runner-agy")
	if len(runner.Runtimes) != 1 || runner.Runtimes[0].AuthMode != domain.RuntimeAuthPersonal {
		t.Fatalf("heartbeat = %+v", runner.Runtimes)
	}
}

func TestRunnerTaskClaimRespectsProviderAndTargeting(t *testing.T) {
	s := newTestRunner(t)
	putQueuedTask(t, s, "rt-codex", "codex", "", 0)
	putQueuedTask(t, s, "rt-claude", "claude-code", "", 0)
	putQueuedTask(t, s, "rt-pinned", "codex", "runner-c", 0)

	now := time.Now().UTC()
	// runner-a supports claude only: it must not see the codex tasks.
	task, claimed, err := s.ClaimRunnerTask("runner-a", []string{"claude-code"}, now, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claude claim failed: claimed=%v err=%v", claimed, err)
	}
	if task.ID != "rt-claude" {
		t.Fatalf("claimed %s, want rt-claude", task.ID)
	}

	// runner-b may take the untargeted codex task.
	task, claimed, err = s.ClaimRunnerTask("runner-b", []string{"codex"}, now, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("untargeted codex claim failed: claimed=%v err=%v", claimed, err)
	}
	if task.ID != "rt-codex" {
		t.Fatalf("claimed %s, want rt-codex", task.ID)
	}

	// runner-c is the only one allowed to take the pinned codex task; another
	// codex runner must not steal it.
	_, claimed, err = s.ClaimRunnerTask("runner-d", []string{"codex"}, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("runner-d stole a task pinned to runner-c")
	}
	task, claimed, err = s.ClaimRunnerTask("runner-c", []string{"codex"}, now, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("targeted claim failed: claimed=%v err=%v", claimed, err)
	}
	if task.ID != "rt-pinned" {
		t.Fatalf("claimed %s, want rt-pinned", task.ID)
	}
}

func TestRunnerTaskLeaseLifecycle(t *testing.T) {
	s := newTestRunner(t)
	seed := putQueuedTask(t, s, "rt-1", "codex", "", 0)
	seed.Invocation = &domain.RunnerInvocation{
		CredentialSource: domain.RuntimeAuthPersonal,
		CodexAPIKey:      "codex-secret",
		CodexBaseURL:     "https://gateway.example/v1",
		Repository:       &domain.RunnerRepository{GitToken: "git-secret"},
	}
	if err := s.PutRunnerTask(seed); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	task, claimed, _ := s.ClaimRunnerTask("runner-a", []string{"codex"}, now, time.Minute)
	if !claimed {
		t.Fatal("claim failed")
	}

	// Renewal from a different runner must be rejected.
	if _, err := s.RenewRunnerTaskLease(task.ID, "runner-b", time.Minute, now); err != ErrRunnerTaskLeaseLost {
		t.Fatalf("renew by non-owner err = %v, want ErrRunnerTaskLeaseLost", err)
	}
	// Cancellation travels on the renewal response.
	if err := s.CancelRunnerTask(task.ID, now); err != nil {
		t.Fatal(err)
	}
	desired, err := s.RenewRunnerTaskLease(task.ID, "runner-a", time.Minute, now)
	if err != nil || desired != domain.RunnerTaskDesiredCancel {
		t.Fatalf("renew desired=%q err=%v", desired, err)
	}

	exit := 0
	if err := s.CompleteRunnerTask(task.ID, "runner-a", domain.RunnerTaskSuccess, &exit, "done", "", map[string]string{"plan": "# plan"}, nil, now); err != nil {
		t.Fatal(err)
	}
	final, ok := s.RunnerTask(task.ID)
	if !ok || final.Status != domain.RunnerTaskSuccess || final.LeaseOwner != "" || final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("unexpected final task: %+v", final)
	}
	if final.Artifacts["plan"] != "# plan" {
		t.Fatalf("uploaded artifacts not stored: %+v", final.Artifacts)
	}
	if final.Invocation.CodexAPIKey != "" || final.Invocation.CodexBaseURL != "" || final.Invocation.Repository.GitToken != "" {
		t.Fatalf("task credentials survived completion: %+v", final.Invocation)
	}
	// Completing twice is rejected.
	if err := s.CompleteRunnerTask(task.ID, "runner-a", domain.RunnerTaskFailed, nil, "", "", nil, nil, now); err != ErrRunnerTaskLeaseLost {
		t.Fatalf("double complete err = %v", err)
	}
}

func TestRunnerTaskAttemptFencesReclaimedLease(t *testing.T) {
	s := newTestRunner(t)
	task := putQueuedTask(t, s, "rt-reclaimed", "codex", "runner-a", 0)
	task.MaxAttempts = 2
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	first, ok, err := s.ClaimRunnerTask("runner-a", []string{"codex"}, now, time.Second)
	if err != nil || !ok {
		t.Fatalf("first claim: %v", err)
	}
	later := now.Add(2 * time.Second)
	if err := s.CompleteRunnerTask(task.ID, "runner-a", domain.RunnerTaskSuccess, nil, "expired", "", nil, nil, later, first.Attempt); err != ErrRunnerTaskLeaseLost {
		t.Fatalf("expired lease accepted: %v", err)
	}
	if _, err := s.ExpireRunnerTasks(later); err != nil {
		t.Fatal(err)
	}
	second, ok, err := s.ClaimRunnerTask("runner-a", []string{"codex"}, later, time.Minute)
	if err != nil || !ok || second.Attempt != first.Attempt+1 {
		t.Fatalf("second claim: %+v %v", second, err)
	}
	for _, attempt := range []int{0, first.Attempt} {
		if err := s.StartRunnerTask(task.ID, "runner-a", later, attempt); err != ErrRunnerTaskLeaseLost {
			t.Fatalf("stale start attempt %d: %v", attempt, err)
		}
		if _, err := s.RenewRunnerTaskLease(task.ID, "runner-a", time.Minute, later, attempt); err != ErrRunnerTaskLeaseLost {
			t.Fatalf("stale renew attempt %d: %v", attempt, err)
		}
		if _, err := s.AppendLeasedRunnerTaskEvents(task.ID, "runner-a", attempt, later, []domain.RunnerTaskEvent{{Seq: 1}}); err != ErrRunnerTaskLeaseLost {
			t.Fatalf("stale event attempt %d: %v", attempt, err)
		}
		if err := s.CompleteRunnerTask(task.ID, "runner-a", domain.RunnerTaskSuccess, nil, "stale", "", nil, nil, later, attempt); err != ErrRunnerTaskLeaseLost {
			t.Fatalf("stale completion attempt %d: %v", attempt, err)
		}
	}
	if err := s.StartRunnerTask(task.ID, "runner-a", later, second.Attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendLeasedRunnerTaskEvents(task.ID, "runner-a", second.Attempt, later, []domain.RunnerTaskEvent{{Seq: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRunnerTask(task.ID, "runner-a", domain.RunnerTaskSuccess, nil, "current", "", nil, nil, later, second.Attempt); err != nil {
		t.Fatal(err)
	}
}

func TestSingletonRunnerTaskKeepsFenceAcrossLifecycles(t *testing.T) {
	s := newTestRunner(t)
	task := domain.RunnerTask{ID: "cleanup", Provider: "terminal", RunnerID: "runner-a", MaxAttempts: 3}
	if queued, err := s.QueueSingletonRunnerTask(task); err != nil || !queued {
		t.Fatalf("queue: %v %v", queued, err)
	}
	if queued, err := s.QueueSingletonRunnerTask(task); err != nil || queued {
		t.Fatalf("duplicate queue: %v %v", queued, err)
	}
	now := time.Now().UTC()
	first, ok, err := s.ClaimRunnerTask("runner-a", []string{"terminal"}, now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim: %v", err)
	}
	if err := s.CompleteRunnerTask(task.ID, "runner-a", domain.RunnerTaskSuccess, nil, "", "", nil, nil, now, first.Attempt); err != nil {
		t.Fatal(err)
	}
	if queued, err := s.QueueSingletonRunnerTask(task); err != nil || !queued {
		t.Fatalf("requeue: %v %v", queued, err)
	}
	second, ok, err := s.ClaimRunnerTask("runner-a", []string{"terminal"}, now, time.Minute)
	if err != nil || !ok || second.Attempt != first.Attempt+1 || second.MaxAttempts != first.Attempt+3 {
		t.Fatalf("new lifecycle: %+v %v", second, err)
	}
	if err := s.CompleteRunnerTask(task.ID, "runner-a", domain.RunnerTaskSuccess, nil, "stale", "", nil, nil, now, first.Attempt); err != ErrRunnerTaskLeaseLost {
		t.Fatalf("old lifecycle completed new task: %v", err)
	}
}

func TestLegacyRunnerCannotClaimReusedTask(t *testing.T) {
	s := newTestRunner(t)
	task := domain.RunnerTask{ID: "cleanup-reused", Provider: "terminal", RunnerID: "runner-a", Status: domain.RunnerTaskQueued, Attempt: 1, MaxAttempts: 3}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	if available, err := s.RunnerTaskAvailable("runner-a", []string{"terminal"}, false); err != nil || available {
		t.Fatalf("legacy preflight: available=%v err=%v", available, err)
	}
	if _, claimed, err := s.ClaimRunnerTask("runner-a", []string{"terminal"}, time.Now().UTC(), time.Minute, false); err != nil || claimed {
		t.Fatalf("legacy claim: claimed=%v err=%v", claimed, err)
	}
	if _, claimed, err := s.ClaimRunnerTask("runner-a", []string{"terminal"}, time.Now().UTC(), time.Minute, true); err != nil || !claimed {
		t.Fatalf("fenced claim: claimed=%v err=%v", claimed, err)
	}
}

func TestCanceledQueuedRunnerTaskScrubsCredentials(t *testing.T) {
	s := newTestRunner(t)
	task := putQueuedTask(t, s, "rt-cancel-secret", "claude-code", "", 0)
	task.Invocation = &domain.RunnerInvocation{
		CredentialSource:  domain.RuntimeAuthPersonal,
		ClaudeCodeAPIKey:  "claude-secret",
		ClaudeCodeBaseURL: "https://claude.example",
	}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelRunnerTask(task.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	final, _ := s.RunnerTask(task.ID)
	if final.Invocation.ClaudeCodeAPIKey != "" || final.Invocation.ClaudeCodeBaseURL != "" {
		t.Fatalf("task credentials survived cancellation: %+v", final.Invocation)
	}
}

func TestRunnerTaskExpireRequeuesOrFails(t *testing.T) {
	s := newTestRunner(t)
	retryable := putQueuedTask(t, s, "rt-retry", "codex", "", 0)
	retryable.MaxAttempts = 2
	if err := s.PutRunnerTask(retryable); err != nil {
		t.Fatal(err)
	}
	putQueuedTask(t, s, "rt-final", "codex", "", 0)

	now := time.Now().UTC()
	if _, claimed, _ := s.ClaimRunnerTask("runner-a", []string{"codex"}, now, time.Second); !claimed {
		t.Fatal("claim rt-retry failed")
	}
	// rt-final has max_attempts=1 and is already attempted once.
	if _, claimed, _ := s.ClaimRunnerTask("runner-b", []string{"codex"}, now, time.Second); !claimed {
		t.Fatal("claim rt-final failed")
	}

	recovered, err := s.ExpireRunnerTasks(now.Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 2 {
		t.Fatalf("recovered %d tasks, want 2", len(recovered))
	}
	retry, _ := s.RunnerTask("rt-retry")
	if retry.Status != domain.RunnerTaskQueued {
		t.Fatalf("retryable task status = %s, want queued", retry.Status)
	}
	if retry.Attempt != 1 {
		t.Fatalf("retryable attempt = %d, want 1 (a re-claim will make it 2)", retry.Attempt)
	}
	final, _ := s.RunnerTask("rt-final")
	if final.Status != domain.RunnerTaskFailed {
		t.Fatalf("exhausted task status = %s, want failed", final.Status)
	}
}

func TestRunnerTaskEventsIdempotent(t *testing.T) {
	s := newTestRunner(t)
	putQueuedTask(t, s, "rt-1", "codex", "", 0)

	events := []domain.RunnerTaskEvent{
		{Seq: 1, Type: "stdout", Data: "line-1"},
		{Seq: 2, Type: "stdout", Data: "line-2"},
	}
	if n, err := s.AppendRunnerTaskEvents("rt-1", events); err != nil || n != 2 {
		t.Fatalf("append n=%d err=%v", n, err)
	}
	// A retried batch with the same seq numbers must be ignored.
	duplicate := []domain.RunnerTaskEvent{
		{Seq: 1, Type: "stdout", Data: "line-1"},
		{Seq: 3, Type: "stdout", Data: "line-3"},
	}
	if n, err := s.AppendRunnerTaskEvents("rt-1", duplicate); err != nil || n != 1 {
		t.Fatalf("retry append n=%d err=%v", n, err)
	}
	stored := s.RunnerTaskEvents("rt-1")
	if len(stored) != 3 {
		t.Fatalf("stored %d events, want 3", len(stored))
	}
	if stored[2].Data != "line-3" {
		t.Fatalf("unexpected third event: %+v", stored[2])
	}
}

func TestRunnerTaskPayloadTimeFormats(t *testing.T) {
	// MySQL JSON_SET writes DATETIME-shaped strings; Go marshaling writes
	// RFC3339. Both must decode, and neither may break the payload decode.
	cases := []string{
		`{"prompt":"p","startedAt":"2026-09-08 10:31:58.280893"}`,
		`{"prompt":"p","startedAt":"2026-09-08T10:31:58.280893Z"}`,
		`{"prompt":"p","startedAt":"2026-09-08T10:31:58.280893+08:00","completedAt":"2026-09-08 10:32:01"}`,
		`{"prompt":"p","startedAt":"not-a-timestamp"}`,
	}
	for _, raw := range cases {
		var p runnerTaskPayload
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		if p.Prompt != "p" {
			t.Fatalf("payload %s lost prompt", raw)
		}
	}
	var p runnerTaskPayload
	if err := json.Unmarshal([]byte(cases[0]), &p); err != nil {
		t.Fatal(err)
	}
	if p.StartedAt == nil || p.StartedAt.Year() != 2026 {
		t.Fatalf("MySQL DATETIME not parsed: %+v", p.StartedAt)
	}
}

func TestRunnerSessionLookup(t *testing.T) {
	s := newTestRunner(t)
	expires := time.Now().UTC().Add(time.Hour)
	runner := domain.Runner{
		ID: "runner-1", Name: "mac-1", OS: "darwin", Arch: "arm64",
		Status: domain.RunnerStatusOnline, MaxConcurrency: 2,
		Runtimes:         []domain.RunnerRuntime{{Provider: "codex", BinaryPath: "/usr/local/bin/codex", Status: domain.RuntimeStatusOnline}},
		SessionTokenHash: "hash-1", SessionExpiresAt: &expires,
	}
	if err := s.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.RunnerSessionByHash("hash-1"); !ok {
		t.Fatal("valid session not resolved")
	}
	if _, ok := s.RunnerSessionByHash("hash-2"); ok {
		t.Fatal("unknown session resolved")
	}

	expired := time.Now().UTC().Add(-time.Minute)
	if err := s.PutRunner(domain.Runner{ID: "runner-2", SessionTokenHash: "hash-old", SessionExpiresAt: &expired}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.RunnerSessionByHash("hash-old"); ok {
		t.Fatal("expired session resolved")
	}
}
