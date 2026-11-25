package repo

import (
	"os"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// A reviewer/deliverer task carries the whole upstream artifact contract in its
// payload — several hundred KB. ClaimRunnerTask sorts queued tasks by
// created_at, and every selected column travels in the sort record, so a full
// row could not be sorted inside MySQL's sort_buffer_size: the claim failed
// with "Error 1038 (HY001): Out of sort memory" and the run stayed queued
// forever while the runner retried every few seconds.
//
// The DSN must point to a disposable local MySQL database.
func TestClaimRunnerTaskWithLargePayloadMySQL(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_RUNNER_CLAIM_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable local MySQL only")
	}
	store, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.DB.Exec("SET SESSION sort_buffer_size = 262144").Error; err != nil {
		t.Fatal(err)
	}
	task := domain.RunnerTask{
		ID:          "rt-large-payload-review",
		RunnerID:    "runner-large-payload",
		Provider:    "pi",
		Prompt:      strings.Repeat("审", 120_000),
		Status:      domain.RunnerTaskQueued,
		MaxAttempts: 1,
		RunID:       "run-large-payload",
		NodeID:      "review",
	}
	if err := store.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	defer store.DB.Exec("DELETE FROM runner_tasks WHERE id = ?", task.ID)

	claimed, ok, err := store.ClaimRunnerTask(task.RunnerID, []string{"pi"}, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatalf("claim failed on a large queued payload: %v", err)
	}
	if !ok || claimed.ID != task.ID {
		t.Fatalf("claimed = %q, ok = %v, want %q", claimed.ID, ok, task.ID)
	}
	if claimed.Prompt != task.Prompt {
		t.Fatalf("claimed prompt truncated: %d bytes, want %d", len(claimed.Prompt), len(task.Prompt))
	}
}
