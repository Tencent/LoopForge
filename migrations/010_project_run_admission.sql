-- 010: SOP run admission (project limit 10, optional personal-machine limit).
-- Apply manually, in order, BEFORE rolling out this version.
--
-- Rules recap:
-- - Platform (cloud) runs count per project; empty project shares one
--   implicit bucket ("project:"). No public project is inserted.
-- - Runs on a personal machine (runners.owner_user_id > 0) skip the project
--   limit and count per machine ("runner:<id>") only when its owner sets
--   runners.run_limit. NULL keeps every existing machine unlimited.
-- - Started failed/waiting/cancelled runs keep their slot; existing runs above
--   the limit are not interrupted (new runs queue until it drops below).
--
-- Schema (sections 1-2) is idempotent with gorm AutoMigrate: if a dev/test
-- binary already added a column or index, skip that statement. MySQL 8.0 has
-- no ADD COLUMN IF NOT EXISTS, so check information_schema first if unsure.

-- 1. Per-bucket admission lock rows ("project:<id>" / "runner:<id>").
CREATE TABLE IF NOT EXISTS run_admission_locks (
  id VARCHAR(128) NOT NULL PRIMARY KEY
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 2. Run quota identity and queue state.
ALTER TABLE runs
  ADD COLUMN quota_bucket VARCHAR(128) NULL,
  ADD COLUMN quota_project_id VARCHAR(64) NULL,
  ADD COLUMN quota_parent_run_id VARCHAR(64) NULL,
  ADD COLUMN quota_exempt BOOLEAN NULL,
  ADD COLUMN admission_state VARCHAR(32) NULL,
  ADD COLUMN queue_entered_at DATETIME(3) NULL,
  ADD INDEX idx_runs_quota_bucket (quota_bucket, status, queue_entered_at);

ALTER TABLE runners
  ADD COLUMN run_limit INT NULL;

-- 3. Backfill historical runs (data migration; keeps every run's status).
--    Project: execution_context.projectId, else the linked issue's project.
--    Exempt: the run's frozen execution machine is personal.
--    Rows left NULL (e.g. written by the old binary during rollout) are still
--    resolved by the application at read time; re-running this block is safe.
UPDATE runs r
LEFT JOIN issues i
  ON i.id = NULLIF(JSON_UNQUOTE(JSON_EXTRACT(r.execution_context, '$.issueId')), '')
LEFT JOIN runners m
  ON m.id = NULLIF(JSON_UNQUOTE(JSON_EXTRACT(r.execution_context, '$.executionRunnerId')), '')
SET
  r.quota_project_id = COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(r.execution_context, '$.projectId')), ''), i.project_id, ''),
  r.quota_parent_run_id = COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(r.execution_context, '$.parentRunId')), ''), ''),
  r.quota_exempt = (m.owner_user_id IS NOT NULL AND m.owner_user_id > 0)
WHERE r.quota_bucket IS NULL;

UPDATE runs
SET quota_bucket = IF(quota_exempt,
  CONCAT('runner:', COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(execution_context, '$.executionRunnerId')), ''), '')),
  CONCAT('project:', quota_project_id))
WHERE quota_bucket IS NULL;

-- 4. Upgrade from an earlier build of this branch (only if it was deployed):
--    the old single-lock row is unused and the old index is superseded.
-- DELETE FROM run_admission_locks WHERE id = 'admission';
-- ALTER TABLE runs DROP INDEX idx_runs_admission;
-- ALTER TABLE run_admission_locks MODIFY id VARCHAR(128) NOT NULL;
