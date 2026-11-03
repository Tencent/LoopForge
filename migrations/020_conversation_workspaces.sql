-- Manual, idempotent MySQL migration. Stop old API/workers, run this SQL,
-- then start upgraded API/workers/runners together.
-- Retains all history, snapshots, files and old role collaboration records.
SET @workspace_kind_exists = (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'runs' AND COLUMN_NAME = 'kind'
);
SET @workspace_kind_ddl = IF(@workspace_kind_exists = 0,
  'ALTER TABLE runs ADD COLUMN kind VARCHAR(32) NULL', 'SELECT 1');
PREPARE workspace_kind_stmt FROM @workspace_kind_ddl;
EXECUTE workspace_kind_stmt;
DEALLOCATE PREPARE workspace_kind_stmt;

-- Only bound ordinary chat workspaces are converted. An actual old group with
-- roles or dispatched nodes is cancelled below.
UPDATE runs AS r
JOIN assistant_sessions AS s
  ON JSON_UNQUOTE(JSON_EXTRACT(s.payload, '$.workspaceRunId')) = r.id
SET r.kind = 'workspace'
WHERE r.workflow_id = 'free-collaboration'
  AND COALESCE(JSON_UNQUOTE(JSON_EXTRACT(s.payload, '$.runId')), '') = ''
  AND COALESCE(JSON_LENGTH(JSON_EXTRACT(r.snapshot, '$.groupMembers')), 0) = 0
  AND NOT EXISTS (SELECT 1 FROM run_nodes AS n WHERE n.run_id = r.id);

-- Every other role group is cancelled. Its shared conversation stays an
-- ordinary task conversation of a finished run; history is not deleted.
-- Statements are ordered so that each one still sees the unfinished groups.
UPDATE runner_tasks AS t
JOIN runs AS r ON r.id = JSON_UNQUOTE(JSON_EXTRACT(t.payload, '$.runId'))
SET t.desired_state = 'cancel', t.updated_at = UTC_TIMESTAMP()
WHERE COALESCE(r.kind, '') <> 'workspace'
  AND JSON_UNQUOTE(JSON_EXTRACT(r.snapshot, '$.collaborationMode')) = 'group'
  AND r.status NOT IN ('succeeded', 'failed', 'cancelled')
  AND t.status IN ('leased', 'running');

UPDATE runner_tasks AS t
JOIN runs AS r ON r.id = JSON_UNQUOTE(JSON_EXTRACT(t.payload, '$.runId'))
SET t.status = 'canceled', t.lease_owner = '', t.lease_until = NULL, t.desired_state = '', t.updated_at = UTC_TIMESTAMP()
WHERE COALESCE(r.kind, '') <> 'workspace'
  AND JSON_UNQUOTE(JSON_EXTRACT(r.snapshot, '$.collaborationMode')) = 'group'
  AND r.status NOT IN ('succeeded', 'failed', 'cancelled')
  AND t.status = 'queued';

UPDATE run_nodes AS n
JOIN runs AS r ON r.id = n.run_id
SET n.status = 'cancelled',
    n.payload = JSON_SET(n.payload, '$.status', 'cancelled', '$.error', '', '$.activity', '旧角色协作已退出，自动取消',
      '$.finishedAt', DATE_FORMAT(UTC_TIMESTAMP(), '%Y-%m-%dT%H:%i:%sZ'))
WHERE COALESCE(r.kind, '') <> 'workspace'
  AND JSON_UNQUOTE(JSON_EXTRACT(r.snapshot, '$.collaborationMode')) = 'group'
  AND r.status NOT IN ('succeeded', 'failed', 'cancelled')
  AND n.status IN ('running', 'pending_input', 'waiting_approval');

UPDATE runs
SET status = 'cancelled', cancel_requested = 0, pause_requested = 0,
    owner_instance_id = '', lease_until = NULL, revision = revision + 1, updated_at = UTC_TIMESTAMP()
WHERE COALESCE(kind, '') <> 'workspace'
  AND JSON_UNQUOTE(JSON_EXTRACT(snapshot, '$.collaborationMode')) = 'group'
  AND status NOT IN ('succeeded', 'failed', 'cancelled');

-- Legacy group follow-up/handoff tables are intentionally kept for audit;
-- new processes no longer poll or execute their rows.
