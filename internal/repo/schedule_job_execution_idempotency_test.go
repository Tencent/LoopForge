package repo

// Regression tests for scheduled execution counting across history retention.
// These use the real CatalogRepo and SQLite store to verify pruning /
// replay counting on the real CatalogRepo + OpenTest store.

import (
	"fmt"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func newExecutionCountingJob(t *testing.T, st *Store, prefix string) string {
	t.Helper()
	id := fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	if err := st.PutScheduleJob(domain.ScheduleJob{ID: id, Name: "audit", ProjectID: "platform", SkipDispatch: true, AssistantPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DeleteScheduleJob(id) })
	return id
}

func putCountingExecution(t *testing.T, st *Store, jobID, trace string, started time.Time) domain.ScheduleJobExecution {
	t.Helper()
	e := domain.ScheduleJobExecution{JobID: jobID, ProjectID: "platform", TraceID: trace, Status: "running", StartedAt: started}
	if err := st.PutScheduleJobExecution(e); err != nil {
		t.Fatal(err)
	}
	return e
}

func finishCountingExecution(t *testing.T, st *Store, e domain.ScheduleJobExecution, status string) {
	t.Helper()
	fin := e.StartedAt.Add(time.Second)
	e.Status = status
	e.FinishedAt = &fin
	e.DurationMs = 1000
	if status == "failed" {
		e.Error = "boom"
	}
	if err := st.PutScheduleJobExecution(e); err != nil {
		t.Fatal(err)
	}
}

func countingBool(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Reproduces the hypothesis: a finished execution is counted, then pruned from
// the 200-row history, then the same terminal state is replayed for the same
// execution ID. The prior status is gone with the row, so PutScheduleJobExecution
// treats it as a brand-new execution and increments the hourly bucket again.
func TestScheduleExecutionPrunedTerminalReplayCountsOnce(t *testing.T) {
	for _, status := range []string{"failed", "success"} {
		t.Run(status, func(t *testing.T) {
			st, err := OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			jobID := newExecutionCountingJob(t, st, "audit-replay")
			now := time.Now().UTC()
			target := putCountingExecution(t, st, jobID, "target", now.Add(-2*time.Hour))
			finishCountingExecution(t, st, target, status)

			if cur, _ := st.ScheduleJobExecutionOverview([]string{jobID}, now); cur.Finished != 1 {
				t.Fatalf("baseline overview = %+v, want Finished=1", cur)
			}

			// Push the finished execution out of the 200-row window, exactly as the
			// real prune does (newest kept, oldest deleted).
			for i := 0; i < MaxScheduleJobExecutionsPerJob; i++ {
				started := now.Add(-90 * time.Minute).Add(time.Duration(i) * time.Second)
				putCountingExecution(t, st, jobID, fmt.Sprintf("filler-%03d", i), started)
			}
			id := domain.ScheduleJobExecutionID(target.TraceID)
			if _, ok := st.ScheduleJobExecution(id); ok {
				t.Fatalf("target execution still retained; prune did not happen")
			}
			if cur, _ := st.ScheduleJobExecutionOverview([]string{jobID}, now); cur.Finished != 1 {
				t.Fatalf("pruning alone changed the count: %+v, want Finished=1", cur)
			}

			// Replay the same terminal state for the pruned execution ID.
			replay := target
			finishCountingExecution(t, st, replay, status)

			cur, _ := st.ScheduleJobExecutionOverview([]string{jobID}, now)
			var stat scheduleJobExecutionStatRow
			_ = st.DB.Where("job_id = ?", jobID).Take(&stat).Error
			wantFailed, wantSuccess := countingBool(status == "failed"), countingBool(status == "success")
			if cur.Finished != 1 || cur.Failed != wantFailed || cur.Success != wantSuccess {
				t.Fatalf("REPRODUCED double count: replayed pruned %s execution counted twice: stat.Finished=%d stat.Failed=%d stat.Success=%d overview=%+v", status, stat.Finished, stat.Failed, stat.Success, cur)
			}
		})
	}
}

// Control: while the finished row is still retained, rewriting the same terminal
// state does NOT count again (the prior status guard works).
func TestScheduleExecutionRetainedTerminalRewriteCountsOnce(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	jobID := newExecutionCountingJob(t, st, "audit-kept")
	now := time.Now().UTC()
	e := putCountingExecution(t, st, jobID, "kept", now.Add(-time.Hour))
	finishCountingExecution(t, st, e, "failed")
	finishCountingExecution(t, st, e, "failed") // same ID, row still kept: prior status=failed
	cur, _ := st.ScheduleJobExecutionOverview([]string{jobID}, now)
	if cur.Finished != 1 || cur.Failed != 1 {
		t.Fatalf("retained rewrite recounted: overview=%+v, want Finished=1 Failed=1", cur)
	}
}

// Sequential duplicate terminal writes without pruning also stay at one count.
func TestScheduleExecutionSequentialTerminalWritesCountOnce(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	jobID := newExecutionCountingJob(t, st, "audit-dup")
	now := time.Now().UTC()
	e := putCountingExecution(t, st, jobID, "dup", now.Add(-time.Hour))
	for i := 0; i < 5; i++ {
		finishCountingExecution(t, st, e, "success")
	}
	cur, _ := st.ScheduleJobExecutionOverview([]string{jobID}, now)
	if cur.Finished != 1 || cur.Success != 1 {
		t.Fatalf("sequential duplicate recounted: overview=%+v, want Finished=1 Success=1", cur)
	}
}

func TestScheduleExecutionLegacyTerminalSeedsKeyWithoutRecount(t *testing.T) {
	st := openExecutionTestStore(t, "sqlite")
	jobID := newExecutionCountingJob(t, st, "legacy")
	now := time.Now().UTC()
	e := putCountingExecution(t, st, jobID, "legacy", now.Add(-time.Hour))
	finishCountingExecution(t, st, e, "success")
	// Emulate an upgrade: the retained terminal row and its aggregate exist,
	// but the newly added counting-key table is empty.
	if err := st.DB.Where("job_id = ?", jobID).Delete(&scheduleJobExecutionCountedRow{}).Error; err != nil {
		t.Fatal(err)
	}
	finishCountingExecution(t, st, e, "success")
	current, _ := st.ScheduleJobExecutionOverview([]string{jobID}, now)
	var keys int64
	if err := st.DB.Model(&scheduleJobExecutionCountedRow{}).Where("job_id = ?", jobID).Count(&keys).Error; err != nil {
		t.Fatal(err)
	}
	if current.Finished != 1 || current.Success != 1 || keys != 1 {
		t.Fatalf("legacy terminal rewrite: overview=%+v keys=%d", current, keys)
	}
}

func TestScheduleExecutionCountingRollbackAllowsRetry(t *testing.T) {
	st := openExecutionTestStore(t, "sqlite")
	jobID := newExecutionCountingJob(t, st, "rollback")
	e := putCountingExecution(t, st, jobID, "rollback", time.Now().UTC())
	if err := st.DB.Exec(`CREATE TRIGGER reject_execution_stats BEFORE INSERT ON schedule_job_execution_stats
		BEGIN SELECT RAISE(FAIL, 'statistics unavailable'); END`).Error; err != nil {
		t.Fatal(err)
	}
	e.Status = "success"
	if err := st.PutScheduleJobExecution(e); err == nil {
		t.Fatal("statistics write should fail")
	}
	var keys int64
	if err := st.DB.Model(&scheduleJobExecutionCountedRow{}).Where("job_id = ?", jobID).Count(&keys).Error; err != nil {
		t.Fatal(err)
	}
	stored, _ := st.ScheduleJobExecution(domain.ScheduleJobExecutionID(e.TraceID))
	if keys != 0 || stored.Status != "running" {
		t.Fatalf("failed transaction leaked a key or terminal state: keys=%d execution=%+v", keys, stored)
	}
	if err := st.DB.Exec("DROP TRIGGER reject_execution_stats").Error; err != nil {
		t.Fatal(err)
	}
	if err := st.PutScheduleJobExecution(e); err != nil {
		t.Fatal(err)
	}
	current, _ := st.ScheduleJobExecutionOverview([]string{jobID}, time.Now().UTC())
	if current.Finished != 1 || current.Success != 1 {
		t.Fatalf("retry was not counted once: %+v", current)
	}
}

func TestScheduleExecutionExpiredTerminalDoesNotRecreateStatistics(t *testing.T) {
	st := openExecutionTestStore(t, "sqlite")
	jobID := newExecutionCountingJob(t, st, "expired")
	e := putCountingExecution(t, st, jobID, "expired", time.Now().UTC().Add(-40*24*time.Hour))
	finishCountingExecution(t, st, e, "success")
	for _, table := range []string{"schedule_job_execution_counted", "schedule_job_execution_stats"} {
		var count int64
		if err := st.DB.Table(table).Where("job_id = ?", jobID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("expired terminal created %d rows in %s", count, table)
		}
	}
}

func TestScheduleExecutionZeroStartKeepsCountingKeyAndBucketAligned(t *testing.T) {
	st := openExecutionTestStore(t, "sqlite")
	jobID := newExecutionCountingJob(t, st, "zero")
	e := putCountingExecution(t, st, jobID, "zero", time.Time{})
	finishCountingExecution(t, st, e, "success")
	finishCountingExecution(t, st, e, "success")
	var key scheduleJobExecutionCountedRow
	if err := st.DB.Where("job_id = ?", jobID).Take(&key).Error; err != nil {
		t.Fatal(err)
	}
	var stats scheduleJobExecutionStatRow
	if err := st.DB.Where("job_id = ?", jobID).Take(&stats).Error; err != nil {
		t.Fatal(err)
	}
	if !key.BucketHour.Equal(stats.BucketHour) || stats.Finished != 1 {
		t.Fatalf("counting key and bucket differ: key=%+v stats=%+v", key, stats)
	}
}

func TestScheduleExecutionCountingKeysFollowRetentionAndJobDeletion(t *testing.T) {
	st := openExecutionTestStore(t, "sqlite")
	jobID := newExecutionCountingJob(t, st, "retention")
	now := time.Now().UTC()
	e := putCountingExecution(t, st, jobID, "recent", now)
	finishCountingExecution(t, st, e, "success")
	oldHour := now.Add(-31 * 24 * time.Hour).Truncate(time.Hour)
	if err := st.DB.Create(&scheduleJobExecutionCountedRow{ExecutionID: "old", JobID: jobID, BucketHour: oldHour}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB.Create(&scheduleJobExecutionStatRow{JobID: jobID, BucketHour: oldHour, Finished: 1, Success: 1}).Error; err != nil {
		t.Fatal(err)
	}
	putCountingExecution(t, st, jobID, "next", now.Add(time.Second))
	for _, table := range []string{"schedule_job_execution_counted", "schedule_job_execution_stats"} {
		var count int64
		if err := st.DB.Table(table).Where("job_id = ?", jobID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("retention left %d rows in %s, want the recent row only", count, table)
		}
	}
	if err := st.DeleteScheduleJob(jobID); err != nil {
		t.Fatal(err)
	}
	var keys int64
	if err := st.DB.Model(&scheduleJobExecutionCountedRow{}).Where("job_id = ?", jobID).Count(&keys).Error; err != nil || keys != 0 {
		t.Fatalf("deleted job left counting keys: count=%d err=%v", keys, err)
	}
}
