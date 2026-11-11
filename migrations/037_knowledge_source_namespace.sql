-- Run manually after 034, with API and workers stopped. Never run on startup.
-- Defaults to one database. For content on the same MySQL server, set
-- @knowledge_content_schema before sourcing this file. See the proposal's 037 notes
-- for separate-server content databases. Back up both databases first.
-- ===== PLAN: control database =====
SET @knowledge_content_schema = COALESCE(@knowledge_content_schema, DATABASE());

-- Keep the mapping so interruption/repetition and separate content DBs can resume.
CREATE TABLE IF NOT EXISTS knowledge_id_migration_037 (
  kind VARCHAR(16) NOT NULL,
  old_id VARCHAR(64) NOT NULL,
  new_id VARCHAR(64) NOT NULL,
  PRIMARY KEY (kind, old_id),
  UNIQUE KEY uq_knowledge_id_037_target (kind, new_id)
) DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
INSERT IGNORE INTO knowledge_id_migration_037 (kind, old_id, new_id)
SELECT 'base', id, CONCAT('kb-project-', LEFT(SHA2(CONCAT('project:', project_id), 256), 32))
FROM knowledge_bases
WHERE layer = 'project' AND LENGTH(CONCAT('kb-', project_id)) > 64
  AND id <> CONCAT('kb-project-', LEFT(SHA2(CONCAT('project:', project_id), 256), 32));
INSERT IGNORE INTO knowledge_id_migration_037 (kind, old_id, new_id)
SELECT 'source', s.id, CONCAT('kb-hosted-', LEFT(SHA2(CONCAT('hosted:', COALESCE(m.new_id, s.base_id)), 256), 32))
FROM knowledge_sources s
LEFT JOIN knowledge_id_migration_037 m ON m.kind = 'base' AND BINARY m.old_id=BINARY s.base_id
WHERE s.kind = 'hosted'
  AND s.id <> CONCAT('kb-hosted-', LEFT(SHA2(CONCAT('hosted:', COALESCE(m.new_id, s.base_id)), 256), 32));
-- Fail rather than silently skip two legacy sources targeting the same ID.
DROP TEMPORARY TABLE IF EXISTS kb037_assert;
CREATE TEMPORARY TABLE kb037_assert (valid INT CHECK (valid = 1));
INSERT INTO kb037_assert
SELECT IF(COUNT(*) = 0, 1, 0) FROM knowledge_sources s
LEFT JOIN knowledge_id_migration_037 b ON b.kind='base' AND BINARY b.old_id=BINARY s.base_id
WHERE s.kind='hosted'
 AND s.id <> CONCAT('kb-hosted-', LEFT(SHA2(CONCAT('hosted:', COALESCE(b.new_id,s.base_id)),256),32))
 AND NOT EXISTS (SELECT 1 FROM knowledge_id_migration_037 m WHERE m.kind='source' AND BINARY m.old_id=BINARY s.id
 AND m.new_id=CONCAT('kb-hosted-', LEFT(SHA2(CONCAT('hosted:', COALESCE(b.new_id,s.base_id)),256),32)));
DROP TEMPORARY TABLE kb037_assert;
-- Git IDs are deliberately not renamed: AES-GCM authenticates their IDs.
-- Re-registration reuses an exact legacy configuration ONLY inside its owning
-- base; new Git IDs hash git:<baseID>\0<url>\0<branch>\0<prefix>.

