package repo

import (
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
)

// countScheduleJobExecutionOnce shares the execution write's transaction. The
// key survives history pruning and admits only one writer to the hourly bucket.
func countScheduleJobExecutionOnce(tx *gorm.DB, e domain.ScheduleJobExecution, alreadyCounted bool, now time.Time) error {
	if e.StartedAt.IsZero() {
		e.StartedAt = now
	}
	hour := e.StartedAt.UTC().Truncate(time.Hour)
	if hour.Before(now.Add(-scheduleExecutionStatsRetention)) {
		// An old replay must not recreate a bucket whose counting key expired.
		return nil
	}
	inserted, err := insertIgnore(tx, "schedule_job_execution_counted", map[string]any{
		"execution_id": e.ID,
		"job_id":       e.JobID,
		"bucket_hour":  hour,
	})
	if err != nil {
		return err
	}
	// Seeding a key for a retained pre-upgrade terminal row is not a new
	// execution. Missing history/aggregates cannot be reconstructed here.
	if !inserted || alreadyCounted {
		return nil
	}
	return countScheduleJobExecution(tx, e)
}
