package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentNotificationsFollowRealGateAndApprovalEvents(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.PutProject(domain.Project{ID: "notify-p", Name: "通知项目"}); err != nil {
		t.Fatal(err)
	}
	bot := domain.NotificationBot{ID: "notify-bot", ProjectID: "notify-p", Name: "通知", BotID: "temporary", Enabled: true, Target: &domain.BotChat{ID: "chat", Name: "研发群", Type: "group"}}
	if err = s.SaveNotificationBot(bot, "fake-ciphertext"); err != nil {
		t.Fatal(err)
	}
	putGateTestAgent(t, s, "notify-agent", []domain.VerificationGate{{ID: "manual", Name: "人工确认", Mode: "manual", Stage: "after_execution", Required: true}})
	wf := domain.WorkflowDefinition{ID: "notify-flow", Name: "通知流程", ProjectID: "notify-p", SandboxProfileID: "local", Notifications: []domain.NotificationBinding{{BotID: bot.ID, Scope: "run", Events: []string{"succeeded"}, ContentMode: "ai"}}, Nodes: []domain.WorkflowNode{{ID: "agent", Name: "实现", Type: "agent", AgentID: "notify-agent", ExecutorID: "mock", MaxAttempts: 1}}}
	if err = s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	run, err := e.StartWithExecutionContext(wf.ID, "通知链路验证", domain.RepositoryContext{}, domain.TelemetryContext{ProjectID: "notify-p"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := s.RunState(run.ID)
		if current.Status == "waiting" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	items, err := s.NotificationDeliveries(bot.ProjectID)
	if err != nil || len(items) != 1 || items[0].EventType != "waiting" {
		t.Fatalf("gate event did not enqueue: %v %+v", err, items)
	}
	if err = e.Approve(run.ID, "agent", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "succeeded")
	items, err = s.NotificationDeliveries(bot.ProjectID)
	if err != nil || len(items) != 2 {
		t.Fatalf("approval did not enqueue completion: %v %+v", err, items)
	}
	kinds := map[string]bool{}
	for _, item := range items {
		kinds[item.EventType] = true
	}
	if !kinds["waiting"] || !kinds["succeeded"] {
		t.Fatalf("wrong events %+v", items)
	}
}
