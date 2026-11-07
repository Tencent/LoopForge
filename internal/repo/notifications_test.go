package repo

import (
	"agentflow/internal/domain"
	"fmt"
	"gorm.io/gorm"
	"os"
	"sync"
	"testing"
	"time"
)

func notificationFixture(t *testing.T, s *Store, suffix string) (domain.NotificationBot, domain.Run) {
	t.Helper()
	bot := domain.NotificationBot{ID: "bot-" + suffix, ProjectID: "project-" + suffix, Name: "通知 Bot", BotID: "wecom-test", Enabled: true, SecretConfigured: true, Target: &domain.BotChat{ID: "chat-test", Name: "研发群", Type: "group"}}
	if err := s.PutProject(domain.Project{ID: bot.ProjectID, Name: "研发项目"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveNotificationBot(bot, "ciphertext-not-a-real-secret"); err != nil {
		t.Fatal(err)
	}
	node := domain.WorkflowNode{ID: "agent", Name: "工程师", Type: "agent", Notifications: []domain.NodeNotification{{BotID: bot.ID, Events: []string{"succeeded", "failed", "waiting"}}}}
	run := domain.Run{ID: "run-" + suffix, WorkflowID: "wf-" + suffix, Status: "running", ExecutionContext: domain.TelemetryContext{ProjectID: bot.ProjectID}, WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf-" + suffix, ProjectID: bot.ProjectID, Name: "交付流程", Nodes: []domain.WorkflowNode{node}}, Nodes: map[string]*domain.NodeRun{"agent": {NodeID: "agent", Status: "succeeded", Attempt: 1}}, Events: []domain.RunEvent{{ID: "event-" + suffix, NodeID: "agent", Type: "node.failed", Timestamp: time.Now().UTC()}}}
	return bot, run
}
func TestNotificationOutboxAtomicAndIdempotent(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bot, run := notificationFixture(t, s, "atomic")
	// An invalid persisted Bot forces enqueue failure, rolling back the run event and header.
	if err = s.DB.Model(&notificationBotRow{}).Where("id = ?", bot.ID).Update("payload", []byte(`not-json`)).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.PutRun(run); err == nil {
		t.Fatal("expected rollback")
	}
	if _, ok := s.RunState(run.ID); ok {
		t.Fatal("run committed despite outbox failure")
	}
	if err = s.SaveNotificationBot(bot, ""); err == nil {
		t.Fatal("corrupt bot should not be silently overwritten")
	}
	if err = s.DB.Where("id = ?", bot.ID).Delete(&notificationBotRow{}).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.SaveNotificationBot(bot, ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.NotificationDeliveries(bot.ProjectID)
	if err != nil || len(items) != 1 {
		t.Fatalf("expected one delivery: %v %+v", err, items)
	}
	// Cross-project references in a snapshot must never enqueue messages.
	other := run
	other.ID = "other-run"
	other.ExecutionContext.ProjectID = "another-project"
	if err = s.PutRun(other); err != nil {
		t.Fatal(err)
	}
	items, _ = s.NotificationDeliveries(bot.ProjectID)
	if len(items) != 1 {
		t.Fatal("cross-project enqueue")
	}
	// A stale lease owner cannot complete a reclaimed job.
	first, err := s.ClaimNotification(time.Now().UTC(), "owner-1")
	if err != nil || first == nil {
		t.Fatal(err)
	}
	if row, err := s.ClaimNotification(time.Now().UTC(), "owner-2"); err != nil || row != nil {
		t.Fatalf("concurrent claimant got row: %v", err)
	}
	if err = s.DB.Model(first).Update("lease_until", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimNotification(time.Now().UTC(), "owner-2")
	if err != nil || second == nil {
		t.Fatal(err)
	}
	if err = s.FinishNotification(*first, "sent", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	items, _ = s.NotificationDeliveries(bot.ProjectID)
	if items[0].Status != "sending" {
		t.Fatal("stale owner completed job")
	}
	if err = s.FinishNotification(*second, "sent", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	items, _ = s.NotificationDeliveries(bot.ProjectID)
	if items[0].Status != "sent" {
		t.Fatal("current owner could not finish")
	}
}
func TestWorkflowNotificationValidation(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, run := notificationFixture(t, s, "validation")
	w := run.WorkflowSnapshot
	if err = s.PutWorkflow(w); err != nil {
		t.Fatal(err)
	}
	w, _ = s.Workflow(w.ID)
	w.Nodes[0].Notifications[0].Template = "{{secret}}"
	if err = s.PutWorkflow(w); err == nil {
		t.Fatal("unknown template variable accepted")
	}
	w.Nodes[0].Notifications[0].Template = ""
	w.ProjectID = "foreign"
	if err = s.PutWorkflow(w); err == nil {
		t.Fatal("cross-project bot accepted")
	}
	w.ProjectID = run.ExecutionContext.ProjectID
	w.Shared = true
	if err = s.PutWorkflow(w); err == nil {
		t.Fatal("project bot leaked into shared workflow")
	}
}
func TestMySQLNotificationConcurrentClaims(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("local MySQL DSN not set")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bot, run := notificationFixture(t, s, fmt.Sprint(time.Now().UnixNano()))
	t.Cleanup(func() {
		_ = s.DeleteNotificationBot(bot.ID)
		_ = s.DB.Where("project_id = ?", bot.ProjectID).Delete(&domain.NotificationDelivery{}).Error
	})
	if err = s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	count := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			row, err := s.ClaimNotification(time.Now().UTC(), fmt.Sprint(i))
			if err != nil {
				t.Errorf("claim: %v", err)
			}
			if row != nil {
				mu.Lock()
				count++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if count != 1 {
		t.Fatalf("got %d claimants", count)
	}
}

// A failed enclosing transaction must roll back outbox insertion even after a valid enqueue.
func TestNotificationEnqueueRollback(t *testing.T) {
	s, _ := OpenTest()
	defer s.Close()
	bot, run := notificationFixture(t, s, "rollback")
	_ = s.DB.Transaction(func(tx *gorm.DB) error {
		if err := s.NotificationsRepo.enqueueRunNotifications(tx, run, run.Events[0], 0); err != nil {
			t.Fatal(err)
		}
		return fmt.Errorf("abort")
	})
	items, _ := s.NotificationDeliveries(bot.ProjectID)
	if len(items) != 0 {
		t.Fatal("outbox escaped transaction")
	}
}

func TestNotificationHumanDecisionEvents(t *testing.T) {
	for _, tc := range []struct{ kind, trigger string }{
		{"agent.turns.declined", "failed"}, {"agent.turns.exhausted", "waiting"}, {"agent.loop.exhausted", "waiting"}, {"node.paused", "waiting"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			s, err := OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			bot, run := notificationFixture(t, s, tc.kind)
			run.Events[0].Type = tc.kind
			if err = s.PutRun(run); err != nil {
				t.Fatal(err)
			}
			items, err := s.NotificationDeliveries(bot.ProjectID)
			if err != nil || len(items) != 1 || items[0].EventType != tc.trigger {
				t.Fatalf("event not covered: %v %+v", err, items)
			}
		})
	}
}
func TestDisabledBotDoesNotPreventEditingItsWorkflow(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bot, run := notificationFixture(t, s, "disabled-flow")
	bot.Enabled = false
	if err = s.SaveNotificationBot(bot, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.PutWorkflow(run.WorkflowSnapshot); err != nil {
		t.Fatal(err)
	}
	if err = s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	items, _ := s.NotificationDeliveries(bot.ProjectID)
	if len(items) != 0 {
		t.Fatal("disabled bot enqueued notification")
	}
}
