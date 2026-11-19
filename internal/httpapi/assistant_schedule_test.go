package httpapi

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestScheduledAssistantDeadline(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "schedule-owner", CodeBuddyAPIKey: "test-key"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("schedule-owner")
	for _, tc := range []struct {
		name    string
		minutes int
		want    time.Duration
	}{
		{"existing job", 0, 30 * time.Minute},
		{"configured limit", 5, 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h := &assistantHandlers{deps: &deps{repo: st}, generate: func(ctx context.Context, _ string, _ domain.User) (string, error) {
				called = true
				deadline, ok := ctx.Deadline()
				if remaining := time.Until(deadline); !ok || remaining > tc.want || remaining < tc.want-10*time.Second {
					t.Errorf("scheduled generation deadline = %s remaining, want %s", remaining, tc.want)
				}
				return "定时巡检已完成", nil
			}}
			if _, err := h.prepareScheduleJob(domain.ScheduleJob{OwnerUserID: user.ID, AssistantPrompt: "inspect", TimeoutMinutes: tc.minutes}); err != nil || !called {
				t.Fatalf("generation did not complete: called=%v err=%v", called, err)
			}
		})
	}
}

func TestScheduledAssistantStopsAtDeadline(t *testing.T) {
	h, user := workbenchFixture(t)
	started := make(chan struct{})
	h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}
	task := assistantTask{ProjectID: "workbench", Name: "Timeout", Prompt: "wait"}
	ctx := withScheduledSessionPolicy(context.Background(), scheduledSessionPolicy{Timeout: time.Second})
	if err := h.executeAssistantTask(ctx, user, &task); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("scheduled generation error = %v, want DeadlineExceeded", err)
	}
	select {
	case <-started:
	default:
		t.Fatal("deadline elapsed before the turn started")
	}
	if task.LastStatus != "error" || task.LastError != "定时任务执行超时" || task.SessionID == "" {
		t.Fatalf("unexpected task outcome: %+v", task)
	}
	generation, ok := h.repo.PeekAssistantGeneration(user.ID, task.SessionID)
	if !ok || generation.Status != "failed" || generation.Failure == nil || generation.Failure.Code != "EXECUTION_TIMEOUT" {
		t.Fatalf("unexpected generation outcome: %+v", generation)
	}
}

func TestScheduledAssistantRecoveredDraftDoesNotDispatchRun(t *testing.T) {
	h, user := workbenchFixture(t)
	wf := domain.WorkflowDefinition{ID: "wf", Name: "Scheduled approval", ProjectID: "workbench", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "review", Type: "approval"}}}
	if err := h.repo.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	h.engine = engine.New(h.repo, runtime.NewManager(t.TempDir()))
	h.api = New(h.repo, h.engine, tracker.NewService(), "", "")
	h.generate = func(context.Context, string, domain.User) (string, error) {
		return `{"message":"启动并等待","drafts":[{"tool":"runs_start","body":{"workflowId":"wf"}}]}`, nil
	}
	task := assistantTask{ProjectID: "workbench", Name: "Wait for SOP", Prompt: "启动 SOP 并等待终态"}
	err := h.executeAssistantTask(context.Background(), user, &task)
	if err == nil || !strings.Contains(err.Error(), "agentflow__runs_start") || task.LastStatus != "error" {
		t.Fatalf("scheduled draft fallback: status=%q err=%v", task.LastStatus, err)
	}
	if runs := h.repo.Runs(); len(runs) != 0 {
		t.Fatalf("scheduled draft fallback dispatched %d runs", len(runs))
	}
}

func TestScheduledAssistantAutoWritesWithoutConfirmation(t *testing.T) {
	for _, scheduleJob := range []bool{false, true} {
		name := "assistant_task"
		if scheduleJob {
			name = "schedule_job"
		}
		t.Run(name, func(t *testing.T) {
			st, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			if err := st.PutUser(domain.User{LoginName: "auto-owner", CodeBuddyAPIKey: "test-key"}); err != nil {
				t.Fatal(err)
			}
			user, _ := st.UserByLoginName("auto-owner")
			joinScheduledProject(t, st, "platform", user.ID)
			if err := st.PutIssue(domain.Issue{ID: "scheduled-issue", ProjectID: "platform", Title: "keep", Source: "tapd", Status: "backlog"}); err != nil {
				t.Fatal(err)
			}
			api := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
			h := &assistantHandlers{deps: &deps{repo: st}, api: api}
			called := false
			h.generate = func(ctx context.Context, prompt string, u domain.User) (string, error) {
				called = true
				if !strings.Contains(prompt, "当前审批模式为「完全权限」") {
					t.Fatal("scheduled prompt must explicitly enable auto execution")
				}
				for _, forbidden := range []string{"当前审批模式为「每次确认」", "平台写入工具不可直接调用", "供用户确认", "每轮 1 个"} {
					if strings.Contains(prompt, forbidden) {
						t.Fatalf("scheduled prompt contains confirmation instruction %q", forbidden)
					}
				}
				mode := assistantExecModeFrom(ctx)
				if mode != assistantExecModeForApproval(AssistantApprovalAuto) {
					t.Fatalf("unexpected scheduled gateway mode: %+v", mode)
				}
				if strings.Contains(prompt, "不要调用 issues_dispatch") || strings.Contains(prompt, "不要写进 AI 定时任务") {
					t.Fatal("scheduled prompts must allow SOP execution")
				}
				if _, err := h.callAssistantPlatform(ctx, u, "platform", assistantMCPCall{
					Tool: "issues_save", Body: map[string]any{"id": "scheduled-issue", "automationState": "ignored"},
				}); err != nil {
					t.Fatalf("scheduled write should execute immediately: %v", err)
				}
				for _, name := range []string{"issues_dispatch", "runs_start", "runs_retry", "workflows_publish", "schedule_jobs_save", "schedule_jobs_run", "schedule_jobs_delete"} {
					if !assistantPlatformToolAllowed(mode, name) {
						t.Fatalf("full-access scheduled task must allow %s", name)
					}
				}
				return "已完成写入", nil
			}
			if scheduleJob {
				if _, err := h.prepareScheduleJob(domain.ScheduleJob{ProjectID: "platform", OwnerUserID: user.ID, AssistantPrompt: "将需求标记为 ignored"}); err != nil {
					t.Fatal(err)
				}
			} else {
				task := assistantTask{ProjectID: "platform", Prompt: "将需求标记为 ignored"}
				if err := h.executeAssistantTask(context.Background(), user, &task); err != nil {
					t.Fatal(err)
				}
				if task.LastStatus != "ok" {
					t.Fatalf("scheduled task status: %+v", task)
				}
			}
			got, _ := st.Issue("scheduled-issue")
			if !called || got.AutomationState != "ignored" || got.Title != "keep" {
				t.Fatalf("scheduled write did not persist: called=%v issue=%+v", called, got)
			}
		})
	}
}
