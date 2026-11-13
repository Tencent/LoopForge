package repo

import (
	"agentflow/internal/domain"
	"testing"
)

func TestNotificationEventSelected(t *testing.T) {
	cases := []struct {
		name   string
		events []string
		event  string
		want   bool
	}{
		{"hit", []string{"succeeded", "failed"}, "failed", true},
		{"miss", []string{"succeeded"}, "waiting", false},
		{"nil events", nil, "succeeded", false},
		{"empty event not selected", []string{"succeeded"}, "", false},
		{"case sensitive", []string{"Succeeded"}, "succeeded", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			binding := domain.NotificationBinding{Events: tc.events}
			if got := notificationEventSelected(binding, tc.event); got != tc.want {
				t.Fatalf("notificationEventSelected(%v, %q) = %v, want %v", tc.events, tc.event, got, tc.want)
			}
		})
	}
}
