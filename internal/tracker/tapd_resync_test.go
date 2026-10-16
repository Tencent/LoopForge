package tracker

import (
	"testing"

	"agentflow/internal/domain"
)

func TestResyncTAPDIssueRefreshesStoryContent(t *testing.T) {
	storiesPayload := `{"count":1,"data":[{"id":"1001","name":"需求A-新标题","description":"新正文","status":"planned","owner":"zhangsan(low)","priority":"high","iteration_id":"it1","created":"2026-09-10 10:00:00","modified":"2026-09-20 10:00:00"}]}`
	bugsPayload := `{"count":0,"data":[]}`
	gateway := mockTAPDMultiGateway(t, storiesPayload, bugsPayload, nil, nil)
	defer gateway.Close()

	svc := NewService()
	svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})
	store := &memIssueStore{projects: map[string]domain.Project{"dp": {ID: "dp", Name: "DNSPod", TAPDWorkspaceID: "10000000"}}}

	seed, err := svc.upsertTAPDStory(store, "dp", "10000000", tapdStory{
		ID: "1001", Name: "旧标题", Description: "旧正文", Status: "planned",
		Owner: "zhangsan", Priority: "middle",
		Created: "2026-09-10 10:00:00", Modified: "2026-09-10 10:00:00",
	}, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	seed.Clarification = domain.ClarificationAssessment{Status: domain.ClarificationReady, Summary: "已澄清"}
	seed.RepositoryIDs = []string{"repo-1"}
	seed.RepositoryID = "repo-1"
	if err := store.PutIssue(seed); err != nil {
		t.Fatal(err)
	}

	updated, err := svc.ResyncTAPDIssue(store, seed)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "需求A-新标题" {
		t.Fatalf("title=%q want refreshed", updated.Title)
	}
	if updated.Description != "新正文" {
		t.Fatalf("description=%q want refreshed", updated.Description)
	}
	if updated.Clarification.Summary != "已澄清" || updated.Clarification.Status != domain.ClarificationReady {
		t.Fatalf("clarification clobbered: %+v", updated.Clarification)
	}
	if updated.ExternalRef == nil || !updated.ExternalRef.Imported {
		t.Fatalf("imported flag lost: %+v", updated.ExternalRef)
	}
	if len(updated.RepositoryIDs) != 1 || updated.RepositoryIDs[0] != "repo-1" {
		t.Fatalf("repository binding lost: %+v", updated.RepositoryIDs)
	}
	if !updated.ExternalRef.SyncedAt.After(seed.ExternalRef.SyncedAt) {
		t.Fatalf("SyncedAt not advanced: %v -> %v", seed.ExternalRef.SyncedAt, updated.ExternalRef.SyncedAt)
	}
}

func TestResyncTAPDIssueRefreshesBugContent(t *testing.T) {
	storiesPayload := `{"count":0,"data":[]}`
	bugsPayload := `{"count":1,"data":[{"id":"2001","title":"缺陷B-新标题","description":"缺陷新正文","status":"new","current_owner":"lisi","severity":"normal","priority":"low","iteration_id":"it1","created":"2026-09-10 10:00:00","modified":"2026-09-20 10:00:00"}]}`
	gateway := mockTAPDMultiGateway(t, storiesPayload, bugsPayload, nil, nil)
	defer gateway.Close()

	svc := NewService()
	svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})
	store := &memIssueStore{projects: map[string]domain.Project{"dp": {ID: "dp", Name: "DNSPod", TAPDWorkspaceID: "10000000"}}}

	seed, err := svc.upsertTAPDBug(store, "dp", "10000000", tapdStory{
		ID: "2001", Name: "缺陷B旧标题", Description: "缺陷旧正文", Status: "new",
		CurrentOwner: "lisi", Priority: "middle",
		Created: "2026-09-10 10:00:00", Modified: "2026-09-10 10:00:00",
	}, 0, false)
	if err != nil {
		t.Fatal(err)
	}

	updated, err := svc.ResyncTAPDIssue(store, seed)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "缺陷B-新标题" {
		t.Fatalf("title=%q want refreshed", updated.Title)
	}
	if updated.Kind != "bug" {
		t.Fatalf("kind=%q want bug", updated.Kind)
	}
}

func TestResyncTAPDIssueRejectsNonTAPDSource(t *testing.T) {
	svc := NewService()
	store := &memIssueStore{projects: map[string]domain.Project{"dp": {ID: "dp", Name: "DNSPod"}}}
	if _, err := svc.ResyncTAPDIssue(store, domain.Issue{ID: "local-1", ProjectID: "dp", Source: "local"}); err == nil {
		t.Fatal("expected error for non-tapd issue")
	}
	tapdNoRef := domain.Issue{ID: "tapd-1001", ProjectID: "dp", Source: "tapd"}
	if _, err := svc.ResyncTAPDIssue(store, tapdNoRef); err == nil {
		t.Fatal("expected error for tapd issue without external ref")
	}
}
