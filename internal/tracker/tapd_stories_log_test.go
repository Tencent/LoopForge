package tracker

import (
	"fmt"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAddTAPDStoriesLogFillsFixedFields(t *testing.T) {
	s := NewService()
	started := time.Date(2026, 11, 14, 3, 30, 0, 0, time.UTC)

	s.addTAPDStoriesLog(started, "p1", "failed", 3, "boom")
	s.addTAPDStoriesLog(started, "", "success", 5, "done")

	logs := s.Logs() // most recent first
	if len(logs) != 2 {
		t.Fatalf("expected 2 logs, got %d", len(logs))
	}
	wantID := fmt.Sprintf("sync-%d", started.UnixNano())
	for i, want := range []struct {
		projectID, status, message string
		count                      int
	}{
		{projectID: "", status: "success", message: "done", count: 5},
		{projectID: "p1", status: "failed", message: "boom", count: 3},
	} {
		got := logs[i]
		if got.Direction != "pull" || got.Source != "tapd" || got.Action != "sync_stories" {
			t.Fatalf("log %d fixed fields = %+v", i, got)
		}
		if got.ID != wantID {
			t.Fatalf("log %d ID = %q", i, got.ID)
		}
		if got.ProjectID != want.projectID || got.Status != want.status || got.Message != want.message || got.Count != want.count {
			t.Fatalf("log %d = %+v, want %+v", i, got, want)
		}
		if !got.StartedAt.Equal(started) || got.FinishedAt.Before(started) {
			t.Fatalf("log %d timestamps = %v / %v", i, got.StartedAt, got.FinishedAt)
		}
	}
}

func TestPullTAPDStoriesWithoutPoolsWritesNoLog(t *testing.T) {
	s := NewService()
	n, err := s.pullTAPDStories(&memIssueStore{})
	if n != 0 || err != nil {
		t.Fatalf("pullTAPDStories() = %d, %v", n, err)
	}
	if logs := s.Logs(); len(logs) != 0 {
		t.Fatalf("expected no logs without pools, got %+v", logs)
	}
}

func TestPullTAPDStoriesClientFailureLogsPerProject(t *testing.T) {
	s := NewService() // no TAPD client configured
	store := &memIssueStore{projects: map[string]domain.Project{
		"p1": {ID: "p1", TAPDWorkspaceID: "10001", TAPDIterationID: "20001"},
	}}

	n, err := s.pullTAPDStories(store)
	if n != 0 || err == nil {
		t.Fatalf("pullTAPDStories() = %d, %v; want client error", n, err)
	}
	logs := s.Logs()
	if len(logs) != 1 {
		t.Fatalf("expected one failure log, got %+v", logs)
	}
	got := logs[0]
	if got.Action != "sync_stories" || got.Source != "tapd" || got.Status != "failed" || got.ProjectID != "p1" || got.Count != 0 {
		t.Fatalf("failure log = %+v", got)
	}
	if got.Message != actionableAuthError("TAPD", err) {
		t.Fatalf("failure log message = %q, want %q", got.Message, actionableAuthError("TAPD", err))
	}
}
