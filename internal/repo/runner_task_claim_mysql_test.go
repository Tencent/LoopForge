package repo

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// Legacy runners receive input files inline, so a queued task payload can
// exceed MySQL's default 256 KiB sort buffer. Claims and the task list must
// not sort whole rows, or one large task blocks every claim for its provider.
func TestMySQLRunnerTaskClaimWithLargePayload(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
	}
	store, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	prefix := fmt.Sprintf("claim-large-%d", time.Now().UnixNano())
	provider := prefix + "-provider"
	runner := domain.Runner{ID: prefix + "-runner", OwnerUserID: 1, Name: "Large payload runner", Status: domain.RunnerStatusOnline}
	if err := store.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = store.DB.Where("id LIKE ?", prefix+"%").Delete(&runnerTaskRow{}).Error
		_ = store.DeregisterRunner(runner.ID, time.Now().UTC())
	})
	created := time.Now().UTC().Add(-time.Minute)
	ids := []string{prefix + "-older", prefix + "-newer"}
	for i, id := range ids {
		if err := store.PutRunnerTask(domain.RunnerTask{
			ID: id, Provider: provider, Status: domain.RunnerTaskQueued, MaxAttempts: 1,
			Prompt:    strings.Repeat("x", 2<<20),
			CreatedAt: created.Add(time.Duration(i) * time.Second), UpdatedAt: created,
		}); err != nil {
			t.Fatal(err)
		}
	}
	listed := map[string]bool{}
	for _, task := range store.RunnerTasks() {
		listed[task.ID] = len(task.Prompt) == 2<<20
	}
	if !listed[ids[0]] || !listed[ids[1]] {
		t.Fatalf("task list omitted large tasks: %v", listed)
	}
	task, ok, err := store.ClaimRunnerTask(runner.ID, []string{provider}, time.Now().UTC(), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim with a large queued payload = %v, %v", ok, err)
	}
	if task.ID != ids[0] || task.Status != domain.RunnerTaskLeased || task.Attempt != 1 || len(task.Prompt) != 2<<20 {
		t.Fatalf("claimed %s status=%s attempt=%d prompt=%d, want the oldest task leased with its payload", task.ID, task.Status, task.Attempt, len(task.Prompt))
	}
}
