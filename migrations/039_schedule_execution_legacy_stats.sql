-- MySQL 8.0.16+; execute manually with the platform stopped and a database backup.
-- Requires the current schedule_job_execution_stats columns (additive AutoMigrate).
-- Backfill only missing hourly buckets from retained, finished legacy executions.
-- Existing buckets (including records removed by retention) are never overwritten.
-- Reject partial existing buckets and missing buckets with usage: those require
-- execution-level reconciliation, not an additive or estimated backfill.
-- Unreported legacy Token usage stays unknown; no Token usage is reconstructed.
-- Stop on the first SQL error; do not execute with mysql --force.

SET @legacy_stats_from = UTC_TIMESTAMP() - INTERVAL 30 DAY;
START TRANSACTION;

CREATE TEMPORARY TABLE legacy_schedule_stats AS
SELECT
    job_id,
    MAX(project_id) AS project_id,
    CAST(DATE_FORMAT(started_at, '%Y-%m-%d %H:00:00') AS DATETIME) AS bucket_hour,
    COUNT(*) AS finished,
    SUM(status = 'success') AS success,
    SUM(status = 'failed') AS failed,
    SUM(COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.durationMs')) AS SIGNED), 0) > 0) AS timed,
    SUM(GREATEST(COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.durationMs')) AS SIGNED), 0), 0)) AS duration_ms,
    SUM(COALESCE(JSON_TYPE(JSON_EXTRACT(payload, '$.usage')) = 'OBJECT', 0)) AS with_usage
FROM schedule_job_executions
WHERE started_at >= @legacy_stats_from
  AND COALESCE(status, '') NOT IN ('', 'running')
GROUP BY job_id, bucket_hour;

CREATE TEMPORARY TABLE legacy_schedule_stats_guard (
    passed TINYINT NOT NULL CHECK (passed = 1)
);
INSERT INTO legacy_schedule_stats_guard (passed)
SELECT IF(COUNT(*) = 0, 1, 0)
FROM legacy_schedule_stats r
LEFT JOIN schedule_job_execution_stats s
  ON s.job_id = r.job_id AND s.bucket_hour = r.bucket_hour
WHERE (s.job_id IS NOT NULL AND s.finished < r.finished)
   OR (s.job_id IS NULL AND r.with_usage > 0);

CREATE TEMPORARY TABLE legacy_schedule_stats_missing AS
SELECT r.*
FROM legacy_schedule_stats r
LEFT JOIN schedule_job_execution_stats s
  ON s.job_id = r.job_id AND s.bucket_hour = r.bucket_hour
WHERE s.job_id IS NULL
  AND EXISTS (SELECT 1 FROM legacy_schedule_stats_guard WHERE passed = 1);

INSERT INTO schedule_job_execution_stats (
    job_id, bucket_hour, project_id, finished, success, failed, timed, duration_ms,
    metered, tokens_total, tokens_input, tokens_output, tokens_cache_read,
    tokens_cache_write, priced, cost_usd
)
SELECT
    job_id, bucket_hour, project_id, finished, success, failed,
    timed, duration_ms, 0, 0, 0, 0, 0, 0, 0, 0
FROM legacy_schedule_stats_missing;

SELECT ROW_COUNT() AS restored_hour_buckets;
DROP TEMPORARY TABLE legacy_schedule_stats_missing;
DROP TEMPORARY TABLE legacy_schedule_stats_guard;
DROP TEMPORARY TABLE legacy_schedule_stats;
COMMIT;
