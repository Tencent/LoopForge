package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func controlLeaseRun(t *testing.T, e *Engine, id string, saved bool) string {
	t.Helper()
	run := domain.Run{ID: id, Status: "failed", Generation: 1, ExecutionContext: domain.TelemetryContext{ProjectID: "p"}, WorkflowSnapshot: domain.WorkflowDefinition{ID: "control-lease", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "step", Type: "script", Script: "true", MaxAttempts: 1}}}, Nodes: map[string]*domain.NodeRun{"step": {NodeID: "step", Status: "failed", Attempt: 1}}}
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	token, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	if saved {
		root := e.RunWorkspace(id)
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "retained.txt"), []byte("saved work"), 0600); err != nil {
			t.Fatal(err)
		}
		archive, err := runtime.CreateWorkspaceCheckpoint(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.repo.SaveWorkspaceCheckpoint(id, token, time.Now().UTC(), archive); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
	}
	run, _ = e.repo.RunState(id)
	run.WorkspaceCleanedAt = time.Now().UTC()
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.EndConversationWorkspaceUse(context.Background(), id, token) })
	return token
}

func TestMainBotControlReusesRunWorkspaceLease(t *testing.T) {
	for _, saved := range []bool{false, true} {
		for _, op := range []string{"retry", "resume", "enable", "dispatch"} {
			name := op + "/legacy"
			if saved {
				name = op + "/snapshot"
			}
			t.Run(name, func(t *testing.T) {
				e, _ := newRecoveryEngine(t)
				e.ConfigureExecution("control", "control", time.Minute, false)
				id := "control-" + op
				token := controlLeaseRun(t, e, id, saved)
				ctx := WithConversationWorkspaceToken(context.Background(), token, id)
				run, _ := e.repo.RunState(id)
				if op == "resume" {
					run.Status, run.PauseRequested = "waiting", true
					run.Nodes["step"].Status = "waiting_approval"
					run.Nodes["step"].Activity = "已由用户暂停，继续后脚本将从头执行"
				}
				if op == "dispatch" {
					run.Status = "waiting"
					run.WorkflowSnapshot.CollaborationMode = "group"
					run.WorkflowSnapshot.GroupMembers = []string{"dev"}
					if err := e.repo.PutAgent(domain.AgentDefinition{ID: "dev", Name: "开发", ProjectID: "p", ExecutorID: "mock"}); err != nil {
						t.Fatal(err)
					}
				}
				if err := e.repo.PutRun(run); err != nil {
					t.Fatal(err)
				}
				if op == "resume" {
					if _, err := e.repo.RequestRunPause(id); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				switch op {
				case "retry":
					err = e.RetryContext(ctx, id, "step", "retry", "")
				case "resume":
					err = e.ResumePausedContext(ctx, id, "")
				case "enable":
					err = e.EnableTaskGroupContext(ctx, id)
				case "dispatch":
					_, err = e.DispatchGroupTaskContext(ctx, id, "", GroupTaskRequest{AgentID: "dev", Prompt: "检查文件", RequestID: "request"})
				}
				if err != nil {
					t.Fatal(err)
				}
				current, _ := e.repo.RunState(id)
				if op == "enable" && current.WorkflowSnapshot.CollaborationMode != "group" || op != "enable" && current.Status != "running" {
					t.Fatalf("control decision did not activate: %+v", current)
				}
				meta, _ := e.repo.WorkspaceCheckpoint(id)
				if meta.LeaseToken != token {
					t.Fatal("activation released or replaced the caller's lease")
				}
				if saved {
					body, err := os.ReadFile(filepath.Join(e.RunWorkspace(id), "retained.txt"))
					if err != nil || string(body) != "saved work" {
						t.Fatalf("saved file was not restored: %q, %v", body, err)
					}
				}
			})
		}
	}
}

func TestConversationWorkspaceLeaseRequiresMatchingRunScope(t *testing.T) {
	ctx := WithConversationWorkspaceToken(context.Background(), "parent-token", "parent")
	if ConversationWorkspaceToken(ctx, "parent") != "parent-token" || ConversationWorkspaceToken(ctx, "child") != "" {
		t.Fatal("workspace lease scope was not enforced")
	}
	if ConversationWorkspaceToken(WithConversationWorkspaceToken(context.Background(), "unscoped"), "parent") != "" {
		t.Fatal("legacy unscoped lease was reused for execution")
	}
}

func TestMainBotControlCannotBorrowAnotherRunOrWriterLease(t *testing.T) {
	for _, saved := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "snapshot"}[saved], func(t *testing.T) {
			e, _ := newRecoveryEngine(t)
			e.ConfigureExecution("control", "control", time.Minute, false)
			token := controlLeaseRun(t, e, "blocked", saved)
			for _, scope := range []string{"different-run", "blocked"} {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
				callerToken := token
				if scope == "blocked" {
					callerToken = "another-writer"
				}
				ctx = WithConversationWorkspaceToken(ctx, callerToken, scope)
				err := e.RetryContext(ctx, "blocked", "step", "retry", "")
				cancel()
				if !errors.Is(err, repo.ErrWorkspaceInUse) && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("other writer lease was bypassed: %v", err)
				}
				current, _ := e.repo.RunState("blocked")
				if current.Status != "failed" || current.WorkspaceCleanedAt.IsZero() {
					t.Fatal("failed restoration changed run state")
				}
			}
		})
	}
}

