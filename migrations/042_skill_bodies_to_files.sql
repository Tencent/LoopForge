-- Run with skill writes paused. If run before deploying file-backed skill
-- creation, rerun after deployment to pick up any remaining legacy bodies.
-- Move catalog bodies to skill_files and refresh the file manifest.

DROP TEMPORARY TABLE IF EXISTS skill_body_migration_guard;
DROP TEMPORARY TABLE IF EXISTS skill_body_migration_blockers;
CREATE TEMPORARY TABLE skill_body_migration_blockers AS
SELECT
  legacy.id AS skill_id,
  CASE WHEN existing.skill_id IS NULL THEN 'body_exceeds_10_mib'
       ELSE 'skill_md_path_collision' END AS reason,
  legacy.body_bytes,
  existing.path AS existing_path
FROM (
  SELECT id, OCTET_LENGTH(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.content'))) AS body_bytes
  FROM skills
  WHERE JSON_TYPE(JSON_EXTRACT(payload, '$.content')) = 'STRING'
) AS legacy
LEFT JOIN skill_files AS existing
  ON existing.skill_id = legacy.id AND existing.path = 'SKILL.md'
WHERE legacy.body_bytes > 0
  AND ((existing.skill_id IS NOT NULL AND CAST(existing.path AS BINARY) <> 'SKILL.md')
       OR (existing.skill_id IS NULL AND legacy.body_bytes > 10485760));

SELECT skill_id, reason, body_bytes, existing_path
FROM skill_body_migration_blockers
ORDER BY skill_id;

-- On MySQL 5.7, a duplicate primary key rejects the entire InnoDB INSERT.
-- Both writes require the guard row so clients that continue after an error
-- still leave the batch untouched.
CREATE TEMPORARY TABLE skill_body_migration_guard (
  result VARCHAR(64) NOT NULL PRIMARY KEY
) ENGINE=InnoDB;
INSERT INTO skill_body_migration_guard (result)
SELECT 'Migration 042 blocked by preflight'
UNION ALL
SELECT 'Migration 042 blocked by preflight' FROM DUAL
WHERE EXISTS (SELECT 1 FROM skill_body_migration_blockers);

START TRANSACTION;

INSERT INTO skill_files (skill_id, path, content, size, sha256, unix_mode, updated_at)
SELECT id, 'SKILL.md', body, OCTET_LENGTH(body), SHA2(body, 256), 420, updated_at
FROM (
  SELECT id, CAST(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.content')) AS BINARY) AS body, updated_at
  FROM skills
  WHERE JSON_TYPE(JSON_EXTRACT(payload, '$.content')) = 'STRING'
) AS legacy
WHERE OCTET_LENGTH(body) BETWEEN 1 AND 10485760
  AND EXISTS (SELECT 1 FROM skill_body_migration_guard)
ON DUPLICATE KEY UPDATE skill_id = skill_files.skill_id;

-- An uploaded SKILL.md already takes precedence at runtime. Keep its bytes
-- and rebuild the manifest from the stored files, including any attachments.
UPDATE skills AS skill
JOIN skill_files AS body
  ON body.skill_id = skill.id AND CAST(body.path AS BINARY) = 'SKILL.md'
SET skill.payload = JSON_REMOVE(
  JSON_SET(skill.payload, '$.fileRefs', (
    SELECT JSON_ARRAYAGG(JSON_OBJECT(
      'path', file.path,
      'size', file.size,
      'sha256', file.sha256,
      'mode', file.unix_mode,
      'updatedAt', DATE_FORMAT(file.updated_at, '%Y-%m-%dT%H:%i:%s.%fZ')
    ))
    FROM skill_files AS file
    WHERE file.skill_id = skill.id
  )),
  '$.content'
)
WHERE JSON_TYPE(JSON_EXTRACT(skill.payload, '$.content')) = 'STRING'
  AND OCTET_LENGTH(JSON_UNQUOTE(JSON_EXTRACT(skill.payload, '$.content'))) > 0
  AND EXISTS (SELECT 1 FROM skill_body_migration_guard);

COMMIT;

SELECT COUNT(*) AS remaining_inline_skill_bodies
FROM skills
WHERE JSON_TYPE(JSON_EXTRACT(payload, '$.content')) = 'STRING'
  AND OCTET_LENGTH(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.content'))) > 0;

DROP TEMPORARY TABLE skill_body_migration_guard;
DROP TEMPORARY TABLE skill_body_migration_blockers;
