package tracker

import (
	"errors"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestPullRepositoryIssuesSkipsUnsupportedRepositories(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := NewService()
	for _, repository := range []domain.Repository{
		{ID: "repo-local", ProjectID: "platform", Provider: "local"},
		{ID: "repo-gf-empty", ProjectID: "platform", Provider: "gongfeng"},
	} {
		if err := svc.pullRepositoryIssues(store, repository); err != nil {
			t.Fatalf("%s must be skipped, got %v", repository.ID, err)
		}
	}
}

func TestPullRepositoryIssuesReportsMissingGongfengClient(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := NewService()
	err = svc.pullRepositoryIssues(store, domain.Repository{ID: "repo-gf", ProjectID: "platform", Provider: "gongfeng", GongfengProjectID: "test/repo"})
	if err == nil || err.Error() != "gongfeng client not configured" {
		t.Fatalf("err=%v, want unconfigured gongfeng client error", err)
	}
}

func TestRecordPullResultPersistsOutcome(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := NewService()
	svc.cfg.Set(domain.SyncConfig{GongfengMCPURL: "http://gongfeng.example", LastSyncError: "stale"})

	svc.recordPullResult(store, errors.New("boom"))
	got := svc.cfg.Get()
	if got.LastSyncStatus != "failed" || got.LastSyncError != "boom" || got.LastSyncAt.IsZero() {
		t.Fatalf("failed pull not recorded: %+v", got)
	}
	if got.GongfengMCPURL != "http://gongfeng.example" {
		t.Fatalf("unrelated config lost: %+v", got)
	}
	if persisted := store.SyncConfig(); persisted.LastSyncStatus != "failed" || persisted.LastSyncError != "boom" {
		t.Fatalf("failed pull not persisted: %+v", persisted)
	}

	svc.recordPullResult(store, nil)
	got = svc.cfg.Get()
	if got.LastSyncStatus != "success" || got.LastSyncError != "" {
		t.Fatalf("successful pull not recorded: %+v", got)
	}
	if persisted := store.SyncConfig(); persisted.LastSyncStatus != "success" || persisted.LastSyncError != "" {
		t.Fatalf("successful pull not persisted: %+v", persisted)
	}
}