func TestResumeChildSOPDoesNotReuseParentWorkspaceLease(t *testing.T) {
	e, _ := newRecoveryEngine(t)
	e.ConfigureExecution("control", "control", time.Minute, false)
	token := controlLeaseRun(t, e, "child", true)
	child, _ := e.repo.RunState("child")
	child.Status, child.PauseRequested = "waiting", true
	child.Nodes["step"].Status, child.Nodes["step"].Activity = "waiting_approval", "已由用户暂停，继续后脚本将从头执行"
	if err := e.repo.PutRun(child); err != nil {
		t.Fatal(err)
	}
	if _, err := e.repo.RequestRunPause(child.ID); err != nil {
		t.Fatal(err)
	}
	parent := domain.Run{ID: "parent", Status: "waiting", PauseRequested: true, Nodes: map[string]*domain.NodeRun{"child-sop": {NodeID: "child-sop", Status: "waiting_approval", ChildRunID: child.ID, Activity: "已由用户暂停，等待补充信息"}}}
	if err := e.repo.PutRun(parent); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	// Even an accidentally matching token cannot cross the parent run boundary.
	ctx = WithConversationWorkspaceToken(ctx, token, parent.ID)
	err := e.ResumePausedContext(ctx, parent.ID, "")
	if err == nil || !strings.Contains(err.Error(), "继续子 SOP child") || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("child accepted parent's workspace lease: %v", err)
	}
	child, _ = e.repo.RunState(child.ID)
	if child.Status != "waiting" || !child.PauseRequested {
		t.Fatal("child SOP resumed despite another active workspace owner")
	}
}

func TestMainBotRetryExecutesAfterWorkspaceOwnerReleasesLease(t *testing.T) {
	for _, saved := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "snapshot"}[saved], func(t *testing.T) {
			e, _ := newRecoveryEngine(t)
			token := controlLeaseRun(t, e, "actual-retry", saved)
			run, _ := e.repo.RunState("actual-retry")
			run.WorkflowSnapshot.Nodes[0].Script = "printf 'resumed' > resumed.txt; cat resumed.txt"
			if err := e.repo.PutRun(run); err != nil {
				t.Fatal(err)
			}
			ctx := WithConversationWorkspaceToken(context.Background(), token, run.ID)
			if err := e.RetryContext(ctx, run.ID, "step", "retry", ""); err != nil {
				t.Fatal(err)
			}
			if !saved {
				// The execution worker has no chat context. It must wait for the
				// current writer rather than reject the reopened node or race it.
				time.Sleep(200 * time.Millisecond)
				current, _ := e.repo.RunState(run.ID)
				if current.Status != "running" || current.Nodes["step"].Status == "failed" {
					t.Fatalf("workspace contention failed the resumed script: %+v", current)
				}
				if _, err := os.Stat(filepath.Join(e.RunWorkspace(run.ID), "resumed.txt")); !os.IsNotExist(err) {
					t.Fatalf("script executed before workspace lease release: %v", err)
				}
			}
			if err := e.EndConversationWorkspaceUse(context.Background(), run.ID, token); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(8 * time.Second)
			for time.Now().Before(deadline) {
				current, _ := e.repo.RunState(run.ID)
				if current.Status == "succeeded" {
					if current.Nodes["step"].Attempt != 2 {
						t.Fatal("workspace wait consumed an extra script attempt")
					}
					if !strings.Contains(current.Nodes["step"].Output, "resumed") {
						t.Fatal("script did not read back the file it wrote")
					}
					return
				}
				if current.Status == "failed" || current.Status == "waiting" {
					t.Fatalf("resumed script did not execute: %+v", current.Nodes["step"])
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatal("resumed script did not complete")
		})
	}
}

