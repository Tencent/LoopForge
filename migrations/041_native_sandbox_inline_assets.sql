-- Manual, idempotent MySQL migration: sandboxes carry their own Dockerfile,
-- native replaces local, and environment install scripts are gone.
--
-- A docker sandbox now stores payload.dockerfile {content, files}; a native
-- sandbox (seatbelt / bwrap) only has environment variables and uses the
-- toolchains of the machine it runs on. dockerfileId, environmentId and
-- install are gone, and so are the dockerfiles and environments tables with
-- their API. The local sandbox type is removed.
--
-- 1. Each docker sandbox copies the Dockerfile it referenced.
-- 2. A Dockerfile no docker sandbox used becomes a docker sandbox, keeping its
--    name, project and sharing. It keeps the Dockerfile ID when available, then
--    tries a "-docker" suffix and finally an unused numbered ID.
--    Built-in sandbox IDs are reserved even without a catalog row.
-- 3. Each local sandbox becomes a native sandbox: its execution machine,
--    checkout directory and repository paths are dropped, and it keeps its
--    environment variables.
-- 4. Native sandboxes keep ordinary variables from referenced environments,
--    overlaid by inline variables. Install scripts and PATH overrides are
--    dropped. Native commands use the execution machine's own PATH.
-- 5. SOPs using the old default move to generic-docker. Project profiles
--    occupying this reserved ID move along with their references. A local
--    workspace preference becomes native.
-- 6. The legacy tables are dropped.
--
-- Missing legacy tables are treated as empty so the migration can be repeated.
-- Docker images are keyed by the Dockerfile content and are reused.
-- Apply manually to existing databases before starting the upgraded platform.
-- For Compose, use ./start.sh migrate-sandboxes with the old platform stopped.

SET NAMES utf8mb4;

CREATE TABLE IF NOT EXISTS dockerfiles (
  `id` varchar(64) NOT NULL,
  `name` varchar(255) DEFAULT NULL,
  `payload` json DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`)
) DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS environments (
  `id` varchar(64) NOT NULL,
  `name` varchar(255) DEFAULT NULL,
  `payload` json DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`)
) DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Keep the additive DDL outside the data transaction. Startup AutoMigrate
-- also adds this column. Older schemas may have no preference table.
SET @assistant_preferences_exists = EXISTS (
  SELECT 1 FROM information_schema.tables
  WHERE table_schema = DATABASE() AND table_name = 'assistant_preferences');
