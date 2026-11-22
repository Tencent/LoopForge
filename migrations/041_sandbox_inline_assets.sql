-- Manual, idempotent MySQL migration: Dockerfiles and environment install
-- scripts become parts of their sandbox.
--
-- A sandbox now stores its own recipe: a docker sandbox keeps
-- payload.dockerfile {content, files}, a native (seatbelt / bwrap) sandbox
-- keeps payload.install {script, pathEntries, variables}. dockerfileId and
-- environmentId are gone, and so are the dockerfiles and environments tables
-- with their API.
--
-- 1. Each docker sandbox copies the Dockerfile it referenced; each native
--    sandbox copies the environment it referenced. Docker and local sandboxes
--    never used an environment, so their environmentId is simply dropped.
-- 2. A Dockerfile no docker sandbox used becomes a docker sandbox, and an
--    environment no native sandbox used becomes a native sandbox, keeping the
--    asset's name, project and sharing. The sandbox takes the asset's ID, or
--    the ID with a "-docker" / "-native" suffix when a sandbox already has it.
-- 3. The legacy tables are dropped.
--
-- The tables are created first when missing, so the script also runs cleanly
-- on a fresh database and when run again. Docker images are keyed by the
-- Dockerfile content and are reused; a native sandbox reinstalls its
-- toolchain once, under a directory named after the sandbox.
-- scripts/install-schema-extensions.sh runs this on install and on
-- ./start.sh pull.

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

