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
--    tries a "-docker" suffix and finally a derived ID if the suffix is taken.
-- 3. Each local sandbox becomes a native sandbox: its execution machine,
--    checkout directory and repository paths are dropped, and it keeps its
--    environment variables.
-- 4. Install scripts, the environments they came from and PATH in native
--    sandbox variables are dropped. A native sandbox runs commands with the
--    execution machine's own PATH.
-- 5. The built-in SOPs that used the old shared local profile move to
--    generic-docker, and a 「对话工作区」 preference for local becomes native.
-- 6. The legacy tables are dropped.
--
-- Missing legacy tables are treated as empty so the migration can be repeated.
-- Docker images are keyed by the Dockerfile content and are reused.
-- Apply manually to existing databases before starting the upgraded platform.

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
SELECT w.sid, d.name, JSON_MERGE_PATCH(
  JSON_OBJECT('id', w.sid, 'name', d.name, 'driver', 'docker', 'readOnlyRootFS', false,
    'dockerfile', JSON_MERGE_PATCH(
      JSON_OBJECT('content', COALESCE(JSON_UNQUOTE(JSON_EXTRACT(d.payload, '$.content')), '')),
      JSON_OBJECT('files', JSON_EXTRACT(d.payload, '$.files')))),
  JSON_OBJECT('projectId', JSON_EXTRACT(d.payload, '$.projectId')),
  JSON_OBJECT('shared', IF(JSON_EXTRACT(d.payload, '$.shared') = true, CAST('true' AS JSON), NULL)))
FROM dockerfiles d
JOIN (
  SELECT legacy.id AS did,
    CASE
      WHEN NOT EXISTS (SELECT 1 FROM sandboxes s WHERE s.id = legacy.id COLLATE utf8mb4_unicode_ci)
        THEN legacy.id
      WHEN CHAR_LENGTH(legacy.id) <= 57
        AND NOT EXISTS (SELECT 1 FROM sandboxes s WHERE s.id = CONCAT(legacy.id, '-docker') COLLATE utf8mb4_unicode_ci)
        AND NOT EXISTS (SELECT 1 FROM dockerfiles d WHERE d.id = CONCAT(legacy.id, '-docker') COLLATE utf8mb4_unicode_ci)
        THEN CONCAT(legacy.id, '-docker')
      ELSE CONCAT('dockerfile-', LEFT(SHA2(legacy.id, 256), 53))
    END AS sid
  FROM dockerfiles legacy
) w ON w.did = d.id
WHERE NOT EXISTS (
  SELECT 1 FROM sandboxes s
  WHERE JSON_UNQUOTE(JSON_EXTRACT(s.payload, '$.driver')) = 'docker'
    AND JSON_UNQUOTE(JSON_EXTRACT(s.payload, '$.dockerfileId')) COLLATE utf8mb4_unicode_ci = d.id
);

-- 3. Local sandboxes become native sandboxes.
UPDATE sandboxes
SET payload = JSON_SET(JSON_REMOVE(payload, '$.runnerId', '$.baseDir', '$.repositoryPaths'),
  '$.driver', 'native', '$.readOnlyRootFS', false)
WHERE JSON_UNQUOTE(JSON_EXTRACT(payload, '$.driver')) = 'local';

-- 4. Drop install scripts and PATH overrides of native sandboxes.
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

-- 5. Built-in SOPs and the 「对话工作区」 preference leave local.
UPDATE workflows
SET payload = JSON_SET(payload, '$.sandboxProfileId', 'generic-docker')
WHERE id IN ('welcome-workflow', 'scheduled-baseline-align')
  AND COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.sandboxProfileId')), '') IN ('', 'local');

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
