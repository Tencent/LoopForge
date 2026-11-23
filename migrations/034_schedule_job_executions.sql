-- 034 schedule job execution history (定时任务执行记录看板).
-- Optional manual additive DDL: startup AutoMigrate creates the same tables.
-- No data migration and no seed. Jobs keep their last* fields; history starts
-- with the first execution after deployment. Each job keeps its latest 200
-- executions; deleting a job deletes its executions and statistics.

CREATE TABLE IF NOT EXISTS schedule_job_executions (
  id VARCHAR(80) NOT NULL PRIMARY KEY,
  job_id VARCHAR(64),
  project_id VARCHAR(64),
  trace_id VARCHAR(64),
  status VARCHAR(32),
  payload JSON,
  started_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  INDEX idx_schedule_job_executions_job (job_id),
  INDEX idx_schedule_job_executions_project (project_id),
  INDEX idx_schedule_job_executions_trace (trace_id),
  INDEX idx_schedule_job_executions_started (started_at)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Hourly counts of finished executions (UTC hour they started in), kept 30
-- days. The dashboard overview ("最近 7 天 / 较上周") reads these, so it stays
-- exact when a frequent job has more executions than the 200 it keeps.
CREATE TABLE IF NOT EXISTS schedule_job_execution_stats (
  job_id VARCHAR(64) NOT NULL,
  bucket_hour DATETIME(3) NOT NULL,
  project_id VARCHAR(64),
  finished BIGINT NOT NULL DEFAULT 0,
  success BIGINT NOT NULL DEFAULT 0,
  failed BIGINT NOT NULL DEFAULT 0,
  timed BIGINT NOT NULL DEFAULT 0,
  duration_ms BIGINT NOT NULL DEFAULT 0,
  metered BIGINT NOT NULL DEFAULT 0,
  tokens_total BIGINT NOT NULL DEFAULT 0,
  tokens_input BIGINT NOT NULL DEFAULT 0,
  tokens_output BIGINT NOT NULL DEFAULT 0,
  tokens_cache_read BIGINT NOT NULL DEFAULT 0,
  tokens_cache_write BIGINT NOT NULL DEFAULT 0,
  priced BIGINT NOT NULL DEFAULT 0,
  cost_usd DOUBLE NOT NULL DEFAULT 0,
  PRIMARY KEY (job_id, bucket_hour),
  INDEX idx_schedule_job_execution_stats_project (project_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