-- The openssh-client fix of 031, for installs that copy an unmodified
-- generic-backend Dockerfile.
UPDATE dockerfiles
SET payload = JSON_SET(payload, '$.content', 'FROM node:22-bookworm\nRUN apt-get update && apt-get install -y --no-install-recommends git openssh-client python3 ripgrep ca-certificates curl && rm -rf /var/lib/apt/lists/*\nRUN npm install -g @openai/codex @anthropic-ai/claude-code @tencent-ai/codebuddy-code\nWORKDIR /workspace\n')
WHERE id = 'generic-backend'
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.content')) = 'FROM node:22-bookworm\nRUN apt-get update && apt-get install -y --no-install-recommends git python3 ripgrep ca-certificates curl && rm -rf /var/lib/apt/lists/*\nRUN npm install -g @openai/codex @anthropic-ai/claude-code @tencent-ai/codebuddy-code\nWORKDIR /workspace\n';

-- 1. Copy referenced assets into their sandboxes.
UPDATE sandboxes s
JOIN dockerfiles d
  ON d.id = JSON_UNQUOTE(JSON_EXTRACT(s.payload, '$.dockerfileId')) COLLATE utf8mb4_unicode_ci
SET s.payload = JSON_SET(s.payload, '$.dockerfile', JSON_MERGE_PATCH(
  JSON_OBJECT('content', COALESCE(JSON_UNQUOTE(JSON_EXTRACT(d.payload, '$.content')), '')),
  JSON_OBJECT('files', JSON_EXTRACT(d.payload, '$.files'))))
WHERE JSON_UNQUOTE(JSON_EXTRACT(s.payload, '$.driver')) = 'docker'
  AND JSON_EXTRACT(s.payload, '$.dockerfile') IS NULL;

UPDATE sandboxes s
JOIN environments e
  ON e.id = JSON_UNQUOTE(JSON_EXTRACT(s.payload, '$.environmentId')) COLLATE utf8mb4_unicode_ci
SET s.payload = JSON_SET(s.payload, '$.install', JSON_MERGE_PATCH('{}',
  JSON_OBJECT('script', JSON_EXTRACT(e.payload, '$.installScript')),
  JSON_OBJECT('pathEntries', JSON_EXTRACT(e.payload, '$.pathEntries')),
  JSON_OBJECT('variables', JSON_EXTRACT(e.payload, '$.variables'))))
WHERE JSON_UNQUOTE(JSON_EXTRACT(s.payload, '$.driver')) IN ('native', 'seatbelt', 'bwrap')
  AND JSON_EXTRACT(s.payload, '$.install') IS NULL;

-- 2. Wrap unused assets as sandboxes of their own.
INSERT IGNORE INTO sandboxes (`id`, `name`, `payload`)
SELECT w.sid, d.name, JSON_MERGE_PATCH(
  JSON_OBJECT('id', w.sid, 'name', d.name, 'driver', 'docker', 'readOnlyRootFS', false,
    'dockerfile', JSON_MERGE_PATCH(
      JSON_OBJECT('content', COALESCE(JSON_UNQUOTE(JSON_EXTRACT(d.payload, '$.content')), '')),
      JSON_OBJECT('files', JSON_EXTRACT(d.payload, '$.files')))),
  JSON_OBJECT('projectId', JSON_EXTRACT(d.payload, '$.projectId')),
  JSON_OBJECT('shared', IF(JSON_EXTRACT(d.payload, '$.shared') = true, CAST('true' AS JSON), NULL)))
FROM dockerfiles d
JOIN (
  SELECT d2.id AS did,
    IF(EXISTS (SELECT 1 FROM sandboxes x WHERE x.id = d2.id COLLATE utf8mb4_unicode_ci), CONCAT(LEFT(d2.id, 57), '-docker'), d2.id) AS sid
  FROM dockerfiles d2
) w ON w.did = d.id
WHERE NOT EXISTS (
  SELECT 1 FROM sandboxes s
  WHERE JSON_UNQUOTE(JSON_EXTRACT(s.payload, '$.driver')) = 'docker'
    AND JSON_UNQUOTE(JSON_EXTRACT(s.payload, '$.dockerfileId')) COLLATE utf8mb4_unicode_ci = d.id
);

INSERT IGNORE INTO sandboxes (`id`, `name`, `payload`)
SELECT w.sid, e.name, JSON_MERGE_PATCH(
  JSON_OBJECT('id', w.sid, 'name', e.name, 'driver', 'native', 'readOnlyRootFS', false,
    'install', JSON_MERGE_PATCH('{}',
      JSON_OBJECT('script', JSON_EXTRACT(e.payload, '$.installScript')),
      JSON_OBJECT('pathEntries', JSON_EXTRACT(e.payload, '$.pathEntries')),
      JSON_OBJECT('variables', JSON_EXTRACT(e.payload, '$.variables')))),
  JSON_OBJECT('projectId', JSON_EXTRACT(e.payload, '$.projectId')),
  JSON_OBJECT('shared', IF(JSON_EXTRACT(e.payload, '$.shared') = true, CAST('true' AS JSON), NULL)))
FROM environments e
JOIN (
  SELECT e2.id AS eid,
    IF(EXISTS (SELECT 1 FROM sandboxes x WHERE x.id = e2.id COLLATE utf8mb4_unicode_ci), CONCAT(LEFT(e2.id, 57), '-native'), e2.id) AS sid
  FROM environments e2
) w ON w.eid = e.id
WHERE NOT EXISTS (
  SELECT 1 FROM sandboxes s
  WHERE JSON_UNQUOTE(JSON_EXTRACT(s.payload, '$.driver')) IN ('native', 'seatbelt', 'bwrap')
    AND JSON_UNQUOTE(JSON_EXTRACT(s.payload, '$.environmentId')) COLLATE utf8mb4_unicode_ci = e.id
);

-- 3. Drop the references and the legacy tables.
UPDATE sandboxes
SET payload = JSON_REMOVE(payload, '$.dockerfileId')
WHERE JSON_CONTAINS_PATH(payload, 'one', '$.dockerfileId');

UPDATE sandboxes
SET payload = JSON_REMOVE(payload, '$.environmentId')
WHERE JSON_CONTAINS_PATH(payload, 'one', '$.environmentId');

DROP TABLE IF EXISTS dockerfiles, environments;
