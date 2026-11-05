-- Manual, idempotent MySQL migration for assistant conversation sources.
-- Run it in a low-traffic window: on a large assistant_sessions table the
-- index build is a blocking DDL. Startup incremental DDL
-- (internal/repo/automigrate.go) adds the same columns/index when they are
-- missing, so an environment that already started the new binary can skip the
-- DDL below and run only the backfill.
--
-- Nothing here deletes rows: pruning old scheduled conversations stays in the
-- running service (AGENTFLOW_ASSISTANT_SESSION_JANITOR).
--
-- source is chat | sop | scheduled. Legacy rows keep an empty source until this
-- script runs; the sidebar falls back to the historical "定时 · " title prefix.

SET @asst_source_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema=DATABASE() AND table_name='assistant_sessions' AND column_name='source')=0,
 'ALTER TABLE assistant_sessions ADD COLUMN source VARCHAR(32) NULL',
 'SELECT 1');
PREPARE asst_source_stmt FROM @asst_source_ddl;
EXECUTE asst_source_stmt;
DEALLOCATE PREPARE asst_source_stmt;

SET @asst_task_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema=DATABASE() AND table_name='assistant_sessions' AND column_name='task_id')=0,
 'ALTER TABLE assistant_sessions ADD COLUMN task_id VARCHAR(64) NULL',
 'SELECT 1');
PREPARE asst_task_stmt FROM @asst_task_ddl;
EXECUTE asst_task_stmt;
DEALLOCATE PREPARE asst_task_stmt;

SET @asst_source_index_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.statistics
  WHERE table_schema=DATABASE() AND table_name='assistant_sessions' AND index_name='idx_assistant_sessions_source')=0,
 'CREATE INDEX idx_assistant_sessions_source ON assistant_sessions (source)',
 'SELECT 1');
PREPARE asst_source_index_stmt FROM @asst_source_index_ddl;
EXECUTE asst_source_index_stmt;
DEALLOCATE PREPARE asst_source_index_stmt;

-- 1. SOP run conversations: shared and bound to a run.
UPDATE assistant_sessions
SET source = 'sop'
WHERE (source IS NULL OR source = '')
  AND COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.shared')), 'false') IN ('true', '1')
  AND COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.runId')), '') <> '';

-- 2. Scheduled task conversations: still carry the historical title prefix and
-- were never renamed by the user. Shared run conversations are marked above,
-- so they cannot be caught here.
UPDATE assistant_sessions
SET source = 'scheduled'
WHERE (source IS NULL OR source = '')
  AND title LIKE '定时 · %'
  AND COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.titleEdited')), 'false') NOT IN ('true', '1')
  AND COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.shared')), 'false') NOT IN ('true', '1');

-- 3. Everything else is an ordinary conversation.
UPDATE assistant_sessions
SET source = 'chat'
WHERE source IS NULL OR source = '';

-- Both backfills leave updated_at untouched, so the sidebar order is preserved.
-- Re-running the script is safe: every statement is guarded by an empty source.

-- Optional, second pass: recover task_id for the historical scheduled rows so
-- the janitor can keep the latest run per task instead of treating them as
-- untracked (untracked rows simply follow the plain 3-day retention).
-- The title join is only correct when task names are unique, and rows whose
-- title was truncated to 128 chars will not match. Check for duplicates first
-- and only then uncomment the UPDATE.
SELECT name, COUNT(*) AS jobs FROM schedule_jobs GROUP BY name HAVING COUNT(*) > 1;

-- UPDATE assistant_sessions AS s
-- JOIN schedule_jobs AS j ON s.title = CONCAT('定时 · ', j.name)
-- SET s.task_id = j.id
-- WHERE s.source = 'scheduled'
--   AND (s.task_id IS NULL OR s.task_id = '');
