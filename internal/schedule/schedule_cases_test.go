package schedule

import (
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestJobDueBranches(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		job domain.ScheduleJob
		due bool
	}{
		{domain.ScheduleJob{}, false},
		{domain.ScheduleJob{Enabled: true, LastStatus: "running"}, false},
		{domain.ScheduleJob{Enabled: true}, true},
		{domain.ScheduleJob{Enabled: true, LastRunAt: now.Add(-31 * time.Second), IntervalSec: 1}, true},
		{domain.ScheduleJob{Enabled: true, LastRunAt: now.Add(-29 * time.Second), IntervalSec: 1}, false},
		{domain.ScheduleJob{Enabled: true, Cron: "bad", LastRunAt: now.Add(-time.Hour)}, false},
		{domain.ScheduleJob{Enabled: true, Cron: "* * * * *", LastRunAt: now.Add(-2 * time.Minute)}, true},
	}
	for i, tc := range tests {
		if got := jobDue(tc.job, now); got != tc.due {
			t.Fatalf("%d: due=%v", i, got)
		}
	}
}

func TestStartAndImmediateStop(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutProject(domain.Project{ID: "cvs-project", Name: "P", DefaultRepositoryID: "repo"}); err != nil {
		t.Fatal(err)
	}
	if got := issueRepositoryIDs(st, domain.Issue{ProjectID: "cvs-project"}); len(got) != 1 || got[0] != "repo" {
		t.Fatalf("repository IDs = %#v", got)
	}
	if got := issueRepositoryIDs(st, domain.Issue{RepositoryIDs: []string{"a", "b"}}); len(got) != 2 {
		t.Fatalf("bound repository IDs = %#v", got)
	}
	if err := st.PutScheduleJob(domain.ScheduleJob{ID: "cvs-interrupted", Name: "Interrupted", ProjectID: "cvs-project", LastStatus: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutScheduleJob(domain.ScheduleJob{ID: "cvs-idle", Name: "Idle", ProjectID: "cvs-project", LastStatus: "success"}); err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, nil)
	s.Start()
	s.Stop()
	if got, ok := st.ScheduleJob("cvs-interrupted"); !ok || got.LastStatus != "failed" {
		t.Fatalf("recovered job = %#v, %v", got, ok)
	}
}

func TestStartAfterStopBeforeRecovery(t *testing.T) {
	s := &Service{}
	gate := make(chan struct{})
	s.StartAfter(gate)
	s.StartAfter(gate)
	s.Stop()
	s.Stop()
}

func TestBlockerHelpers(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-time.Hour)
	blocked := map[string]domain.ScheduleJobBlocker{
		"b": {Reason: "same"},
		"a": {RepositoryID: "a", Reason: " same "},
		"c": {Reason: ""},
	}
	if got := joinBlockReasons(blocked); got != "same" {
		t.Fatalf("reasons = %q", got)
	}
	merged := mergeBlockers([]domain.ScheduleJobBlocker{{RepositoryID: "a", Since: old}}, blocked, now)
	if len(merged) != 3 || merged[0].RepositoryID != "a" || !merged[0].Since.Equal(old) || !merged[1].Since.Equal(now) {
		t.Fatalf("merged = %#v", merged)
	}
}
