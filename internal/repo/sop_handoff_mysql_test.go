package repo

import (
	"agentflow/internal/domain"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSOPHandoffMySQLConcurrentClaimsAndLeaseRecovery(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_SOP_HANDOFF_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("isolated local Docker MySQL required")
	}
	if !strings.Contains(dsn, "127.0.0.1:") || !strings.Contains(dsn, "/agentflow_handoff_jobs_test_") {
		t.Fatal("requires disposable local handoff test database")
	}
	a, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	now := time.Now().UTC().Truncate(time.Millisecond)
	runID := fmt.Sprintf("mysql-handoff-%d", now.UnixNano())
	if err := a.PutRun(domain.Run{ID: runID, Status: "waiting", ExecutionContext: domain.TelemetryContext{ProjectID: "mysql-handoff"}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		job domain.SOPHandoffJob
		won bool
		err error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	var group sync.WaitGroup
	for i, s := range []*Store{a, b} {
		group.Add(1)
		go func(i int, s *Store) {
			defer group.Done()
			<-start
			j, won, err := s.BeginSOPHandoffJob(domain.SOPHandoffJob{ID: fmt.Sprintf("job-%d", i), RunID: runID, NodeID: "build", ActorID: 1, CreatedAt: now})
			results <- outcome{j, won, err}
		}(i, s)
	}
	close(start)
	group.Wait()
	close(results)
	wins := 0
	jobID := ""
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.won {
			wins++
		}
		if jobID != "" && jobID != result.job.ID {
			t.Fatal("duplicate submission returned distinct jobs")
		}
		jobID = result.job.ID
	}
	if wins != 1 {
		t.Fatal("enqueue winners", wins)
	}
	start = make(chan struct{})
	results = make(chan outcome, 2)
	for i, s := range []*Store{a, b} {
		group.Add(1)
		go func(i int, s *Store) {
			defer group.Done()
			<-start
			j, won, err := s.ClaimSOPHandoffJob(runID, jobID, fmt.Sprintf("owner-%d", i), now, time.Minute)
			results <- outcome{j, won, err}
		}(i, s)
	}
	close(start)
	group.Wait()
	close(results)
	wins = 0
	var owned domain.SOPHandoffJob
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.won {
			wins++
			owned = result.job
		}
	}
	if wins != 1 {
		t.Fatal("claim winners", wins)
	}
	token, err := a.BeginWorkspaceUse(runID, now, jobID)
	if err != nil {
		t.Fatal(err)
	}
	next, ok, err := b.ClaimSOPHandoffJob(runID, jobID, "replacement", now.Add(2*time.Minute), time.Minute)
	if err != nil || !ok {
		t.Fatal("expired lease not recoverable", err)
	}
	if changed, err := a.UpdateSOPHandoffJob(owned, owned.Owner, now.Add(2*time.Minute)); err != nil || changed {
		t.Fatal("stale owner wrote after takeover", err)
	}
	if err := a.FinishWorkspaceUse(runID, token, now.Add(2*time.Minute), nil, ""); err == nil {
		t.Fatal("stale workspace token released replacement")
	}
	token, err = b.BeginWorkspaceUse(runID, now.Add(2*time.Minute), next.ID)
	if err != nil {
		t.Fatal("replacement blocked by old workspace", err)
	}
	if err := b.FinishWorkspaceUse(runID, token, now.Add(2*time.Minute), nil, ""); err != nil {
		t.Fatal(err)
	}
	next.Status = domain.SOPHandoffJobSucceeded
	next.Phase = domain.SOPHandoffPhaseDone
	if changed, err := b.FinishSOPHandoffJob(next, next.Owner, now.Add(2*time.Minute)); err != nil || !changed {
		t.Fatal("replacement cannot finish", err)
	}
	jobs, err := a.SOPHandoffJobsForProject("mysql-handoff")
	if err != nil || len(jobs) != 1 {
		t.Fatal("MySQL project query", err, len(jobs))
	}
}
