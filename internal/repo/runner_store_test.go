package repo

import (
	"encoding/json"
	"testing"
	"time"

	"agentflow/internal/domain"
)

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

func TestCanceledQueuedRunnerTaskScrubsCredentials(t *testing.T) {
	s := newTestRunner(t)
	task := putQueuedTask(t, s, "rt-cancel-secret", "claude-code", "", 0)
	task.Invocation = &domain.RunnerInvocation{
		CredentialSource:  domain.RuntimeAuthPersonal,
		ClaudeCodeAPIKey:  "claude-secret",
		ClaudeCodeBaseURL: "https://claude.example",
		TaihuPAT:          "tai_pat_secret",
	}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelRunnerTask(task.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	final, _ := s.RunnerTask(task.ID)
	if final.Invocation.ClaudeCodeAPIKey != "" || final.Invocation.ClaudeCodeBaseURL != "" || final.Invocation.TaihuPAT != "" {
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
