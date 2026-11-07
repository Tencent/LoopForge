package repo

import (
	"fmt"
	"os"
	"testing"

	"agentflow/internal/domain"
)

func TestPutSyncedIssuePreservesCurrentLifecycle(t *testing.T) {
	for _, driver := range []string{"sqlite", "mysql"} {
		t.Run(driver, func(t *testing.T) {
			var s *Store
			var err error
			if driver == "mysql" {
				dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
				}
				s, err = Open(dsn)
			} else {
				s, err = OpenTest()
			}
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			for _, source := range []string{"tapd", "gongfeng"} {
				for _, status := range []string{"backlog", "todo", "in_progress", "in_review", "blocked", "done", "cancelled", "closed"} {
					for _, remote := range []string{"new", "open", "done", "closed"} {
						t.Run(source+"/"+status+"/"+remote, func(t *testing.T) {
							id := fmt.Sprintf("sync-status-%s-%s-%s", source, status, remote)
							t.Cleanup(func() { s.DB.Exec("DELETE FROM issues WHERE id=?", id) })
							issue := domain.Issue{ID: id, ProjectID: "platform", Source: source, Title: "before", Status: "backlog", AutomationState: "eligible"}
							if err := s.PutIssue(issue); err != nil {
								t.Fatal(err)
							}
							// The tracker snapshot predates a local status edit.
							snapshot, _ := s.Issue(id)
							issue.Status = status
							if err := s.PutIssue(issue); err != nil {
								t.Fatal(err)
							}
							current, _ := s.Issue(id)
							snapshot.Title = "refreshed"
							snapshot.ExternalRef = &domain.ExternalRef{Source: source, ExternalState: remote}
							if err := s.PutSyncedIssue(snapshot); err != nil {
								t.Fatal(err)
							}
							got, _ := s.Issue(id)
							if got.Status != current.Status || got.CloseSource != current.CloseSource || got.AutomationState != current.AutomationState {
								t.Fatalf("sync changed lifecycle: before=%+v after=%+v", current, got)
							}
							if got.Title != "refreshed" || got.ExternalRef.ExternalState != remote {
								t.Fatalf("tracker metadata not refreshed: %+v", got)
							}
							var storedStatus string
							if err := s.DB.Table("issues").Select("status").Where("id=?", id).Scan(&storedStatus).Error; err != nil {
								t.Fatal(err)
							}
							if storedStatus != status {
								t.Fatalf("status column=%q, want %q", storedStatus, status)
							}
							if remote == "closed" && !got.TrackerClosed() {
								t.Fatal("external closure must still prevent execution")
							}
						})
					}
				}
			}
		})
	}
}
