package tracker

import (
	"testing"

	"agentflow/internal/domain"
)

func TestPickTAPDRecord(t *testing.T) {
	raw := `{"count":2,"data":[{"id":"1001","name":"需求A"},{"id":"1002","name":"需求B"}]}`
	st, err := pickTAPDRecord(raw, "story", " 1002 ")
	if err != nil {
		t.Fatal(err)
	}
	if st.ID != "1002" || st.Name != "需求B" {
		t.Fatalf("picked %+v, want 1002/需求B", st)
	}

	for _, tc := range []struct{ kind, want string }{
		{"story", "未找到 TAPD 需求 9999"},
		{"bug", "未找到 TAPD 缺陷 9999"},
		{"task", "未找到 TAPD 任务 9999"},
	} {
		if _, err := pickTAPDRecord(raw, tc.kind, "9999"); err == nil || err.Error() != tc.want {
			t.Fatalf("kind=%s err=%v, want %q", tc.kind, err, tc.want)
		}
	}

	if _, err := pickTAPDRecord("not json", "story", "1001"); err == nil {
		t.Fatal("want parse error for non-JSON result")
	}
}

func TestUpsertTAPDByKindDispatchesByRecordKind(t *testing.T) {
	svc := NewService()
	store := &memIssueStore{projects: map[string]domain.Project{"dp": {ID: "dp", Name: "DemoDNS", TAPDWorkspaceID: "10000000"}}}
	for _, tc := range []struct{ kind, id, wantType string }{
		{"story", "3001", "story"},
		{"bug", "3002", "bug"},
		{"task", "3003", "task"},
		{"", "3004", "story"},
	} {
		issue, err := svc.upsertTAPDByKind(store, tc.kind, "dp", "10000000", tapdStory{ID: tc.id, Name: "记录" + tc.id}, 0, false)
		if err != nil {
			t.Fatalf("kind=%q: %v", tc.kind, err)
		}
		if got := tapdExternalType(issue); got != tc.wantType {
			t.Fatalf("kind=%q external type=%q, want %q", tc.kind, got, tc.wantType)
		}
	}
}