SET @assistant_workspace_driver_sql = IF(
  @assistant_preferences_exists AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = DATABASE() AND table_name = 'assistant_preferences' AND column_name = 'workspace_driver'),
  'ALTER TABLE assistant_preferences ADD COLUMN workspace_driver VARCHAR(16) NOT NULL DEFAULT ''''',
  'DO 0');
PREPARE assistant_workspace_driver_stmt FROM @assistant_workspace_driver_sql;
EXECUTE assistant_workspace_driver_stmt;
DEALLOCATE PREPARE assistant_workspace_driver_stmt;

START TRANSACTION;

-- Old saves wrote "local" for the default, even when no sandbox row existed.
-- The Compose platform ran this default through Docker. Keep a configured
-- profile with this ID as a separate sandbox so its explicit settings survive.
SET @replace_stock_local_default = NOT EXISTS (
  SELECT 1 FROM sandboxes
  WHERE id = 'local'
    AND (
      COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.driver')), '') <> 'local'
      OR COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.projectId')), '') <> ''
      OR COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.environmentId')), '') <> ''
      OR COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.runnerId')), '') <> ''
      OR COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.baseDir')), '') <> ''
      OR COALESCE(JSON_LENGTH(JSON_EXTRACT(payload, '$.environment')), 0) > 0
      OR COALESCE(JSON_LENGTH(JSON_EXTRACT(payload, '$.repositoryPaths')), 0) > 0
    ));

-- Before this upgrade a project could use the generic-docker ID on an
-- unseeded installation. Reserve it for the platform and keep the project
-- profile (including its sharing policy) under a fresh ID. Rewrite explicit
-- references before making generic-docker the default for old SOPs.
SET @legacy_generic_sandbox_project = (
  SELECT JSON_UNQUOTE(JSON_EXTRACT(payload, '$.projectId')) FROM sandboxes
  WHERE id COLLATE utf8mb4_unicode_ci = 'generic-docker'
    AND COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.projectId')), '') <> ''
  LIMIT 1);
SET @legacy_generic_sandbox_id = IF(@legacy_generic_sandbox_project IS NOT NULL,
  CONCAT('sandbox-', REPLACE(UUID(), '-', '')), NULL);
SET @rewrite_sandbox_references = @replace_stock_local_default OR @legacy_generic_sandbox_id IS NOT NULL;

UPDATE sandboxes
SET id = @legacy_generic_sandbox_id,
    payload = JSON_SET(payload, '$.id', @legacy_generic_sandbox_id)
WHERE @legacy_generic_sandbox_id IS NOT NULL
  AND id COLLATE utf8mb4_unicode_ci = 'generic-docker';

-- The openssh-client fix of 031, for installs that copy an unmodified
-- generic-backend Dockerfile.
UPDATE dockerfiles
SET payload = JSON_SET(payload, '$.content', 'FROM node:22-bookworm\nRUN apt-get update && apt-get install -y --no-install-recommends git openssh-client python3 ripgrep ca-certificates curl && rm -rf /var/lib/apt/lists/*\nRUN npm install -g @openai/codex @anthropic-ai/claude-code @tencent-ai/codebuddy-code\nWORKDIR /workspace\n')
WHERE id = 'generic-backend'
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.content')) = 'FROM node:22-bookworm\nRUN apt-get update && apt-get install -y --no-install-recommends git python3 ripgrep ca-certificates curl && rm -rf /var/lib/apt/lists/*\nRUN npm install -g @openai/codex @anthropic-ai/claude-code @tencent-ai/codebuddy-code\nWORKDIR /workspace\n';

-- 1. Copy referenced Dockerfiles into their sandboxes.
UPDATE sandboxes s
JOIN dockerfiles d
  ON d.id = JSON_UNQUOTE(JSON_EXTRACT(s.payload, '$.dockerfileId')) COLLATE utf8mb4_unicode_ci
SET s.payload = JSON_SET(s.payload, '$.dockerfile', JSON_MERGE_PATCH(
  JSON_OBJECT('content', COALESCE(JSON_UNQUOTE(JSON_EXTRACT(d.payload, '$.content')), '')),
  JSON_OBJECT('files', JSON_EXTRACT(d.payload, '$.files'))))
WHERE JSON_UNQUOTE(JSON_EXTRACT(s.payload, '$.driver')) = 'docker'
  AND JSON_EXTRACT(s.payload, '$.dockerfile') IS NULL;

-- 2. Wrap unused Dockerfiles as sandboxes of their own.
INSERT INTO sandboxes (`id`, `name`, `payload`)
WITH unused_dockerfiles AS (
  SELECT legacy.id, legacy.name, legacy.payload,
    CASE
      WHEN legacy.id COLLATE utf8mb4_unicode_ci NOT IN ('generic-docker', 'local')
        AND NOT EXISTS (SELECT 1 FROM sandboxes s WHERE s.id = legacy.id COLLATE utf8mb4_unicode_ci)
        THEN legacy.id
      WHEN CHAR_LENGTH(legacy.id) <= 57
        AND CONCAT(legacy.id, '-docker') COLLATE utf8mb4_unicode_ci <> 'generic-docker'
        AND NOT EXISTS (SELECT 1 FROM sandboxes s WHERE s.id = CONCAT(legacy.id, '-docker') COLLATE utf8mb4_unicode_ci)
        AND NOT EXISTS (SELECT 1 FROM dockerfiles d WHERE d.id = CONCAT(legacy.id, '-docker') COLLATE utf8mb4_unicode_ci)
        THEN CONCAT(legacy.id, '-docker')
    END AS preferred_id
  FROM dockerfiles legacy
  WHERE NOT EXISTS (
    SELECT 1 FROM sandboxes s
    WHERE JSON_UNQUOTE(JSON_EXTRACT(s.payload, '$.driver')) = 'docker'
      AND JSON_UNQUOTE(JSON_EXTRACT(s.payload, '$.dockerfileId')) COLLATE utf8mb4_unicode_ci = legacy.id)
), pending_ids AS (
  SELECT id, ROW_NUMBER() OVER (ORDER BY id) AS slot
  FROM unused_dockerfiles
  WHERE preferred_id IS NULL
), numbered_ids AS (
  -- Each existing row can occupy at most one candidate. Include one more
  -- candidate per pending Dockerfile so every pending row gets a free ID.
  SELECT CONCAT('dockerfile-', ROW_NUMBER() OVER ()) COLLATE utf8mb4_unicode_ci AS sid
  FROM (
    SELECT 1 FROM sandboxes
    UNION ALL SELECT 1 FROM dockerfiles
    UNION ALL SELECT 1 FROM pending_ids
  ) occupied_and_pending
  WHERE EXISTS (SELECT 1 FROM pending_ids)
), available_ids AS (
  SELECT sid, ROW_NUMBER() OVER (ORDER BY CHAR_LENGTH(sid), sid) AS slot
  FROM numbered_ids n
  WHERE NOT EXISTS (SELECT 1 FROM sandboxes s WHERE s.id = n.sid)
    AND NOT EXISTS (SELECT 1 FROM dockerfiles d WHERE d.id = n.sid)
)
SELECT w.sid, w.name, JSON_MERGE_PATCH(
  JSON_OBJECT('id', w.sid, 'name', w.name, 'driver', 'docker', 'readOnlyRootFS', false,
    'dockerfile', JSON_MERGE_PATCH(
      JSON_OBJECT('content', COALESCE(JSON_UNQUOTE(JSON_EXTRACT(w.payload, '$.content')), '')),
      JSON_OBJECT('files', JSON_EXTRACT(w.payload, '$.files')))),
  JSON_OBJECT('projectId', JSON_EXTRACT(w.payload, '$.projectId')),
  JSON_OBJECT('shared', IF(JSON_EXTRACT(w.payload, '$.shared') = true, CAST('true' AS JSON), NULL)))
FROM (
  SELECT COALESCE(d.preferred_id, a.sid) AS sid, d.name, d.payload
  FROM unused_dockerfiles d
  LEFT JOIN pending_ids p ON p.id = d.id
  LEFT JOIN available_ids a ON a.slot = p.slot
) w;

-- 3. Local sandboxes become native sandboxes.
UPDATE sandboxes
SET payload = JSON_SET(JSON_REMOVE(payload, '$.runnerId', '$.baseDir', '$.repositoryPaths'),
  '$.driver', 'native', '$.readOnlyRootFS', false)
WHERE JSON_UNQUOTE(JSON_EXTRACT(payload, '$.driver')) = 'local';

-- 4. Copy the old environment's ordinary variables into each native sandbox.
-- Inline overrides used to win. PATH and references to the removed install
-- directory have no meaning in the native runtime.
UPDATE sandboxes s
JOIN environments e
  ON e.id = JSON_UNQUOTE(JSON_EXTRACT(s.payload, '$.environmentId')) COLLATE utf8mb4_unicode_ci
SET s.payload = JSON_SET(s.payload, '$.environment', JSON_MERGE_PATCH(
  COALESCE((
    SELECT JSON_OBJECTAGG(vars.variable_name,
      JSON_EXTRACT(e.payload, CONCAT('$.variables.', JSON_QUOTE(vars.variable_name))))
    FROM JSON_TABLE(JSON_KEYS(e.payload, '$.variables'), '$[*]'
      COLUMNS (variable_name TEXT PATH '$')) vars
    WHERE BINARY vars.variable_name <> 'PATH'
      AND NOT REGEXP_LIKE(JSON_UNQUOTE(JSON_EXTRACT(e.payload, CONCAT('$.variables.', JSON_QUOTE(vars.variable_name)))),
        '[$](PREFIX|AGENTFLOW_ENVIRONMENT_ROOT|[{](PREFIX|AGENTFLOW_ENVIRONMENT_ROOT)[}])', 'c')
  ), JSON_OBJECT()),
  IF(JSON_TYPE(JSON_EXTRACT(s.payload, '$.environment')) = 'OBJECT',
    JSON_EXTRACT(s.payload, '$.environment'), JSON_OBJECT())))
WHERE JSON_UNQUOTE(JSON_EXTRACT(s.payload, '$.driver')) IN ('native', 'seatbelt', 'bwrap');

-- Drop install scripts and PATH overrides of native sandboxes.
UPDATE sandboxes
SET payload = JSON_REMOVE(payload, '$.environment.PATH')
WHERE JSON_UNQUOTE(JSON_EXTRACT(payload, '$.driver')) IN ('native', 'seatbelt', 'bwrap')
  AND JSON_CONTAINS_PATH(payload, 'one', '$.environment.PATH');

UPDATE sandboxes
SET payload = JSON_REMOVE(payload, '$.install')
WHERE JSON_CONTAINS_PATH(payload, 'one', '$.install');

UPDATE sandboxes
SET payload = JSON_REMOVE(payload, '$.dockerfileId')
WHERE JSON_CONTAINS_PATH(payload, 'one', '$.dockerfileId');

UPDATE sandboxes
SET payload = JSON_REMOVE(payload, '$.environmentId')
WHERE JSON_CONTAINS_PATH(payload, 'one', '$.environmentId');

-- 5. Move the old default in SOPs, generic templates, and restorable revisions.
-- Stage the workflow definitions together to apply the same checks and updates.
DROP TEMPORARY TABLE IF EXISTS workflow_sandbox_upgrade;
CREATE TEMPORARY TABLE workflow_sandbox_upgrade (
  source ENUM('workflow', 'revision', 'template') NOT NULL,
  row_id VARCHAR(128) COLLATE utf8mb4_unicode_ci NOT NULL,
  definition JSON NOT NULL,
  old_sandbox_id VARCHAR(16) NULL,
  PRIMARY KEY (source, row_id)
) DEFAULT CHARSET=utf8mb4;

INSERT INTO workflow_sandbox_upgrade (source, row_id, definition)
SELECT 'workflow', id, payload FROM workflows
WHERE @rewrite_sandbox_references AND JSON_TYPE(payload) = 'OBJECT';

SET @generic_templates_exists = EXISTS (
  SELECT 1 FROM information_schema.tables
  WHERE table_schema = DATABASE() AND table_name = 'generic_templates');
SET @stage_generic_templates_sql = IF(@generic_templates_exists AND @rewrite_sandbox_references,
  'INSERT INTO workflow_sandbox_upgrade (source, row_id, definition)
   SELECT ''template'', id, JSON_EXTRACT(payload, ''$.Workflow'') FROM generic_templates
   WHERE JSON_TYPE(JSON_EXTRACT(payload, ''$.Workflow'')) = ''OBJECT''',
  'DO 0');
