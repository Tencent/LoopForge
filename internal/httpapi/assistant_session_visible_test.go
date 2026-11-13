package httpapi

import (
	"reflect"
	"testing"

	"agentflow/internal/repo"
)

func TestVisibleAssistantConversations(t *testing.T) {
	const me, other = int64(1), int64(2)
	sharedRunID := repo.RunAssistantSessionID("run-shared")
	sharedIDs := map[string]bool{"shared-ok": true, "shared-no-project": true, "shared-wrong-run": true, "shared-forbidden": true, sharedRunID: true}
	runProjects := map[string]string{"run-ok": "p1", "run-wrong": "p2", "run-forbidden": "p3"}
	allowedProjects := map[string]bool{"p1": true, "p2": true, "p3": false}

	cases := []struct {
		name      string
		candidate assistantConversation
		visible   bool
	}{
		{"own plain conversation", assistantConversation{ID: "own", UserID: me}, true},
		{"valid shared record", assistantConversation{ID: "shared-ok", Shared: true, RunID: "run-ok", ProjectID: "p1"}, true},
		{"shared record without project", assistantConversation{ID: "shared-no-project", Shared: true, RunID: "run-ok"}, false},
		{"shared record whose run belongs elsewhere", assistantConversation{ID: "shared-wrong-run", Shared: true, RunID: "run-wrong", ProjectID: "p1"}, false},
		{"shared record in forbidden project", assistantConversation{ID: "shared-forbidden", Shared: true, RunID: "run-forbidden", ProjectID: "p3"}, false},
		{"owned row flagged shared keeps private fallback", assistantConversation{ID: "owned-shared", Shared: true, UserID: me}, true},
		{"run conversation covered by shared copy", assistantConversation{ID: "own-run", UserID: me, RunID: "run-shared"}, false},
		{"run conversation without shared copy", assistantConversation{ID: "own-run-solo", UserID: me, RunID: "run-solo"}, true},
		{"other member project chat with access", assistantConversation{ID: "other-allowed", UserID: other, ProjectID: "p1"}, true},
		{"other member project chat without access", assistantConversation{ID: "other-denied", UserID: other, ProjectID: "p3"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := visibleAssistantConversations([]assistantConversation{tc.candidate}, me, sharedIDs, runProjects, allowedProjects)
			if visible := len(got) == 1; visible != tc.visible {
				t.Fatalf("visible = %v, want %v", visible, tc.visible)
			}
		})
	}
}

func TestVisibleAssistantConversationsDeduplicatesKeepingFirst(t *testing.T) {
	candidates := []assistantConversation{
		{ID: "a", UserID: 1, Title: "first"},
		{ID: "b", UserID: 1},
		{ID: "a", UserID: 1, Title: "duplicate"},
	}
	got := visibleAssistantConversations(candidates, 1, nil, nil, nil)
	var titles []string
	var ids []string
	for _, item := range got {
		ids = append(ids, item.ID)
		titles = append(titles, item.Title)
	}
	if !reflect.DeepEqual(ids, []string{"a", "b"}) || titles[0] != "first" {
		t.Fatalf("got ids %v titles %v, want [a b] with first title kept", ids, titles)
	}
}

func TestVisibleAssistantConversationsDoesNotResetGenerationFields(t *testing.T) {
	candidates := []assistantConversation{{ID: "a", UserID: 1, GenerationStatus: "running", GenerationID: "g1", Unread: true}}
	got := visibleAssistantConversations(candidates, 1, nil, nil, nil)
	if len(got) != 1 || got[0].GenerationStatus != "running" || got[0].GenerationID != "g1" || !got[0].Unread {
		t.Fatalf("filter must leave generation fields untouched for listSessions to reset, got %+v", got)
	}
}