-- ===== CONTENT: content database (with mapping table imported if separate server) =====
SET @knowledge_content_schema = COALESCE(@knowledge_content_schema, DATABASE());
START TRANSACTION;
-- Content plane. Dynamic schema names are quoted, never treated as SQL text.
SET @kb037_schema = CONCAT('`', REPLACE(@knowledge_content_schema, '`', '``'), '`');
SET @kb037_sql = CONCAT('UPDATE ', @kb037_schema, '.knowledge_documents d JOIN knowledge_id_migration_037 m ON m.kind=''source'' AND BINARY m.old_id=BINARY d.source_id SET d.source_id=m.new_id');
PREPARE kb037_stmt FROM @kb037_sql;
EXECUTE kb037_stmt;
DEALLOCATE PREPARE kb037_stmt;
SET @kb037_sql = CONCAT('UPDATE ', @kb037_schema, '.knowledge_revisions d JOIN knowledge_id_migration_037 m ON m.kind=''source'' AND BINARY m.old_id=BINARY d.source_id SET d.source_id=m.new_id');
PREPARE kb037_stmt FROM @kb037_sql;
EXECUTE kb037_stmt;
DEALLOCATE PREPARE kb037_stmt;
SET @kb037_sql = CONCAT('UPDATE ', @kb037_schema, '.knowledge_chunks d JOIN knowledge_id_migration_037 m ON m.kind=''source'' AND BINARY m.old_id=BINARY d.source_id SET d.source_id=m.new_id');
PREPARE kb037_stmt FROM @kb037_sql;
EXECUTE kb037_stmt;
DEALLOCATE PREPARE kb037_stmt;

COMMIT;

-- ===== CONTROL: control database, after CONTENT succeeded =====
START TRANSACTION;
-- Control plane. Payload stores the review source, including retracted items.
UPDATE knowledge_submissions s JOIN knowledge_id_migration_037 m
 ON m.kind='source' AND BINARY m.old_id=BINARY JSON_UNQUOTE(JSON_EXTRACT(s.payload,'$.source'))
 SET s.payload=JSON_SET(s.payload,'$.source',m.new_id);
UPDATE knowledge_sync_jobs s JOIN knowledge_id_migration_037 m ON m.kind='source' AND BINARY m.old_id=BINARY s.source_id SET s.source_id=m.new_id;
UPDATE knowledge_writebacks s JOIN knowledge_id_migration_037 m ON m.kind='source' AND BINARY m.old_id=BINARY s.source_id SET s.source_id=m.new_id;
UPDATE knowledge_tombstones s JOIN knowledge_id_migration_037 m ON m.kind='source' AND BINARY m.old_id=BINARY s.source_id SET s.source_id=m.new_id;
UPDATE knowledge_feedback s JOIN knowledge_id_migration_037 m ON m.kind='source' AND BINARY m.old_id=BINARY s.source_id SET s.source_id=m.new_id;
UPDATE knowledge_governance_issues s JOIN knowledge_id_migration_037 m ON m.kind='source' AND BINARY m.old_id=BINARY s.source_id SET s.source_id=m.new_id;
UPDATE knowledge_citations s JOIN knowledge_id_migration_037 m ON m.kind='source' AND BINARY m.old_id=BINARY s.source_id SET s.source_id=m.new_id;
UPDATE knowledge_sources s JOIN knowledge_id_migration_037 m ON m.kind='source' AND BINARY m.old_id=BINARY s.id SET s.id=m.new_id;

UPDATE project_knowledge_bindings s JOIN knowledge_id_migration_037 m ON m.kind='base' AND BINARY m.old_id=BINARY s.base_id SET s.base_id=m.new_id;
UPDATE knowledge_sources s JOIN knowledge_id_migration_037 m ON m.kind='base' AND BINARY m.old_id=BINARY s.base_id SET s.base_id=m.new_id;
UPDATE knowledge_tombstones s JOIN knowledge_id_migration_037 m ON m.kind='base' AND BINARY m.old_id=BINARY s.base_id SET s.base_id=m.new_id;
UPDATE knowledge_feedback s JOIN knowledge_id_migration_037 m ON m.kind='base' AND BINARY m.old_id=BINARY s.base_id SET s.base_id=m.new_id;
UPDATE knowledge_governance_issues s JOIN knowledge_id_migration_037 m ON m.kind='base' AND BINARY m.old_id=BINARY s.base_id SET s.base_id=m.new_id;
UPDATE knowledge_governance_scans s JOIN knowledge_id_migration_037 m ON m.kind='base' AND BINARY m.old_id=BINARY s.base_id SET s.base_id=m.new_id;
UPDATE knowledge_citations s JOIN knowledge_id_migration_037 m ON m.kind='base' AND BINARY m.old_id=BINARY s.base_id SET s.base_id=m.new_id;
UPDATE knowledge_bases s JOIN knowledge_id_migration_037 m ON m.kind='base' AND BINARY m.old_id=BINARY s.id SET s.id=m.new_id;
COMMIT;
