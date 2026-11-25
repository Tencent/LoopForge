package repo

import (
	"errors"
	"testing"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

func TestRunnerLeaseOperationsDistinguishDatabaseFailureFromLostLease(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	now := time.Now().UTC()
	if err := st.PutRunnerTask(domain.RunnerTask{ID: "runner-read", Provider: "mock"}); err != nil {
		t.Fatal(err)
	}
	task, found, err := st.ClaimRunnerTask("runner-owner", []string{"mock"}, now, time.Minute)
	if err != nil || !found {
		t.Fatalf("claim found=%t, error=%v", found, err)
	}

	readError := errors.New("transient lease read failure")
	failRead := true
	if err := st.DB.Callback().Query().Before("gorm:query").Register("test_runner_read_failure", func(tx *gorm.DB) {
		if failRead && tx.Statement.Table == "runner_tasks" {
			_ = tx.AddError(readError)
		}
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"start", func() error { return st.StartRunnerTask(task.ID, "runner-owner", now) }},
		{"renew", func() error {
			_, err := st.RenewRunnerTaskLease(task.ID, "runner-owner", time.Minute, now)
			return err
		}},
		{"complete", func() error {
			return st.CompleteRunnerTask(task.ID, "runner-owner", domain.RunnerTaskSuccess, nil, "", "", nil, nil, now)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !errors.Is(err, readError) {
				t.Errorf("operation error = %v, want original database failure", err)
			}
		})
	}
	failRead = false
	if _, err := st.RenewRunnerTaskLease(task.ID, "wrong-owner", time.Minute, now); !errors.Is(err, ErrRunnerTaskLeaseLost) {
		t.Fatalf("wrong owner error = %v, want lost lease", err)
	}
	if _, err := st.RenewRunnerTaskLease(task.ID, "runner-owner", time.Minute, now); err != nil {
		t.Fatalf("owner could not renew after recovery: %v", err)
	}
}
