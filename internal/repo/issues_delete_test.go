package repo

import (
	"agentflow/internal/domain"
	"errors"
	"os"
	"testing"
)

func TestDeleteIssue(t *testing.T) {
	for _, driver := range []string{"sqlite", "mysql"} {
		t.Run(driver, func(t *testing.T) {
			var s *Store
			var err error
			if driver == "mysql" {
				dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("MySQL DSN not set")
				}
				s, err = Open(dsn)
			} else {
				s, err = OpenTest()
			}
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			for _, status := range []string{"idle", "closed", "analyzing", "running", "waiting", "queued", "succeeded", "completed", "done", "failed", "cancelled"} {
				t.Run(status, func(t *testing.T) {
					id := "delete-boundary-" + status
					issue := domain.Issue{ID: id, ProjectID: "platform", Title: "delete test", Status: "backlog", Source: "tapd"}
					if status == "done" {
						issue.Status = "done"
					}
					if status == "closed" {
						issue.Status = "closed"
						issue.CloseSource = "manual"
					}
					if status == "analyzing" {
						issue.Clarification.Status = status
					}
					if err := s.PutIssue(issue); err != nil {
						t.Fatal(err)
					}
					if err := s.PutComment(domain.IssueComment{ID: id + "-comment", IssueID: id, Body: "test"}); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						s.DB.Exec("DELETE FROM issue_comments WHERE issue_id=?", id)
						s.DB.Exec("DELETE FROM tasks WHERE issue_id=?", id)
						s.DB.Exec("DELETE FROM runs WHERE id=?", id+"-run")
						s.DB.Exec("DELETE FROM issues WHERE id=?", id)
					})
					if status == "running" || status == "waiting" || status == "succeeded" || status == "failed" || status == "cancelled" {
						if err := s.PutRun(domain.Run{ID: id + "-run", Status: status, ExecutionContext: domain.TelemetryContext{IssueID: id}}); err != nil {
							t.Fatal(err)
						}
					}
					if status == "queued" || status == "completed" || status == "failed" || status == "cancelled" {
						if err := s.PutTask(domain.Task{ID: id + "-task", IssueID: id, RunID: id + "-run", Status: status}); err != nil {
							t.Fatal(err)
						}
					}
					eligibility, err := s.IssueDeletionAllowed([]domain.Issue{issue})
					if err != nil {
						t.Fatal(err)
					}
					expected := status == "idle" || status == "closed" || status == "failed" || status == "cancelled"
					if eligibility[id] != expected {
						t.Fatalf("delete visibility for %s: %v", status, eligibility[id])
					}
					blocks, err := s.IssueDeletionBlocks([]domain.Issue{issue})
					if err != nil {
						t.Fatal(err)
					}
					block, blocked := blocks[id]
					if blocked == expected {
						t.Fatalf("block and eligibility disagree: %+v", block)
					}
					if blocked {
						code := "active"
						if status == "analyzing" {
							code = "clarifying"
						}
						if status == "done" {
							code = "completed"
						}
						if status == "completed" || status == "succeeded" {
							code = "succeeded"
						}
						if block.Code != code || block.Reason == "" {
							t.Fatalf("incorrect block for %s: %+v", status, block)
						}
						if (status == "running" || status == "waiting" || status == "succeeded") && block.RunID != id+"-run" {
							t.Fatalf("missing run destination: %+v", block)
						}
						if (status == "queued" || status == "completed") && (block.RunID != "" || block.TaskID != id+"-task") {
							t.Fatalf("task-only block points to a missing run: %+v", block)
						}
					}
					closeAllowed, err := s.IssueClosureAllowed([]domain.Issue{issue})
					if err != nil {
						t.Fatal(err)
					}
					expectedClose := expected || status == "completed" || status == "succeeded" || status == "done"
					if closeAllowed[id] != expectedClose {
						t.Fatalf("closure visibility for %s: %v", status, closeAllowed[id])
					}
					ids, err := s.IssueDeletionRuns(id)
					if status == "succeeded" || status == "completed" || status == "done" {
						if !errors.Is(err, ErrIssueSucceeded) {
							t.Fatalf("expected successful guard: %v", err)
						}
						if err = s.DeleteIssue(id); !errors.Is(err, ErrIssueSucceeded) {
							t.Fatal(err)
						}
						return
					}
					if status != "idle" && status != "closed" && status != "failed" && status != "cancelled" {
						if !errors.Is(err, ErrIssueBusy) {
							t.Fatalf("expected busy: %v", err)
						}
						if _, ok := s.Issue(id); !ok || len(s.Comments(id)) != 1 {
							t.Fatal("deleted busy data")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					for _, runID := range ids {
						if err := s.DeleteRun(runID); err != nil {
							t.Fatal(err)
						}
					}
					if err := s.DeleteIssue(id); err != nil {
						t.Fatal(err)
					}
					if _, ok := s.Issue(id); ok || len(s.Comments(id)) != 0 {
						t.Fatal("remaining issue/comment")
					}
					if _, ok := s.Task(id + "-task"); ok {
						t.Fatal("remaining task")
					}
					if err := s.DeleteIssue(id); !errors.Is(err, ErrIssueNotFound) {
						t.Fatal(err)
					}
					if err := s.PutIssue(issue); err != nil {
						t.Fatal(err)
					}
					if len(s.Comments(id)) != 0 {
						t.Fatal("reimport retained comment")
					}
				})
			}
		})
	}
}

func TestIssueDeletionBlocksHistory(t *testing.T) {
	for _, driver := range []string{"sqlite", "mysql"} {
		t.Run(driver, func(t *testing.T) {
			var s *Store
			var err error
			if driver == "mysql" {
				dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("MySQL DSN not set")
				}
				s, err = Open(dsn)
			} else {
				s, err = OpenTest()
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			issue := domain.Issue{ID: "delete-history", ProjectID: "platform", Status: "closed", CloseSource: "manual"}
			if err := s.PutIssue(issue); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				s.DB.Where("issue_id = ?", issue.ID).Delete(&taskRow{})
				s.DB.Where("id IN ?", []string{"delete-history-old", "delete-history-latest", "delete-history-active"}).Delete(&runRow{})
				s.DB.Where("id = ?", issue.ID).Delete(&issueRow{})
			})
			// A legacy success is linked only by a cancelled task; the latest
			// directly linked run failed and must not hide the historical blocker.
			if err := s.PutRun(domain.Run{ID: "delete-history-old", Status: "succeeded"}); err != nil {
				t.Fatal(err)
			}
			if err := s.PutTask(domain.Task{ID: "delete-history-task", IssueID: issue.ID, RunID: "delete-history-old", Status: "cancelled"}); err != nil {
				t.Fatal(err)
			}
			if err := s.PutRun(domain.Run{ID: "delete-history-latest", Status: "failed", ExecutionContext: domain.TelemetryContext{IssueID: issue.ID}}); err != nil {
				t.Fatal(err)
			}
			check := func(code, runID string) {
				t.Helper()
				blocks, err := s.IssueDeletionBlocks([]domain.Issue{issue})
				if err != nil {
					t.Fatal(err)
				}
				if got := blocks[issue.ID]; got.Code != code || got.RunID != runID {
					t.Fatalf("wrong historical blocker: %+v", got)
				}
			}
			check("succeeded", "delete-history-old")
			if err := s.PutRun(domain.Run{ID: "delete-history-active", Status: "running", ExecutionContext: domain.TelemetryContext{IssueID: issue.ID}}); err != nil {
				t.Fatal(err)
			}
			check("active", "delete-history-active")
			issue.Clarification.Status = "analyzing"
			check("clarifying", "")
		})
	}
}
