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

func TestScheduledAssistantReceives180MinuteDeadline(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "schedule-owner", CodeBuddyAPIKey: "test-key"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("schedule-owner")
	called := false
	stopped := errors.New("stop test generation")
	h := &assistantHandlers{deps: &deps{repo: st}, generate: func(ctx context.Context, _ string, _ domain.User) (string, error) {
		called = true
		deadline, ok := ctx.Deadline()
		remaining := time.Until(deadline)
		if !ok || remaining > 180*time.Minute || remaining < 179*time.Minute {
			t.Fatalf("scheduled generation deadline: ok=%v remaining=%s", ok, remaining)
		}
		return "", stopped
	}}
	if _, err := h.prepareScheduleJob(domain.ScheduleJob{OwnerUserID: user.ID, AssistantPrompt: "inspect"}); err == nil || !called {
		t.Fatalf("generation was not reached: called=%v err=%v", called, err)
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
			if err := st.PutIssue(domain.Issue{ID: "scheduled-issue", ProjectID: "platform", Title: "keep", Source: "tapd", Status: "backlog"}); err != nil {
				t.Fatal(err)
			}
			api := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
			h := &assistantHandlers{deps: &deps{repo: st}, api: api}
			called := false
			h.generate = func(ctx context.Context, prompt string, u domain.User) (string, error) {
				called = true
				if !strings.Contains(prompt, "当前审批模式为「自动执行」") {
					t.Fatal("scheduled prompt must explicitly enable auto execution")
				}
				for _, forbidden := range []string{"当前审批模式为「每次确认」", "平台写入工具不可直接调用", "供用户确认", "每轮 1 个"} {
					if strings.Contains(prompt, forbidden) {
						t.Fatalf("scheduled prompt contains confirmation instruction %q", forbidden)
					}
				}
				mode := assistantExecModeFrom(ctx)
				if !mode.AutoWrite || mode.DeferSOP != scheduleJob {
					t.Fatalf("unexpected scheduled gateway mode: %+v", mode)
				}
				if scheduleJob && !strings.Contains(prompt, "不要调用 issues_dispatch") {
					t.Fatal("schedule job must keep SOP dispatch restriction")
				}
				if _, err := h.callAssistantPlatform(ctx, u, "platform", assistantMCPCall{
					Tool: "issues_save", Body: map[string]any{"id": "scheduled-issue", "automationState": "ignored"},
				}); err != nil {
					t.Fatalf("scheduled write should execute immediately: %v", err)
				}
				if scheduleJob {
					if _, err := h.callAssistantPlatform(ctx, u, "platform", assistantMCPCall{Tool: "issues_dispatch", Path: map[string]any{"id": "scheduled-issue"}}); err == nil {
						t.Fatal("AI schedule job must still reject SOP dispatch")
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
