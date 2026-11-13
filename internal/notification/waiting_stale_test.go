package notification

import (
	"context"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestWaitingNodeStale(t *testing.T) {
	item := domain.NotificationDelivery{NodeID: "n", Attempt: 2, ReworkCount: 1}
	waiting := func() *domain.NodeRun {
		return &domain.NodeRun{NodeID: "n", Status: "waiting_approval", Attempt: 2, ReworkCount: 1}
	}
	cases := []struct {
		name   string
		status string
		node   func() *domain.NodeRun
		want   bool
	}{
		{"still waiting", "waiting", waiting, false},
		{"terminal run", "succeeded", waiting, true},
		{"node missing", "waiting", func() *domain.NodeRun { return nil }, true},
		{"node no longer waiting", "running", func() *domain.NodeRun { n := waiting(); n.Status = "running"; return n }, true},
		{"newer attempt", "waiting", func() *domain.NodeRun { n := waiting(); n.Attempt = 3; return n }, true},
		{"newer rework", "waiting", func() *domain.NodeRun { n := waiting(); n.ReworkCount = 2; return n }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := domain.Run{Status: tc.status, Nodes: map[string]*domain.NodeRun{}}
			if node := tc.node(); node != nil {
				run.Nodes["n"] = node
			}
			if got := waitingNodeStale(run, item); got != tc.want {
				t.Fatalf("waitingNodeStale() = %v, want %v", got, tc.want)
			}
		})
	}
	if !waitingNodeStale(domain.Run{}, item) {
		t.Fatal("zero-value run (deleted) must be stale")
	}
}

// Delivery rows queued before waiting policies were standardized may still
// carry WaitingPolicy "event". Those record the transition itself, so they are
// delivered even after the wait ends, but must tell the reader it is over.
func TestLegacyEventPolicyWaitingReminderMarksEndedWait(t *testing.T) {
	const endedNote = "当前该次等待已结束"
	for _, tc := range []struct {
		name    string
		resolve bool
	}{
		{"still waiting", false},
		{"wait ended before send", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, f, bot, run := serviceFixture(t)
			if err := svc.Store.DB.Where("run_id = ?", run.ID).Delete(&domain.NotificationDelivery{}).Error; err != nil {
				t.Fatal(err)
			}
			if tc.resolve {
				run.Status = "succeeded"
				run.Nodes["n"].Status = "succeeded"
				if err := svc.Store.PutRun(run); err != nil {
					t.Fatal(err)
				}
				if err := svc.Store.DB.Where("run_id = ?", run.ID).Delete(&domain.NotificationDelivery{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now().UTC()
			legacy := domain.NotificationDelivery{
				ID: "legacy-event", ProjectID: bot.ProjectID, BotID: bot.ID, RunID: run.ID, NodeID: "n",
				SourceType: "run", SourceID: run.ID, EventType: "waiting", WaitingPolicy: "event",
				TargetID: "chat", TargetType: "group", TargetName: "研发群", ContentSource: "template",
				Content: "节点进入等待", Status: "pending", Attempt: 1, CreatedAt: now, NextAttemptAt: now,
			}
			if err := svc.Store.CreateNotificationDelivery(legacy); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			items, _ := svc.Store.NotificationDeliveries(bot.ProjectID)
			if f.sent != 1 || len(items) != 1 || items[0].Status != "sent" {
				t.Fatalf("legacy event reminder not delivered: sent=%d items=%+v", f.sent, items)
			}
			if got := strings.Contains(f.content, endedNote); got != tc.resolve {
				t.Fatalf("ended note present=%v, want %v; content=%q", got, tc.resolve, f.content)
			}
		})
	}
}
