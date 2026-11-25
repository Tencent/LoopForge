package repo

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// Two API replicas share only the database: the run row lock, not a process
// mutex, has to make "no evaluation is running" and the insert one step.
func TestMySQLConcurrentRunEvalStartsKeepOne(t *testing.T) {
	stores := []*Store{openBotMySQL(t), openBotMySQL(t)}
	live := func(v domain.RunEvalRecord) bool { return time.Since(v.CreatedAt) < time.Minute }
	for round := 0; round < 10; round++ {
		id := fmt.Sprintf("eval-race-%d-%d", time.Now().UnixNano(), round)
		if err := stores[0].PutRun(domain.Run{ID: id, Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = stores[0].DeleteRun(id) })
		// A row whose process stopped must not block the next start.
		stale := domain.RunEvalRecord{ID: id + "-stale", RunID: id, Status: domain.RunEvalRunning, CreatedAt: time.Now().UTC().Add(-time.Hour)}
		if err := stores[0].PutRunEval(stale); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		started, errs := make([]bool, 8), make([]error, 8)
		gate := make(chan struct{})
		for i := range started {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-gate
				v := domain.RunEvalRecord{ID: fmt.Sprintf("%s-%d", id, i), RunID: id, Status: domain.RunEvalRunning, CreatedAt: time.Now().UTC()}
				started[i], errs[i] = stores[i%2].StartRunEval(v, live, "interrupted")
			}()
		}
		close(gate)
		wg.Wait()
		count := 0
		for i := range started {
			if errs[i] != nil {
				t.Fatalf("round %d start %d: %v", round, i, errs[i])
			}
			if started[i] {
				count++
			}
		}
		if rows := stores[0].ListRunEvals(id); count != 1 || len(rows) != 2 {
			t.Fatalf("round %d: started = %d, rows = %d", round, count, len(rows))
		}
		if v, _ := stores[0].RunEval(stale.ID); v.Status != domain.RunEvalFailed || v.Error != "interrupted" || v.FinishedAt.IsZero() {
			t.Fatalf("stale evaluation = %+v", v)
		}
	}
}
