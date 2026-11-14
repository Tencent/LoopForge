package httpapi

import (
	"agentflow/internal/domain"
	"testing"
)

func TestNotificationBotModel(t *testing.T) {
	str := func(v string) *string { return &v }
	cases := []struct {
		name      string
		requested *string
		current   string
		want      string
		wantOK    bool
	}{
		{name: "no request keeps current", requested: nil, current: domain.DefaultNotificationReportModel, want: domain.DefaultNotificationReportModel, wantOK: true},
		{name: "no request and empty current falls back to default", requested: nil, current: "", want: domain.DefaultNotificationReportModel, wantOK: true},
		{name: "blank request means default", requested: str("  "), current: "", want: domain.DefaultNotificationReportModel, wantOK: true},
		{name: "known request is trimmed", requested: str(" " + domain.DefaultNotificationReportModel + " "), current: "", want: domain.DefaultNotificationReportModel, wantOK: true},
		{name: "unknown request is rejected", requested: str("not-a-real-model"), current: domain.DefaultNotificationReportModel, want: "", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := notificationBotModel(tc.requested, tc.current)
			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("notificationBotModel() = %q, %v; want %q, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestFindNotificationTarget(t *testing.T) {
	chats := []domain.BotChat{{ID: "a", Name: "A", Type: "group"}, {ID: "b", Name: "B", Type: "single"}}

	target, ok := findNotificationTarget(chats, "b")
	if !ok || target == nil || *target != chats[1] {
		t.Fatalf("findNotificationTarget(b) = %+v, %v; want %+v, true", target, ok, chats[1])
	}
	target.Name = "changed"
	if chats[1].Name != "B" {
		t.Fatalf("returned target must be a copy, source chat changed to %q", chats[1].Name)
	}

	if target, ok := findNotificationTarget(chats, "missing"); ok || target != nil {
		t.Fatalf("findNotificationTarget(missing) = %+v, %v; want nil, false", target, ok)
	}
	if target, ok := findNotificationTarget(nil, "a"); ok || target != nil {
		t.Fatalf("findNotificationTarget(nil) = %+v, %v; want nil, false", target, ok)
	}
}
