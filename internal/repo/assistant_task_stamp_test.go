package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestStampAssistantTaskNewTaskDefaults(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	got := stampAssistantTask(domain.AssistantTask{ID: "t1", UserID: 99}, domain.AssistantTask{}, 7, now)
	if !got.CreatedAt.Equal(now) || !got.UpdatedAt.Equal(now) {
		t.Fatalf("times = %v / %v, want %v", got.CreatedAt, got.UpdatedAt, now)
	}
	if got.UserID != 7 {
		t.Fatalf("user = %d, want 7", got.UserID)
	}
	if got.NotificationSnapshot != nil {
		t.Fatalf("snapshot = %#v, want nil", got.NotificationSnapshot)
	}
}

func TestStampAssistantTaskKeepsCallerTimesForNewTask(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	created := now.Add(-time.Hour)
	updated := now.Add(-time.Minute)
	got := stampAssistantTask(domain.AssistantTask{ID: "t1", CreatedAt: created, UpdatedAt: updated}, domain.AssistantTask{}, 7, now)
	if !got.CreatedAt.Equal(created) || !got.UpdatedAt.Equal(updated) {
		t.Fatalf("times = %v / %v, want %v / %v", got.CreatedAt, got.UpdatedAt, created, updated)
	}
}

func TestStampAssistantTaskInheritsPriorCreatedAtAndSnapshot(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	priorCreated := now.Add(-24 * time.Hour)
	prior := domain.AssistantTask{
		ID:                   "t1",
		CreatedAt:            priorCreated,
		LastStatus:           "ok",
		NotificationSnapshot: []domain.NotificationBinding{{BotID: "old", Events: []string{"succeeded"}}},
	}
	v := domain.AssistantTask{
		ID:            "t1",
		CreatedAt:     now,
		LastStatus:    "ok",
		Notifications: []domain.NotificationBinding{{BotID: "new"}},
	}
	got := stampAssistantTask(v, prior, 7, now)
	if !got.CreatedAt.Equal(priorCreated) {
		t.Fatalf("created = %v, want prior %v", got.CreatedAt, priorCreated)
	}
	if len(got.NotificationSnapshot) != 1 || got.NotificationSnapshot[0].BotID != "old" {
		t.Fatalf("snapshot = %#v, want prior snapshot", got.NotificationSnapshot)
	}
	got.NotificationSnapshot[0].Events[0] = "mutated"
	if prior.NotificationSnapshot[0].Events[0] != "succeeded" {
		t.Fatal("snapshot must be cloned, not shared with prior")
	}
}

func TestStampAssistantTaskRefreshesSnapshotWhenRunStarts(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	runAt := now.Add(-time.Second)
	bindings := []domain.NotificationBinding{{BotID: "new"}}
	priorSnapshot := []domain.NotificationBinding{{BotID: "old"}}
	cases := []struct {
		name  string
		prior domain.AssistantTask
		want  string
	}{
		{"idle to running", domain.AssistantTask{ID: "t1", LastStatus: "ok", NotificationSnapshot: priorSnapshot}, "new"},
		{"new run while running", domain.AssistantTask{ID: "t1", LastStatus: "running", LastRunAt: runAt.Add(-time.Minute), NotificationSnapshot: priorSnapshot}, "new"},
		{"same run still running", domain.AssistantTask{ID: "t1", LastStatus: "running", LastRunAt: runAt, NotificationSnapshot: priorSnapshot}, "old"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := domain.AssistantTask{ID: "t1", LastStatus: "running", LastRunAt: runAt, Notifications: bindings}
			got := stampAssistantTask(v, tc.prior, 7, now)
			if len(got.NotificationSnapshot) != 1 || got.NotificationSnapshot[0].BotID != tc.want {
				t.Fatalf("snapshot = %#v, want bot %q", got.NotificationSnapshot, tc.want)
			}
		})
	}
}

func TestAssistantTaskCompletion(t *testing.T) {
	runAt := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	running := domain.AssistantTask{ID: "t1", LastStatus: "running"}
	cases := []struct {
		name        string
		prior       domain.AssistantTask
		v           domain.AssistantTask
		wantOK      bool
		wantTrigger string
		wantResult  string
	}{
		{"succeeded uses notification result", running, domain.AssistantTask{LastStatus: "ok", LastRunAt: runAt, NotificationResult: "summary", LastMessage: "msg"}, true, "succeeded", "summary"},
		{"failed falls back to last message", running, domain.AssistantTask{LastStatus: "error", LastRunAt: runAt, LastMessage: "msg"}, true, "failed", "msg"},
		{"new task", domain.AssistantTask{}, domain.AssistantTask{LastStatus: "ok", LastRunAt: runAt}, false, "", ""},
		{"prior not running", domain.AssistantTask{ID: "t1", LastStatus: "ok"}, domain.AssistantTask{LastStatus: "ok", LastRunAt: runAt}, false, "", ""},
		{"still running", running, domain.AssistantTask{LastStatus: "running", LastRunAt: runAt}, false, "", ""},
		{"unknown status", running, domain.AssistantTask{LastStatus: "skipped", LastRunAt: runAt}, false, "", ""},
		{"missing run time", running, domain.AssistantTask{LastStatus: "ok"}, false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			trigger, result, ok := assistantTaskCompletion(tc.prior, tc.v)
			if ok != tc.wantOK || trigger != tc.wantTrigger || result != tc.wantResult {
				t.Fatalf("got (%q, %q, %v), want (%q, %q, %v)", trigger, result, ok, tc.wantTrigger, tc.wantResult, tc.wantOK)
			}
		})
	}
}