PREPARE stage_generic_templates_stmt FROM @stage_generic_templates_sql;
EXECUTE stage_generic_templates_stmt;
DEALLOCATE PREPARE stage_generic_templates_stmt;

SET @definition_revisions_exists = EXISTS (
  SELECT 1 FROM information_schema.tables
  WHERE table_schema = DATABASE() AND table_name = 'definition_revisions');
SET @stage_workflow_revisions_sql = IF(@definition_revisions_exists AND @rewrite_sandbox_references,
  'INSERT INTO workflow_sandbox_upgrade (source, row_id, definition)
   SELECT ''revision'', id, JSON_EXTRACT(payload, ''$.payload'') FROM definition_revisions
   WHERE kind = ''workflow'' AND JSON_TYPE(JSON_EXTRACT(payload, ''$.payload'')) = ''OBJECT''',
  'DO 0');
PREPARE stage_workflow_revisions_stmt FROM @stage_workflow_revisions_sql;
EXECUTE stage_workflow_revisions_stmt;
DEALLOCATE PREPARE stage_workflow_revisions_stmt;

UPDATE workflow_sandbox_upgrade m
SET m.old_sandbox_id = CASE
  WHEN @legacy_generic_sandbox_id IS NOT NULL AND (
    JSON_UNQUOTE(JSON_EXTRACT(m.definition, '$.sandboxProfileId')) COLLATE utf8mb4_unicode_ci = 'generic-docker'
    OR (COALESCE(JSON_UNQUOTE(JSON_EXTRACT(m.definition, '$.sandboxProfileId')), '') = ''
      AND EXISTS (
        SELECT 1 FROM JSON_TABLE(m.definition, '$.nodes[*]' COLUMNS (sandbox_id TEXT PATH '$.sandboxProfileId')) n
        WHERE n.sandbox_id COLLATE utf8mb4_unicode_ci = 'generic-docker')))
    THEN 'generic-docker'
  WHEN @replace_stock_local_default
    AND COALESCE(JSON_UNQUOTE(JSON_EXTRACT(m.definition, '$.sandboxProfileId')), '') COLLATE utf8mb4_unicode_ci IN ('', 'local')
    AND NOT EXISTS (
      SELECT 1 FROM JSON_TABLE(m.definition, '$.nodes[*]' COLUMNS (sandbox_id TEXT PATH '$.sandboxProfileId')) n
      WHERE COALESCE(n.sandbox_id, '') COLLATE utf8mb4_unicode_ci NOT IN ('', 'local'))
    THEN 'local'
END;

DELETE FROM workflow_sandbox_upgrade WHERE old_sandbox_id IS NULL;

UPDATE workflow_sandbox_upgrade m
SET m.definition = JSON_SET(
  CASE
    WHEN NOT EXISTS (
      SELECT 1 FROM JSON_TABLE(m.definition, '$.nodes[*]' COLUMNS (sandbox_id TEXT PATH '$.sandboxProfileId')) n
      WHERE n.sandbox_id COLLATE utf8mb4_unicode_ci = m.old_sandbox_id) THEN m.definition
    ELSE JSON_SET(m.definition, '$.nodes', (
      SELECT JSON_ARRAYAGG(
        IF(JSON_UNQUOTE(JSON_EXTRACT(n.node, '$.sandboxProfileId')) COLLATE utf8mb4_unicode_ci = m.old_sandbox_id,
          JSON_SET(n.node, '$.sandboxProfileId', IF(m.old_sandbox_id = 'local', 'generic-docker', @legacy_generic_sandbox_id)),
          n.node)
      ) OVER (ORDER BY n.position ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)
      FROM JSON_TABLE(m.definition, '$.nodes[*]' COLUMNS (position FOR ORDINALITY, node JSON PATH '$')) n
      LIMIT 1))
  END,
  '$.sandboxProfileId', IF(m.old_sandbox_id = 'local', 'generic-docker', @legacy_generic_sandbox_id));

UPDATE workflows w JOIN workflow_sandbox_upgrade m
  ON m.source = 'workflow' AND m.row_id = w.id COLLATE utf8mb4_unicode_ci
SET w.payload = m.definition;

SET @update_generic_templates_sql = IF(@generic_templates_exists AND @rewrite_sandbox_references,
  'UPDATE generic_templates t JOIN workflow_sandbox_upgrade m
     ON m.source = ''template'' AND m.row_id = t.id COLLATE utf8mb4_unicode_ci
   SET t.payload = JSON_SET(t.payload, ''$.Workflow'', m.definition)',
  'DO 0');
PREPARE update_generic_templates_stmt FROM @update_generic_templates_sql;
EXECUTE update_generic_templates_stmt;
DEALLOCATE PREPARE update_generic_templates_stmt;

SET @update_workflow_revisions_sql = IF(@definition_revisions_exists AND @rewrite_sandbox_references,
  'UPDATE definition_revisions r JOIN workflow_sandbox_upgrade m
     ON m.source = ''revision'' AND m.row_id = r.id COLLATE utf8mb4_unicode_ci
   SET r.payload = JSON_SET(r.payload, ''$.payload'', m.definition)',
  'DO 0');
PREPARE update_workflow_revisions_stmt FROM @update_workflow_revisions_sql;
EXECUTE update_workflow_revisions_stmt;
DEALLOCATE PREPARE update_workflow_revisions_stmt;
DROP TEMPORARY TABLE workflow_sandbox_upgrade;

-- Keep the fixed selection, catalog ID and frozen workspace identity aligned.
-- A previously frozen platform profile can share the old ID and keeps its
-- identity and selection. Only the moved project's profile changes ID.
SET @run_snapshot_exists = EXISTS (
  SELECT 1 FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'runs' AND column_name = 'snapshot');
SET @update_run_sandbox_sql = IF(@run_snapshot_exists AND @legacy_generic_sandbox_id IS NOT NULL,
  'UPDATE runs
   SET snapshot = CASE
     WHEN JSON_TYPE(JSON_EXTRACT(snapshot, ''$.sandboxSnapshot.profile'')) = ''OBJECT''
       THEN JSON_SET(snapshot, ''$.sandboxProfileId'', @legacy_generic_sandbox_id,
         ''$.sandboxSnapshot.profile.id'', @legacy_generic_sandbox_id)
     ELSE JSON_SET(snapshot, ''$.sandboxProfileId'', @legacy_generic_sandbox_id)
   END
   WHERE JSON_UNQUOTE(JSON_EXTRACT(snapshot, ''$.sandboxProfileId'')) COLLATE utf8mb4_unicode_ci = ''generic-docker''
     AND (COALESCE(JSON_TYPE(JSON_EXTRACT(snapshot, ''$.sandboxSnapshot'')), ''NULL'') = ''NULL''
       OR (JSON_UNQUOTE(JSON_EXTRACT(snapshot, ''$.sandboxSnapshot.profile.id'')) COLLATE utf8mb4_unicode_ci = ''generic-docker''
         AND JSON_UNQUOTE(JSON_EXTRACT(snapshot, ''$.sandboxSnapshot.profile.projectId'')) = @legacy_generic_sandbox_project))',
  'DO 0');
PREPARE update_run_sandbox_stmt FROM @update_run_sandbox_sql;
EXECUTE update_run_sandbox_stmt;
DEALLOCATE PREPARE update_run_sandbox_stmt;

SET @assistant_sessions_exists = EXISTS (
  SELECT 1 FROM information_schema.tables
  WHERE table_schema = DATABASE() AND table_name = 'assistant_sessions');
SET @update_assistant_sandbox_sql = IF(@assistant_sessions_exists AND @legacy_generic_sandbox_id IS NOT NULL,
  CONCAT('UPDATE assistant_sessions a ',
    IF(@run_snapshot_exists,
      'LEFT JOIN runs r ON r.id COLLATE utf8mb4_unicode_ci = JSON_UNQUOTE(JSON_EXTRACT(a.payload, ''$.workspaceRunId'')) COLLATE utf8mb4_unicode_ci ',
      ''),
    'SET a.payload = JSON_SET(a.payload, ''$.sandboxProfileId'', @legacy_generic_sandbox_id)
     WHERE JSON_UNQUOTE(JSON_EXTRACT(a.payload, ''$.sandboxProfileId'')) COLLATE utf8mb4_unicode_ci = ''generic-docker''',
    IF(@run_snapshot_exists,
      ' AND COALESCE(JSON_UNQUOTE(JSON_EXTRACT(r.snapshot, ''$.sandboxProfileId'')), '''') COLLATE utf8mb4_unicode_ci <> ''generic-docker''',
      '')),
  'DO 0');
PREPARE update_assistant_sandbox_stmt FROM @update_assistant_sandbox_sql;
EXECUTE update_assistant_sandbox_stmt;
DEALLOCATE PREPARE update_assistant_sandbox_stmt;

SET @assistant_workspace_driver_sql = IF(@assistant_preferences_exists,
  'UPDATE assistant_preferences SET workspace_driver = ''native'' WHERE workspace_driver = ''local''',
  'DO 0');
PREPARE assistant_workspace_driver_stmt FROM @assistant_workspace_driver_sql;
EXECUTE assistant_workspace_driver_stmt;
DEALLOCATE PREPARE assistant_workspace_driver_stmt;

-- Mark the Dockerfiles as copied in the same transaction as the new payloads.
-- A retry after COMMIT but before DROP will not make a sandbox for them again.
DELETE FROM dockerfiles;
COMMIT;

-- 6. Drop the legacy tables.
DROP TABLE IF EXISTS dockerfiles, environments;
