-- 033 schedule job execution history (定时任务执行记录看板).
-- Optional manual additive DDL: startup AutoMigrate creates the same table.
-- No data migration and no seed. Jobs keep their last* fields; history starts
-- with the first execution after deployment. Each job keeps its latest 200
-- executions; deleting a job deletes its executions.

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