func TestExecutionWorkspaceWaitStopsOnCancellation(t *testing.T) {
	e, _ := newRecoveryEngine(t)
	token := controlLeaseRun(t, e, "cancel-wait", false)
	run, _ := e.repo.RunState("cancel-wait")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	busy := make(chan struct{}, 1)
	go func() {
		done <- e.restoreCheckpointForExecution(ctx, run, func() { busy <- struct{}{} })
	}()
	select {
	case <-busy:
	case <-time.After(time.Second):
		t.Fatal("workspace contention was not observed")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation did not stop workspace wait: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled node startup kept waiting")
	}
	checkpoint, _ := e.repo.WorkspaceCheckpoint(run.ID)
	if checkpoint.LeaseToken != token {
		t.Fatal("waiting node stole the active writer's lease")
	}
}

func TestCancelResumedScriptWhileWorkspaceIsBusy(t *testing.T) {
	e, _ := newRecoveryEngine(t)
	token := controlLeaseRun(t, e, "cancel-running-wait", false)
	run, _ := e.repo.RunState("cancel-running-wait")
	run.WorkflowSnapshot.Nodes[0].Script = "printf 'should-not-run' > cancelled.txt"
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	ctx := WithConversationWorkspaceToken(context.Background(), token, run.ID)
	if err := e.RetryContext(ctx, run.ID, "step", "retry", ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := e.repo.RunState(run.ID)
		if current.Nodes["step"].Status == "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := e.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := e.repo.RunState(run.ID)
		if current.Status == "cancelled" && e.ActiveRunCount() == 0 {
			if current.Nodes["step"].Status == "failed" {
				t.Fatal("workspace wait was reported as script failure")
			}
			if _, err := os.Stat(filepath.Join(e.RunWorkspace(run.ID), "cancelled.txt")); !os.IsNotExist(err) {
				t.Fatalf("cancelled script executed: %v", err)
			}
			checkpoint, _ := e.repo.WorkspaceCheckpoint(run.ID)
			if checkpoint.LeaseToken != token {
				t.Fatal("cancelling the waiting script released another writer's lease")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("cancelled scheduler kept waiting for the workspace writer")
}

func TestMainBotResumeLegacyScriptCreatesUsableWorkspace(t *testing.T) {
	e, _ := newRecoveryEngine(t)
	token := controlLeaseRun(t, e, "actual-resume", false)
	run, _ := e.repo.RunState("actual-resume")
	previousCleanup := run.WorkspaceCleanedAt
	run.Status = "waiting"
	run.WorkflowSnapshot.Nodes[0].Script = "printf 'resumed-legacy' > resumed.txt; cat resumed.txt"
	run.Nodes["step"].Status = "waiting_approval"
	run.Nodes["step"].Activity = "已由用户暂停，继续后脚本将从头执行"
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if _, err := e.repo.RequestRunPause(run.ID); err != nil {
		t.Fatal(err)
	}
	ctx := WithConversationWorkspaceToken(context.Background(), token, run.ID)
	if err := e.ResumePausedContext(ctx, run.ID, ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := e.EndConversationWorkspaceUse(context.Background(), run.ID, token); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := e.repo.RunState(run.ID)
		if current.Status == "succeeded" && e.ActiveRunCount() == 0 {
			if !strings.Contains(current.Nodes["step"].Output, "resumed-legacy") {
				t.Fatal("script did not read back its output file")
			}
			// Completion may already have saved and reclaimed the new workspace.
			// Only the original stale marker is invalid; a fresh cleanup is valid.
			if current.WorkspaceCleanedAt.Equal(previousCleanup) {
				t.Fatal("new workspace retained the original cleanup marker")
			}
			useToken, err := e.BeginConversationWorkspaceUse(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := e.EnsureConversationWorkspace(context.Background(), run.ID, useToken); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(filepath.Join(e.RunWorkspace(run.ID), "resumed.txt"))
			if err != nil || string(content) != "resumed-legacy" {
				t.Fatalf("resumed workspace did not retain its real output: %q, %v", content, err)
			}
			if err := e.EndConversationWorkspaceUse(context.Background(), run.ID, useToken); err != nil {
				t.Fatal(err)
			}
			meta, _ := e.repo.WorkspaceCheckpoint(run.ID)
			if meta.SavedAt == nil {
				t.Fatal("new workspace was not available for checkpoint capture")
			}
			return
		}
		if current.Status == "failed" {
			t.Fatalf("resumed legacy script failed: %+v", current.Nodes["step"])
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("legacy resumed script did not complete")
}
