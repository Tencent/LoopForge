package tracker

import (
	"fmt"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestTAPDRepeatedSyncPreservesLocalStatus(t *testing.T) {
	for _, kind := range []string{"requirement", "bug", "task"} {
		t.Run(kind, func(t *testing.T) {
			store, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			svc := NewService()
			record := tapdStory{ID: "1001", Name: "original", Status: "new"}
			created, err := svc.upsertTAPDRecord(store, "platform", "10000000", record, kind, 0, false)
			if err != nil {
				t.Fatal(err)
			}
			wantInitial := "backlog"
			if kind == "bug" {
				wantInitial = "todo"
			}
			if created.Status != wantInitial {
				t.Fatalf("initial status=%q, want %q", created.Status, wantInitial)
			}
			created.Status = "todo"
			created.AutomationState = "eligible"
			created.AppendOperation(domain.IssueOperation{Action: "update", Detail: "状态 待规划 → 待处理"})
			if err := store.PutIssue(created); err != nil {
				t.Fatal(err)
			}
			for _, remote := range []string{"new", "planned", "open", "done", "closed", "new"} {
				record.Status = remote
				record.Name = "refreshed-" + remote
				got, err := svc.upsertTAPDRecord(store, "platform", "10000000", record, kind, 0, false)
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != "todo" || got.AutomationState != "eligible" || got.CloseSource != "" {
					t.Fatalf("remote %q changed local lifecycle: %+v", remote, got)
				}
				if got.Title != record.Name || got.ExternalRef.ExternalState != remote || len(got.Operations) != 1 {
					t.Fatalf("metadata or local audit trail lost: %+v", got)
				}
			}
		})
	}
}

func TestGongfengRepeatedSyncPreservesLocalStatus(t *testing.T) {
	// The MCP transport fixture returns the same payload for both state queries;
	// pullGongfengIssues deduplicates by IID just as it does for real pages.
	for _, remote := range []string{"opened", "closed"} {
		t.Run(remote, func(t *testing.T) {
			payload := fmt.Sprintf(`{"data":[{"id":10,"iid":7,"title":"refreshed","state":%q}]}`, remote)
			gateway := mockTAPDMultiGateway(t, payload, "", nil, nil)
			defer gateway.Close()
			store, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			issue := domain.Issue{
				ID: "gf-sync-7", ProjectID: "platform", RepositoryID: "repo-sync", Source: "gongfeng", Status: "backlog", AutomationState: "eligible",
				DeliveryState: "merged", DeliveryMRState: "closed",
				ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalIID: 7, ExternalState: "opened"},
			}
			if err := store.PutIssue(issue); err != nil {
				t.Fatal(err)
			}
			svc := NewService()
			svc.Configure(domain.SyncConfig{GongfengMCPURL: gateway.URL, TAPDMCPURL: gateway.URL})
			if err := store.PutRepository(domain.Repository{ID: "repo-sync", ProjectID: "platform", Provider: "gongfeng", GongfengProjectID: "test/repo"}); err != nil {
				t.Fatal(err)
			}
			if err := svc.PullAll(store); err != nil {
				t.Fatal(err)
			}
			got, _ := store.Issue(issue.ID)
			if got.Status != "backlog" || got.AutomationState != "eligible" || got.Title != "refreshed" || got.ExternalRef.ExternalState != remote {
				t.Fatalf("sync must preserve local status and refresh tracker metadata: %+v", got)
			}
		})
	}
}
