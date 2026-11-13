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
					states, err := s.IssueDeletionStates([]domain.Issue{issue})
					if err != nil {
						t.Fatal(err)
					}
					state := states[id]
					busy := status == "analyzing" || status == "queued" || status == "running" || status == "waiting"
					if (state.Block != nil) != busy || state.RequiresForce() != (!busy && status != "idle" && status != "closed") {
						t.Fatalf("incorrect deletion state for %s: %+v", status, state)
					}
					if busy {
						block := state.Block
						code := "active"
						if status == "analyzing" {
							code = "clarifying"
						}
						if block.Code != code || block.Reason == "" {
							t.Fatalf("incorrect block for %s: %+v", status, block)
						}
						if (status == "running" || status == "waiting") && block.RunID != id+"-run" {
							t.Fatalf("missing run destination: %+v", block)
						}
						if status == "queued" && (block.RunID != "" || block.TaskID != id+"-task") {
							t.Fatalf("task-only block points to a missing run: %+v", block)
						}
					}
					closeAllowed, err := s.IssueClosureAllowed([]domain.Issue{issue})
					if err != nil {
						t.Fatal(err)
					}
					if closeAllowed[id] == busy {
						t.Fatalf("closure visibility for %s: %v", status, closeAllowed[id])
					}
					if status == "succeeded" || status == "completed" || status == "done" {
						if err := s.DeleteIssue(id); !errors.Is(err, ErrIssueSucceeded) {
							t.Fatal(err)
						}
					}
					if state.RequiresForce() {
						if _, err := s.IssueDeletionRuns(id, IssueDeleteNormal); !errors.Is(err, ErrIssueForceRequired) {
							t.Fatalf("delete without confirmation: %v", err)
						}
					}
					mode := IssueDeleteNormal
					if state.RequiresForce() || busy {
						mode = IssueDeleteForce
					}
					ids, err := s.IssueDeletionRuns(id, mode)
					if busy {
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
					if len(ids) > 0 && s.DeleteIssueAfterRunCleanup(id, mode) == nil {
						t.Fatal("deleted issue before run cleanup")
					}
					for _, runID := range ids {
						if err := s.DeleteRun(runID); err != nil {
							t.Fatal(err)
						}
					}
					if err := s.DeleteIssueAfterRunCleanup(id, mode); err != nil {
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
					if _, _, err := s.ReserveIssueDispatch(domain.Task{ID: id + "-task", IssueID: id, Status: "queued"}); !errors.Is(err, ErrIssueNotFound) {
						t.Fatalf("reserved a task for a deleted issue: %v", err)
					}
					if err := s.UpdateTaskStatus(domain.Task{ID: id + "-task", IssueID: id, RunID: id + "-run"}, "completed"); err != nil {
						t.Fatal(err)
					}
					if _, ok := s.Task(id + "-task"); ok {
						t.Fatal("reconciliation recreated a deleted task")
					}
					if err := s.PutIssue(issue); err != nil {
						t.Fatal(err)
					}
					if len(s.Comments(id)) != 0 {
						t.Fatal("reimport retained comment")
					}
				})
			}
			t.Run("concurrent dispatch", func(t *testing.T) {
				const id = "delete-racing-dispatch"
				if err := s.PutIssue(domain.Issue{ID: id, ProjectID: "platform", Status: "todo"}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					s.DB.Where("issue_id = ?", id).Delete(&taskRow{})
					s.DB.Where("id = ?", id).Delete(&issueRow{})
				})
				reservation := make(chan error, 1)
				go func() {
					_, _, err := s.ReserveIssueDispatch(domain.Task{ID: id + "-task", IssueID: id, Status: "queued"})
					reservation <- err
				}()
				deleteErr := s.DeleteIssueAfterRunCleanup(id, IssueDeleteNormal)
				dispatchErr := <-reservation
				if deleteErr == nil && errors.Is(dispatchErr, ErrIssueNotFound) {
					if _, exists := s.Task(id + "-task"); exists {
						t.Fatal("deleted issue has an orphaned task")
					}
					return
				}
				if !errors.Is(deleteErr, ErrIssueBusy) || dispatchErr != nil {
					t.Fatalf("concurrent deletion: %v, reservation: %v", deleteErr, dispatchErr)
				}
			})
		})
	}
}

func TestIssueDeletionStatesHistory(t *testing.T) {
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
			// The old success is linked only by its Task's RunID.
			if err := s.PutRun(domain.Run{ID: "delete-history-old", Status: "succeeded"}); err != nil {
				t.Fatal(err)
			}
			if err := s.PutTask(domain.Task{ID: "delete-history-task", IssueID: issue.ID, RunID: "delete-history-old", Status: "cancelled"}); err != nil {
				t.Fatal(err)
			}
			if err := s.PutRun(domain.Run{ID: "delete-history-latest", Status: "failed", ExecutionContext: domain.TelemetryContext{IssueID: issue.ID}}); err != nil {
				t.Fatal(err)
			}
			ids, err := s.IssueDeletionRuns(issue.ID, IssueDeleteForce)
			if err != nil || len(ids) != 2 {
				t.Fatalf("incorrect execution history: %v, %v", ids, err)
			}
			if _, err := s.IssueDeletionRuns(issue.ID, IssueDeleteUnsuccessful); !errors.Is(err, ErrIssueSucceeded) {
				t.Fatalf("internal cleanup allowed legacy success: %v", err)
			}
			check := func(code, runID string) {
				t.Helper()
				states, err := s.IssueDeletionStates([]domain.Issue{issue})
				if err != nil {
					t.Fatal(err)
				}
				if got := states[issue.ID].Block; got == nil || got.Code != code || got.RunID != runID {
					t.Fatalf("wrong historical blocker: %+v", got)
				}
			}
			if err := s.PutRun(domain.Run{
				ID: "delete-history-active", Status: "failed", ExecutionContext: domain.TelemetryContext{IssueID: issue.ID},
				Nodes: map[string]*domain.NodeRun{"pending": {NodeID: "pending", Status: "pending_input"}},
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.DB.Where("run_id = ?", "delete-history-active").Delete(&runNodeRow{}) })
			check("active", "delete-history-active")
			if _, err := s.IssueDeletionRuns(issue.ID, IssueDeleteForce); !errors.Is(err, ErrIssueBusy) {
				t.Fatalf("force allowed a run with an active node: %v", err)
			}
			issue.Clarification.Status = "analyzing"
			check("clarifying", "")
		})
	}
}
