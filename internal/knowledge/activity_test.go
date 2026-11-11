package knowledge

import (
	"errors"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func (f *fakeStore) PutKnowledgeActivity(item domain.KnowledgeActivity) error {
	f.activities = append(f.activities, item)
	return nil
}

func (f *fakeStore) KnowledgeActivities(projectID string, limit int) []domain.KnowledgeActivity {
	out := make([]domain.KnowledgeActivity, 0)
	if projectID == "" {
		return out
	}
	if limit <= 0 || limit > ActivityLimit {
		limit = ActivityLimit
	}
	for i := len(f.activities) - 1; i >= 0; i-- {
		if f.activities[i].ProjectID == projectID {
			out = append(out, f.activities[i])
			if len(out) == limit {
				break
			}
		}
	}
	return out
}

func TestActivitySurvivesNewServiceInstance(t *testing.T) {
	store := newFakeStore()
	up := &fakeUpstream{}
	first := newTestService(store, up)
	at := time.Date(2026, 11, 11, 8, 30, 0, 0, time.UTC)
	if err := first.RecordActivity(domain.KnowledgeActivity{
		At: at, Tool: ToolSearch, ProjectID: "p1", AgentID: "dev", UserID: 7,
		RunID: "run-1", SessionID: "chat-1", Query: "青鸾新羽", Hits: 1, OK: true, LatencyMS: 9,
	}); err != nil {
		t.Fatal(err)
	}

	// A new Service has no memory of its own: this stands in for a process
	// restart and must read the same records back from the store.
	second := newTestService(store, up)
	got := second.Activities("p1", 20)
	if len(got) != 1 {
		t.Fatalf("activity after restart = %#v", got)
	}
	item := got[0]
	if item.Tool != ToolSearch || item.AgentID != "dev" || item.UserID != 7 || item.RunID != "run-1" ||
		item.SessionID != "chat-1" || item.Query != "青鸾新羽" || item.Hits != 1 || !item.OK || item.LatencyMS != 9 {
		t.Fatalf("activity fields = %#v", item)
	}
	if !item.At.Equal(at) {
		t.Fatalf("activity time = %s, want %s", item.At, at)
	}
	if other := second.Activities("p2", 20); len(other) != 0 {
		t.Fatalf("another project saw activity: %#v", other)
	}
	if all := second.Activities("", 20); len(all) != 0 {
		t.Fatalf("empty project must not read every project: %#v", all)
	}
}

type failingActivityStore struct {
	*fakeStore
}

func (f failingActivityStore) PutKnowledgeActivity(domain.KnowledgeActivity) error {
	return errors.New("activity store down")
}

func TestRecordActivityReportsStoreFailure(t *testing.T) {
	svc := NewService(failingActivityStore{fakeStore: newFakeStore()}, func() string { return "id1" }, &fakeUpstream{})
	if err := svc.RecordActivity(domain.KnowledgeActivity{Tool: ToolSearch, ProjectID: "p1"}); err == nil {
		t.Fatal("a failed activity write must be reported so the caller can log it")
	}
}
