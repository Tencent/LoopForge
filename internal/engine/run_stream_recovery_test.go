package engine

import (
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"gorm.io/gorm"
)

func TestRunStreamRecoversFromTransientPollReadErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(*gorm.Statement) bool
	}{
		{"revision", func(stmt *gorm.Statement) bool {
			return stmt.Table == "runs" && slices.Contains(stmt.Selects, "revision")
		}},
		{"payload", func(stmt *gorm.Statement) bool {
			return stmt.Table == "run_logs"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			const id = "stream-read-recovery"
			if err := s.PutRun(domain.Run{ID: id, Status: "waiting", Nodes: map[string]*domain.NodeRun{}}); err != nil {
				t.Fatal(err)
			}
			var failNext atomic.Bool
			pollFailed := make(chan struct{})
			if err := s.DB.Callback().Query().Before("gorm:query").Register("fail_one_poll_read", func(tx *gorm.DB) {
				if tc.fail(tx.Statement) && failNext.CompareAndSwap(true, false) {
					_ = tx.AddError(errors.New("temporary database error"))
					close(pollFailed)
				}
			}); err != nil {
				t.Fatal(err)
			}

			e := New(s, nil)
			updates, cancel, err := e.Subscribe(id)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cancel)
			initial := <-updates
			failNext.Store(true)
			// Simulate a different platform process, which can't call
			// publishRun on the engine holding this subscription.
			if err := s.DB.Exec("UPDATE runs SET revision=revision+1, status='failed' WHERE id=?", id).Error; err != nil {
				t.Fatal(err)
			}
			select {
			case <-pollFailed:
			case <-time.After(3 * time.Second):
				t.Fatal("poll did not hit the injected error")
			}
			select {
			case got := <-updates:
				if got.Revision != initial.Revision+1 || got.Status != "failed" {
					t.Fatalf("unexpected update after recovery: revision=%d, status=%s", got.Revision, got.Status)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("subscription stopped polling after a transient read error")
			}
		})
	}
}
