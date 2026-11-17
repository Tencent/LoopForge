package repo

import (
	"errors"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestRunnerRegistryValidationRuntimeAndPreferences(t *testing.T) {
	s := cvEStore(t)
	now := time.Now().UTC()
	if err := s.PutRunner(domain.Runner{}); err == nil {
		t.Fatal("empty runner accepted")
	}
	expires := now.Add(time.Hour)
	runner := domain.Runner{
		ID: "cve-runner-more", Name: "Runner", Status: domain.RunnerStatusOnline, OwnerUserID: 12,
		SessionTokenHash: "session-hash", SessionExpiresAt: &expires, Labels: []string{"one"},
		Runtimes: []domain.RunnerRuntime{
			{Provider: "codex", Status: domain.RuntimeStatusOnline, AuthMode: domain.RuntimeAuthLocal},
			{Provider: "codebuddy", Status: domain.RuntimeStatusOnline, AuthMode: domain.RuntimeAuthPersonal},
		},
		LastSeenAt: &now,
	}
	if err := s.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Runner(runner.ID); !ok || got.OwnerUserID != 12 || len(got.Labels) != 1 || len(got.Runtimes) != 2 {
		t.Fatalf("runner=%+v ok=%v", got, ok)
	}
	if got := s.Runners(); len(got) != 1 {
		t.Fatalf("runners=%v", got)
	}
	if got, ok := s.RunnerSessionByHash("session-hash"); !ok || got.ID != runner.ID {
		t.Fatalf("session runner=%+v ok=%v", got, ok)
	}
	if _, ok := s.RunnerSessionByHash(""); ok {
		t.Fatal("empty session matched")
	}
	if err := s.SetRunnerRunLimit(runner.ID, -1); err == nil {
		t.Fatal("negative run limit accepted")
	}
	if err := s.SetRunnerRunLimit(runner.ID, MaxRunnerRunLimit+1); err == nil {
		t.Fatal("excessive run limit accepted")
	}
	if err := s.SetRunnerRunLimit("missing", 1); !errors.Is(err, ErrRunnerNotFound) {
		t.Fatalf("missing run limit err=%v", err)
	}
	if err := s.SetRunnerRunLimit(runner.ID, 3); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Runner(runner.ID); got.RunLimit != 3 {
		t.Fatalf("run limit=%d", got.RunLimit)
	}
	if err := s.SetRunnerRunLimit(runner.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.RunnerHeartbeat("missing", nil, "", false, now, expires); !errors.Is(err, ErrRunnerNotFound) {
		t.Fatalf("missing heartbeat err=%v", err)
	}
	if err := s.SetRunnerRuntimeAuthMode(runner.ID, "codex", "bad", now); err == nil {
		t.Fatal("invalid auth mode accepted")
	}
	if err := s.SetRunnerRuntimeAuthMode(runner.ID, "codebuddy", domain.RuntimeAuthLocal, now); err == nil {
		t.Fatal("local CodeBuddy auth accepted")
	}
	if err := s.SetRunnerRuntimeAuthMode(runner.ID, "missing", domain.RuntimeAuthPersonal, now); err == nil {
		t.Fatal("missing runtime auth changed")
	}
	if err := s.SetRunnerRuntimeStatus(runner.ID, "codex", "bad", now); err == nil {
		t.Fatal("invalid runtime status accepted")
	}
	if err := s.SetRunnerRuntimeStatus("missing", "codex", domain.RuntimeStatusDisabled, now); !errors.Is(err, ErrRunnerNotFound) {
		t.Fatalf("missing runner status err=%v", err)
	}
	if err := s.SetRunnerRuntimeStatus(runner.ID, "missing", domain.RuntimeStatusDisabled, now); err == nil {
		t.Fatal("missing runtime status changed")
	}
	if err := s.SetRunnerRuntimeStatus(runner.ID, "codex", domain.RuntimeStatusDisabled, now); err != nil {
		t.Fatal(err)
	}
	if err := s.RunnerHeartbeat(runner.ID, []domain.RunnerRuntime{{Provider: "codex"}, {Provider: "claude-code"}, {Provider: "codebuddy"}}, " 1.2.3 ", false, now.Add(time.Minute), expires); err != nil {
		t.Fatal(err)
	}
	heartbeat, _ := s.Runner(runner.ID)
	if heartbeat.Runtimes[0].Status != domain.RuntimeStatusDisabled || heartbeat.Runtimes[1].AuthMode != domain.RuntimeAuthLocal || heartbeat.Runtimes[2].AuthMode != domain.RuntimeAuthPersonal || heartbeat.RunnerVersion != "1.2.3" {
		t.Fatalf("heartbeat=%+v", heartbeat)
	}
	if err := s.MarkRunnerOffline("", now); !errors.Is(err, ErrRunnerNotFound) {
		t.Fatalf("empty offline err=%v", err)
	}
	if err := s.MarkRunnerOffline("missing", now); !errors.Is(err, ErrRunnerNotFound) {
		t.Fatalf("missing offline err=%v", err)
	}
	if err := s.MarkRunnerOffline(runner.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefaultRunner(0, runner.ID, now); err == nil {
		t.Fatal("zero user preference accepted")
	}
	if err := s.SetDefaultRunner(12, "missing", now); !errors.Is(err, ErrRunnerNotFound) {
		t.Fatalf("missing default runner err=%v", err)
	}
	if err := s.SetDefaultRunner(12, runner.ID, now); err != nil || s.DefaultRunner(12) != runner.ID {
		t.Fatalf("default runner=%q err=%v", s.DefaultRunner(12), err)
	}
	if err := s.SetDefaultRunner(12, "", now); err != nil || s.DefaultRunner(12) != "" {
		t.Fatalf("cleared default=%q err=%v", s.DefaultRunner(12), err)
	}
}

func TestRunnerTaskLeaseEventsExpiryCancellationAndDeregister(t *testing.T) {
	s := cvEStore(t)
	now := time.Now().UTC()
	runner := domain.Runner{ID: "cve-task-runner", Status: domain.RunnerStatusOnline, Runtimes: []domain.RunnerRuntime{{Provider: "codex", Status: domain.RuntimeStatusOnline}}}
	if err := s.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRunnerTask(domain.RunnerTask{}); err == nil {
		t.Fatal("empty task accepted")
	}
	if err := s.PutRunnerTask(domain.RunnerTask{ID: "no-provider"}); err == nil {
		t.Fatal("providerless task accepted")
	}
	task := domain.RunnerTask{
		ID: "cve-task", RunnerID: runner.ID, Provider: "codex", RunID: "run-one", MaxAttempts: 2,
		Invocation: &domain.RunnerInvocation{
			CodexAPIKey:  "secret",
			Repository:   &domain.RunnerRepository{GitToken: "git"},
			Repositories: []domain.RunnerRepository{{GitToken: "git-two"}},
		},
	}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	if available, err := s.RunnerTaskAvailable("", []string{"codex"}, false); err != nil || available {
		t.Fatalf("empty availability=%v err=%v", available, err)
	}
	if available, err := s.RunnerTaskAvailable(runner.ID, []string{" "}, false); err != nil || available {
		t.Fatalf("blank availability=%v err=%v", available, err)
	}
	if available, err := s.RunnerTaskAvailable(runner.ID, []string{"codex"}, false); err != nil || !available {
		t.Fatalf("availability=%v err=%v", available, err)
	}
	if _, ok, err := s.ClaimRunnerTask("", []string{"codex"}, false, now, time.Minute); err == nil || ok {
		t.Fatalf("empty claim ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.ClaimRunnerTask(runner.ID, nil, false, now, time.Minute); err != nil || ok {
		t.Fatalf("providerless claim ok=%v err=%v", ok, err)
	}
	claimed, ok, err := s.ClaimRunnerTask(runner.ID, []string{"codex"}, false, now, -time.Second)
	if err != nil || !ok || claimed.Status != domain.RunnerTaskLeased || claimed.Attempt != 1 {
		t.Fatalf("claimed=%+v ok=%v err=%v", claimed, ok, err)
	}
	if err := s.StartRunnerTask(task.ID, "wrong", now); !errors.Is(err, ErrRunnerTaskLeaseLost) {
		t.Fatalf("wrong start err=%v", err)
	}
	if err := s.StartRunnerTask(task.ID, runner.ID, now); err != nil {
		t.Fatal(err)
	}
	if desired, err := s.RenewRunnerTaskLease(task.ID, "wrong", time.Minute, now); !errors.Is(err, ErrRunnerTaskLeaseLost) || desired != "" {
		t.Fatalf("wrong renew desired=%q err=%v", desired, err)
	}
	if err := s.CancelRunnerTask(task.ID, now); err != nil {
		t.Fatal(err)
	}
	if desired, err := s.RenewRunnerTaskLease(task.ID, runner.ID, time.Minute, now); err != nil || desired != domain.RunnerTaskDesiredCancel {
		t.Fatalf("cancel desired=%q err=%v", desired, err)
	}
	if control, ok := s.RunnerTaskControl(task.ID); !ok || control.DesiredState != domain.RunnerTaskDesiredCancel {
		t.Fatalf("control=%+v ok=%v", control, ok)
	}
	if err := s.CompleteRunnerTask(task.ID, runner.ID, "invalid", nil, "", "", nil, nil, now); err == nil {
		t.Fatal("invalid completion accepted")
	}
	exit := 0
	outputs := &domain.RunnerTaskOutputs{Files: map[string]string{"result.md": "ok"}}
	if err := s.CompleteRunnerTask(task.ID, runner.ID, domain.RunnerTaskSuccess, &exit, "done", "", map[string]string{"result": "body"}, outputs, now); err != nil {
		t.Fatal(err)
	}
	completed, ok := s.RunnerTask(task.ID)
	if !ok || completed.Status != domain.RunnerTaskSuccess || completed.Invocation.CodexAPIKey != "" || completed.Invocation.Repository.GitToken != "" || completed.Invocation.Repositories[0].GitToken != "" {
		t.Fatalf("completed=%+v ok=%v", completed, ok)
	}
	if err := s.CompleteRunnerTask(task.ID, runner.ID, domain.RunnerTaskSuccess, nil, "", "", nil, nil, now); !errors.Is(err, ErrRunnerTaskLeaseLost) {
		t.Fatalf("double completion err=%v", err)
	}

	if accepted, err := s.AppendRunnerTaskEvents("", []domain.RunnerTaskEvent{{Seq: 1}}); err == nil || accepted != 0 {
		t.Fatalf("empty events accepted=%d err=%v", accepted, err)
	}
	if accepted, err := s.AppendRunnerTaskEvents(task.ID, nil); err != nil || accepted != 0 {
		t.Fatalf("empty batch accepted=%d err=%v", accepted, err)
	}
	events := []domain.RunnerTaskEvent{{Seq: 1, Type: "stdout", Data: "one", Ts: now}, {Seq: 2, Type: "stderr", Data: "two", Ts: now}}
	if accepted, err := s.AppendRunnerTaskEvents(task.ID, events); err != nil || accepted != 2 {
		t.Fatalf("events accepted=%d err=%v", accepted, err)
	}
	if accepted, err := s.AppendRunnerTaskEvents(task.ID, events); err != nil || accepted != 0 {
		t.Fatalf("duplicate events accepted=%d err=%v", accepted, err)
	}
	if got := s.RunnerTaskEvents(task.ID); len(got) != 2 {
		t.Fatalf("events=%v", got)
	}
	if got := s.RunnerTaskEventsAfter(task.ID, 1); len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("events after=%v", got)
	}

	expiring := domain.RunnerTask{ID: "cve-expire", RunnerID: runner.ID, Provider: "codex", RunID: "run-two", MaxAttempts: 2}
	if err := s.PutRunnerTask(expiring); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimRunnerTask(runner.ID, []string{"codex"}, false, now.Add(time.Second), -time.Second); err != nil || !ok {
		t.Fatalf("first expiry claim ok=%v err=%v", ok, err)
	}
	if recovered, err := s.ExpireRunnerTasks(now.Add(2 * time.Second)); err != nil || len(recovered) != 1 || recovered[0].Status != domain.RunnerTaskQueued {
		t.Fatalf("first recovery=%+v err=%v", recovered, err)
	}
	if _, ok, err := s.ClaimRunnerTask(runner.ID, []string{"codex"}, false, now.Add(3*time.Second), -time.Second); err != nil || !ok {
		t.Fatalf("second expiry claim ok=%v err=%v", ok, err)
	}
	if recovered, err := s.ExpireRunnerTasks(now.Add(4 * time.Second)); err != nil || len(recovered) != 1 || recovered[0].Status != domain.RunnerTaskFailed {
		t.Fatalf("second recovery=%+v err=%v", recovered, err)
	}

	queued := domain.RunnerTask{ID: "cve-queued", RunnerID: runner.ID, Provider: "codex", RunID: "run-three", Invocation: &domain.RunnerInvocation{CodexAPIKey: "secret"}}
	if err := s.PutRunnerTask(queued); err != nil {
		t.Fatal(err)
	}
	if active, err := s.ActiveRunnerTasksForRun("run-three"); err != nil || len(active) != 1 {
		t.Fatalf("active=%v err=%v", active, err)
	}
	if ids, err := s.CancelRunnerTasksForRun("run-three", now); err != nil || len(ids) != 1 || ids[0] != queued.ID {
		t.Fatalf("canceled ids=%v err=%v", ids, err)
	}
	canceled, _ := s.RunnerTask(queued.ID)
	if canceled.Status != domain.RunnerTaskCanceled || canceled.Invocation.CodexAPIKey != "" {
		t.Fatalf("canceled=%+v", canceled)
	}
	if err := s.SetDefaultRunner(22, runner.ID, now); err != nil {
		t.Fatal(err)
	}
	deregisterTask := domain.RunnerTask{ID: "cve-deregister", RunnerID: runner.ID, Provider: "codex", Invocation: &domain.RunnerInvocation{CodexAPIKey: "secret"}}
	if err := s.PutRunnerTask(deregisterTask); err != nil {
		t.Fatal(err)
	}
	if err := s.DeregisterRunner("", now); err == nil {
		t.Fatal("empty deregister accepted")
	}
	if err := s.DeregisterRunner(runner.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Runner(runner.ID); ok || s.DefaultRunner(22) != "" {
		t.Fatal("deregister did not clear runner preference")
	}
	if err := s.DeregisterRunner(runner.ID, now); !errors.Is(err, ErrRunnerNotFound) {
		t.Fatalf("missing deregister err=%v", err)
	}
}
