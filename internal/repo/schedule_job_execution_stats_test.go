package repo

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func openExecutionTestStore(t *testing.T, backend string) *Store {
	t.Helper()
	var st *Store
	var err error
	if backend == "mysql" {
		dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
		if dsn == "" {
			t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
		}
		st, err = Open(dsn)
	} else {
		st, err = OpenTest()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

// The overview counts every execution of the last two weeks, not just the
// 200 a job keeps, and the dashboard filters and pages on the server.
func TestScheduleJobExecutionOverviewOutlivesTheKeptHistory(t *testing.T) {
	for _, backend := range []string{"sqlite", "mysql"} {
		t.Run(backend, func(t *testing.T) {
			st := openExecutionTestStore(t, backend)
			suffix := time.Now().UnixNano()
			jobID, otherID := fmt.Sprintf("stat-job-%d", suffix), fmt.Sprintf("stat-other-%d", suffix)
			for _, id := range []string{jobID, otherID} {
				if err := st.PutScheduleJob(domain.ScheduleJob{ID: id, Name: "AI", ProjectID: "platform", SkipDispatch: true, AssistantPrompt: "p"}); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { _ = st.DeleteScheduleJob(jobID); _ = st.DeleteScheduleJob(otherID) })
			now := time.Now().UTC().Truncate(time.Hour).Add(30 * time.Minute)
			trace := func(i int) string { return fmt.Sprintf("%d-%03d", suffix, i) }
			run := func(job string, i int, started time.Time, status string, durationMs int64) {
				t.Helper()
				e := domain.ScheduleJobExecution{JobID: job, ProjectID: "platform", TraceID: trace(i), Status: "running", StartedAt: started}
				if err := st.PutScheduleJobExecution(e); err != nil {
					t.Fatal(err)
				}
				e.Status, e.DurationMs = status, durationMs
				if status == "failed" {
					e.Error = "boom"
				}
				if err := st.PutScheduleJobExecution(e); err != nil {
					t.Fatal(err)
				}
			}
			// This week: 230 finished, every 10th failed, 1s each.
			const thisWeek = MaxScheduleJobExecutionsPerJob + 30
			for i := range thisWeek {
				status := "success"
				if i%10 == 0 {
					status = "failed"
				}
				run(jobID, i, now.Add(-100*time.Hour).Add(time.Duration(i)*time.Minute), status, 1000)
			}
			// Rewriting a finished execution does not count it again.
			again := domain.ScheduleJobExecution{JobID: jobID, ProjectID: "platform", TraceID: trace(thisWeek - 1), Status: "success", DurationMs: 1000, StartedAt: now.Add(-100 * time.Hour).Add(time.Duration(thisWeek-1) * time.Minute)}
			if err := st.PutScheduleJobExecution(again); err != nil {
				t.Fatal(err)
			}
			// Last week: 3 successes, 4s each. Before that: outside both windows.
			for i := range 3 {
				run(jobID, 500+i, now.Add(-200*time.Hour).Add(time.Duration(i)*time.Hour), "success", 4000)
			}
			run(jobID, 600, now.Add(-20*24*time.Hour), "failed", 9000)
			if err := st.PutScheduleJobExecution(domain.ScheduleJobExecution{JobID: jobID, ProjectID: "platform", TraceID: trace(700), Status: "running", StartedAt: now.Add(-time.Minute)}); err != nil {
				t.Fatal(err)
			}
			run(otherID, 800, now.Add(-time.Hour), "failed", 1000)

			if kept := st.CountScheduleJobExecutions([]string{jobID}); kept != MaxScheduleJobExecutionsPerJob {
				t.Fatalf("kept %d executions", kept)
			}
			current, previous := st.ScheduleJobExecutionOverview([]string{jobID}, now)
			wantCurrent := domain.ScheduleExecutionWindow{Total: thisWeek + 1, Finished: thisWeek, Success: thisWeek - 23, Failed: 23, Running: 1, AvgMs: 1000}
			wantPrevious := domain.ScheduleExecutionWindow{Total: 3, Finished: 3, Success: 3, AvgMs: 4000}
			if current != wantCurrent || previous != wantPrevious {
				t.Fatalf("overview = %+v / %+v, want %+v / %+v", current, previous, wantCurrent, wantPrevious)
			}
			if both, _ := st.ScheduleJobExecutionOverview([]string{jobID, otherID}, now); both.Failed != 24 {
				t.Fatalf("overview over two jobs = %+v", both)
			}

			kept := st.ScheduleJobExecutions(ScheduleJobExecutionQuery{JobIDs: []string{jobID}, Limit: 500})
			failedKept := 0
			for _, e := range kept {
				if e.Status == "failed" {
					failedKept++
				}
			}
			page, total := st.ScheduleJobExecutionPage(ScheduleJobExecutionPageQuery{JobIDs: []string{jobID}, Status: "failed", Offset: 2, Limit: 5})
			if total != failedKept || len(page) != 5 {
				t.Fatalf("failed page: %d items of %d, want 5 of %d", len(page), total, failedKept)
			}
			for i := 1; i < len(page); i++ {
				if page[i].Status != "failed" || page[i].StartedAt.After(page[i-1].StartedAt) {
					t.Fatalf("page not filtered newest first: %+v", page)
				}
			}
			all, total := st.ScheduleJobExecutionPage(ScheduleJobExecutionPageQuery{JobIDs: []string{jobID}, Limit: 1})
			if total != MaxScheduleJobExecutionsPerJob || len(all) != 1 || all[0].TraceID != trace(700) {
				t.Fatalf("first page = %+v of %d", all, total)
			}
			since, total := st.ScheduleJobExecutionPage(ScheduleJobExecutionPageQuery{JobIDs: []string{jobID}, Since: now.Add(-30 * time.Minute), Limit: 50})
			if total != 1 || since[0].TraceID != trace(700) {
				t.Fatalf("since page = %+v of %d", since, total)
			}
			// Search is case-insensitive over the shown fields.
			found, total := st.ScheduleJobExecutionPage(ScheduleJobExecutionPageQuery{JobIDs: []string{jobID}, Search: strings.ToUpper(trace(thisWeek - 2)), Limit: 10})
			if total != 1 || found[0].TraceID != trace(thisWeek-2) {
				t.Fatalf("search by trace = %+v of %d", found, total)
			}
			if _, total := st.ScheduleJobExecutionPage(ScheduleJobExecutionPageQuery{JobIDs: []string{jobID}, Search: "BOOM", Limit: 10}); total != failedKept {
				t.Fatalf("search by error matched %d, want %d", total, failedKept)
			}
			// "running" appears in every payload's history only as a status,
			// which search does not cover.
			if _, total := st.ScheduleJobExecutionPage(ScheduleJobExecutionPageQuery{JobIDs: []string{jobID}, Search: "running", Limit: 10}); total != 0 {
				t.Fatalf("search matched a field it does not cover: %d", total)
			}

			if err := st.DeleteScheduleJob(jobID); err != nil {
				t.Fatal(err)
			}
			if current, previous := st.ScheduleJobExecutionOverview([]string{jobID}, now); current != (domain.ScheduleExecutionWindow{}) || previous != (domain.ScheduleExecutionWindow{}) {
				t.Fatalf("statistics outlived the job: %+v / %+v", current, previous)
			}
		})
	}
}
