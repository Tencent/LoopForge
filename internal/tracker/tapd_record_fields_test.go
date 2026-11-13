package tracker

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestTAPDRecordKindFields(t *testing.T) {
	st := tapdStory{
		ID:            "1001",
		Status:        "open",
		Priority:      "high",
		PriorityLabel: "low",
		Owner:         "owner;",
		CurrentOwner:  "current;",
	}
	for _, tc := range []struct {
		kind string
		want tapdRecordFields
	}{
		{"requirement", tapdRecordFields{externalType: "story", issueKind: "requirement", assignee: "owner;", status: "in_progress", priority: "urgent"}},
		{"bug", tapdRecordFields{externalType: "bug", issueKind: "bug", assignee: "current;", status: "backlog", priority: "urgent"}},
		{"task", tapdRecordFields{externalType: "task", issueKind: "requirement", assignee: "owner;", status: "todo", priority: "low"}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			if got := tapdRecordKindFields(st, tc.kind); got != tc.want {
				t.Fatalf("tapdRecordKindFields(%q)=%+v, want %+v", tc.kind, got, tc.want)
			}
		})
	}
}

func TestAllocateTAPDIssueID(t *testing.T) {
	for _, tc := range []struct {
		name         string
		externalType string
		occupied     []string
		want         string
		wantErr      bool
	}{
		{name: "story historic id", externalType: "story", want: "tapd-1001"},
		{name: "bug historic id", externalType: "bug", want: "tapd-1001"},
		{name: "task historic id", externalType: "task", want: "tapd-task-1001"},
		{name: "historic id taken", externalType: "bug", occupied: []string{"tapd-1001"}, want: "tapd-10000000-bug-1001"},
		{name: "task historic id taken", externalType: "task", occupied: []string{"tapd-task-1001"}, want: "tapd-10000000-task-1001"},
		{name: "both ids taken", externalType: "story", occupied: []string{"tapd-1001", "tapd-10000000-story-1001"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &memIssueStore{}
			for _, id := range tc.occupied {
				if err := store.PutIssue(domain.Issue{ID: id}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := allocateTAPDIssueID(store, "10000000", tc.externalType, "1001")
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "本地 ID 已被占用") {
					t.Fatalf("want occupied error, got id=%q err=%v", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("allocateTAPDIssueID=%q err=%v, want %q", got, err, tc.want)
			}
		})
	}
}
