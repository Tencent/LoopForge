package httpapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/schedule"
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
	wf := domain.WorkflowDefinition{ID: "wf", Name: "Scheduled approval", ProjectID: "workbench", SandboxProfileID: repo.TestNativeSandboxID, Nodes: []domain.WorkflowNode{{ID: "review", Type: "approval"}}}
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

func TestScheduledUsageStaysPerExecution(t *testing.T) {
	h, user := workbenchFixture(t)
	bin := filepath.Join(t.TempDir(), "codebuddy")
	counter := filepath.Join(t.TempDir(), "calls")
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	job := domain.ScheduleJob{ID: "usage-job", ProjectID: "workbench", OwnerUserID: user.ID, RunAsUserID: user.ID, Name: "巡检", SkipDispatch: true, AssistantPrompt: "检查文件", Enabled: true, SessionMode: domain.ScheduleSessionFixed}
	if err := h.repo.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	svc := schedule.New(h.repo, tracker.NewService(), nil)
	svc.SetAssistantSessionPrep(h.runScheduleJobPromptResult)
	var sessionID, firstID string
	for _, tc := range []struct {
		name, message string
		input         int64
		failed        bool
		unreported    bool
	}{
		{name: "first", message: "检查结果正常，没有问题。", input: 10},
		{name: "fixed_second", message: "检查结果正常，没有问题。", input: 10},
		{name: "failed_retries", message: "我先查看一下文件。", input: 30, failed: true},
		{name: "unreported", message: "检查结果正常，没有问题。", unreported: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Legacy CodeBuddy reports session totals, even when a new
			// scheduled execution or protocol retry resumes that session.
			script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo 2.185.0; exit 0; fi
n=0
if [ -f '` + counter + `' ]; then read -r n < '` + counter + `'; fi
n=$((n + 1))
echo "$n" > '` + counter + `'
printf '{"type":"result","result":"%s","usage":{"input_tokens":%s,"output_tokens":%s,"cache_read_input_tokens":%s,"cache_creation_input_tokens":%s}}\n' '` + tc.message + `' "$((n * 10))" "$((n * 5))" "$((n * 100))" "$((n * 3))"
`
			if tc.unreported {
				script = "#!/bin/sh\necho '{\"type\":\"result\",\"result\":\"" + tc.message + "\"}'\n"
			}
			if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			out, err := svc.RunNow(job.ID)
			if (err != nil) != tc.failed {
				t.Fatalf("outcome=%+v err=%v", out, err)
			}
			e, ok := h.repo.ScheduleJobExecution(domain.ScheduleJobExecutionID(out.LastTraceID))
			if !ok || e.SessionID == "" || (!tc.unreported && e.Model == "") {
				t.Fatalf("execution=%+v", e)
			}
			if sessionID == "" {
				sessionID, firstID = e.SessionID, e.ID
			} else if e.SessionID != sessionID {
				t.Fatalf("fixed session changed: %s != %s", e.SessionID, sessionID)
			}
			if tc.unreported {
				if e.Usage != nil {
					t.Fatalf("unexpected usage=%+v", e.Usage)
				}
			} else if e.Usage == nil || e.Usage.InputTokens != tc.input || e.Usage.OutputTokens != tc.input/2 || e.Usage.CacheReadTokens != tc.input*10 || e.ExecutionTotalTokens() != tc.input*3/2 {
				t.Fatalf("usage=%+v", e.Usage)
			}
		})
	}
	first, _ := h.repo.ScheduleJobExecution(firstID)
	if first.Usage == nil || first.Usage.InputTokens != 10 {
		t.Fatalf("earlier usage changed: %+v", first)
	}
}
