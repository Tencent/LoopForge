-- Execution-level counting keys outlive the 200-row execution history.
-- Additive DDL only is also covered by the normal incremental schema setup.
CREATE TABLE IF NOT EXISTS schedule_job_execution_counted (
  execution_id VARCHAR(80) NOT NULL PRIMARY KEY,
  job_id VARCHAR(64) NOT NULL,
  bucket_hour DATETIME(3) NOT NULL,
  INDEX idx_schedule_execution_counted_job_bucket (job_id, bucket_hour)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Upgrade step: stop all schedule writers, run this script before starting
-- the upgraded service, then resume writers. Incremental schema setup alone
-- does not seed these keys. This never changes hourly aggregates.
-- Runtime mutation also seeds retained terminal rows without counting twice.
-- Without this manual backfill, a pre-upgrade retained record pruned before
-- its next rewrite also has no key and may be counted again on replay.
-- IDs already trimmed before upgrade cannot be recovered or deduplicated
-- retrospectively. No historical token usage or statistics are reconstructed.
-- A previously counted, pre-upgrade ID trimmed within the last 30 days can
-- therefore still count once more on its first replay after upgrade.
INSERT IGNORE INTO schedule_job_execution_counted (execution_id, job_id, bucket_hour)
SELECT id, job_id,
       CAST(DATE_FORMAT(started_at, '%Y-%m-%d %H:00:00') AS DATETIME)
FROM schedule_job_executions
WHERE status IS NOT NULL AND status NOT IN ('', 'running')
  AND started_at IS NOT NULL
  AND started_at >= UTC_TIMESTAMP(3) - INTERVAL 30 DAY;

-- Keys use the same 30-day bucket retention as hourly statistics. Expired
-- replays do not recreate expired statistics. Deleting a job follows the
-- existing policy of deleting both its execution history and statistics.
