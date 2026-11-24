-- Run with skill writes paused. If run before deploying file-backed skill
-- creation, rerun after deployment to pick up any remaining legacy bodies.
-- Move catalog bodies to skill_files and refresh the file manifest.
START TRANSACTION;

INSERT INTO skill_files (skill_id, path, content, size, sha256, unix_mode, updated_at)
SELECT id, 'SKILL.md', body, OCTET_LENGTH(body), SHA2(body, 256), 420, updated_at
FROM (
  SELECT id, CAST(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.content')) AS BINARY) AS body, updated_at
  FROM skills
  WHERE JSON_TYPE(JSON_EXTRACT(payload, '$.content')) = 'STRING'
) AS legacy
WHERE OCTET_LENGTH(body) BETWEEN 1 AND 16777215
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
  AND OCTET_LENGTH(JSON_UNQUOTE(JSON_EXTRACT(skill.payload, '$.content'))) > 0;

COMMIT;

-- A path spelling collision or an oversized legacy body can require cleanup.
SELECT COUNT(*) AS remaining_inline_skill_bodies
FROM skills
WHERE JSON_TYPE(JSON_EXTRACT(payload, '$.content')) = 'STRING'
  AND OCTET_LENGTH(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.content'))) > 0;
