-- 058: 托管知识库/来源 ID 命名空间迁移。可重复执行。
--
-- 运行前必须停止所有 API 和 worker 实例（不只是本机 Compose 平台）：
-- 本脚本会改控制面来源 ID，同时改内容面的 source_id 和撤回 fence；旧实例
-- 继续写入会产生旧 ID 的孤儿正文或 fence。安装/升级脚本会检查
-- engine_instances 中仍在线的 worker，并留出停止实例的等待窗口。
--
-- 分三段：
--   control：建立映射表、计算旧版长项目库和新托管来源的 ID，并校验没有
--            两个旧来源映射到同一个新 ID。
--   content：在内容库按映射更新 documents/revisions/chunks 和
--            knowledge_content_fences。独立内容库由
--            scripts/knowledge-schema.sh 在执行本段前导入控制面的映射表；
--            同一 MySQL 服务器上也可由本段从 @knowledge_control_schema 复制。
--   control：更新控制面引用，最后写入 knowledge_schema_versions(55)。
--
-- Git 来源 ID 不改：AES-GCM 用来源 ID 做 AAD。重新登记时只在已核实归属的
-- 库内复用完全相同的旧配置；新 Git ID 以 git:<baseID>\0<url>\0<branch>\0<prefix>
-- 为种子。

-- @plane control

CREATE TABLE IF NOT EXISTS knowledge_schema_versions (
  version    INT         NOT NULL PRIMARY KEY,
  applied_at DATETIME(3) NULL
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

-- Keep the mapping so interruption/repetition and separate content DBs can resume.
CREATE TABLE IF NOT EXISTS knowledge_id_migration_039 (
  kind VARCHAR(16) NOT NULL,
  old_id VARCHAR(64) NOT NULL,
  new_id VARCHAR(64) NOT NULL,
  PRIMARY KEY (kind, old_id),
  UNIQUE KEY uq_knowledge_id_039_target (kind, new_id)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;
INSERT IGNORE INTO knowledge_id_migration_039 (kind, old_id, new_id)
SELECT 'base', id, CONCAT('kb-project-', LEFT(SHA2(CONCAT('project:', project_id), 256), 32))
FROM knowledge_bases
WHERE layer = 'project' AND LENGTH(CONCAT('kb-', project_id)) > 64
  AND id <> CONCAT('kb-project-', LEFT(SHA2(CONCAT('project:', project_id), 256), 32));
INSERT IGNORE INTO knowledge_id_migration_039 (kind, old_id, new_id)
SELECT 'source', s.id, CONCAT('kb-hosted-', LEFT(SHA2(CONCAT('hosted:', COALESCE(m.new_id, s.base_id)), 256), 32))
FROM knowledge_sources s
LEFT JOIN knowledge_id_migration_039 m ON m.kind = 'base' AND BINARY m.old_id=BINARY s.base_id
WHERE s.kind = 'hosted'
  AND s.id <> CONCAT('kb-hosted-', LEFT(SHA2(CONCAT('hosted:', COALESCE(m.new_id, s.base_id)), 256), 32));
-- Fail rather than silently skip two legacy sources targeting the same ID.
DROP TEMPORARY TABLE IF EXISTS kb039_assert;
CREATE TEMPORARY TABLE kb039_assert (valid INT CHECK (valid = 1));
INSERT INTO kb039_assert
SELECT IF(COUNT(*) = 0, 1, 0) FROM knowledge_sources s
LEFT JOIN knowledge_id_migration_039 b ON b.kind='base' AND BINARY b.old_id=BINARY s.base_id
WHERE s.kind='hosted'
 AND s.id <> CONCAT('kb-hosted-', LEFT(SHA2(CONCAT('hosted:', COALESCE(b.new_id,s.base_id)),256),32))
 AND NOT EXISTS (SELECT 1 FROM knowledge_id_migration_039 m WHERE m.kind='source' AND BINARY m.old_id=BINARY s.id
 AND m.new_id=CONCAT('kb-hosted-', LEFT(SHA2(CONCAT('hosted:', COALESCE(b.new_id,s.base_id)),256),32)));
DROP TEMPORARY TABLE kb039_assert;

-- @plane content

-- The content segment only uses a local mapping table. knowledge-schema.sh
-- imports it before this segment when the content database is on another
-- server; on the same server this block copies it from the control schema.
CREATE TABLE IF NOT EXISTS knowledge_id_migration_039 (
  kind VARCHAR(16) NOT NULL,
  old_id VARCHAR(64) NOT NULL,
  new_id VARCHAR(64) NOT NULL,
  PRIMARY KEY (kind, old_id),
  UNIQUE KEY uq_knowledge_id_039_target (kind, new_id)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

SET @kb039_control_schema = IFNULL(@knowledge_control_schema, DATABASE());
SET @kb039_has_control_mapping = (
  SELECT COUNT(*) FROM information_schema.TABLES
  WHERE TABLE_SCHEMA = @kb039_control_schema AND TABLE_NAME = 'knowledge_id_migration_039'
);
SET @kb039_sql = IF(@kb039_has_control_mapping = 0, 'DO 0', CONCAT(
  'INSERT IGNORE INTO knowledge_id_migration_039 (kind, old_id, new_id) ',
  'SELECT kind, old_id, new_id FROM `', REPLACE(@kb039_control_schema, '`', '``'), '`.knowledge_id_migration_039'
));
PREPARE kb039_stmt FROM @kb039_sql;
EXECUTE kb039_stmt;
DEALLOCATE PREPARE kb039_stmt;

-- Content plane. Dynamic quoting keeps schema names out of SQL text; the plain
-- updates below run in the content database and use only the local mapping.
START TRANSACTION;
SET @kb039_schema = CONCAT('`', REPLACE(DATABASE(), '`', '``'), '`');

SET @kb039_sql = CONCAT('UPDATE ', @kb039_schema, '.knowledge_documents d JOIN knowledge_id_migration_039 m ON m.kind=''source'' AND BINARY m.old_id=BINARY d.source_id SET d.source_id=m.new_id');
PREPARE kb039_stmt FROM @kb039_sql;
EXECUTE kb039_stmt;
DEALLOCATE PREPARE kb039_stmt;
SET @kb039_sql = CONCAT('UPDATE ', @kb039_schema, '.knowledge_revisions d JOIN knowledge_id_migration_039 m ON m.kind=''source'' AND BINARY m.old_id=BINARY d.source_id SET d.source_id=m.new_id');
PREPARE kb039_stmt FROM @kb039_sql;
EXECUTE kb039_stmt;
DEALLOCATE PREPARE kb039_stmt;
SET @kb039_sql = CONCAT('UPDATE ', @kb039_schema, '.knowledge_chunks d JOIN knowledge_id_migration_039 m ON m.kind=''source'' AND BINARY m.old_id=BINARY d.source_id SET d.source_id=m.new_id');
PREPARE kb039_stmt FROM @kb039_sql;
EXECUTE kb039_stmt;
DEALLOCATE PREPARE kb039_stmt;

-- Fences must follow the source rename or a late write can revive retracted
-- content under the new ID. Merge into an existing target fence (greatest token,
-- retracted wins), then remove the old row.
SET @kb039_has_fences = (
  SELECT COUNT(*) FROM information_schema.TABLES
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'knowledge_content_fences'
);
SET @kb039_sql = IF(@kb039_has_fences = 0, 'DO 0', CONCAT(
  'INSERT INTO knowledge_content_fences (source_id, path, token, retracted) ',
  'SELECT m.new_id, f.path, f.token, f.retracted FROM knowledge_content_fences f ',
  'JOIN knowledge_id_migration_039 m ON m.kind=''source'' AND BINARY m.old_id=BINARY f.source_id ',
  'ON DUPLICATE KEY UPDATE token=GREATEST(knowledge_content_fences.token, VALUES(token)), retracted=(knowledge_content_fences.retracted OR VALUES(retracted))'
));
PREPARE kb039_stmt FROM @kb039_sql;
EXECUTE kb039_stmt;
DEALLOCATE PREPARE kb039_stmt;
SET @kb039_sql = IF(@kb039_has_fences = 0, 'DO 0', CONCAT(
  'DELETE f FROM knowledge_content_fences f ',
  'JOIN knowledge_id_migration_039 m ON m.kind=''source'' AND BINARY m.old_id=BINARY f.source_id'
));
PREPARE kb039_stmt FROM @kb039_sql;
EXECUTE kb039_stmt;
DEALLOCATE PREPARE kb039_stmt;

COMMIT;

-- @plane control

START TRANSACTION;
-- Control plane. Payload stores the review source, including retracted items.
UPDATE knowledge_submissions s JOIN knowledge_id_migration_039 m
 ON m.kind='source' AND BINARY m.old_id=BINARY JSON_UNQUOTE(JSON_EXTRACT(s.payload,'$.source'))
 SET s.payload=JSON_SET(s.payload,'$.source',m.new_id)
 WHERE COALESCE(JSON_UNQUOTE(JSON_EXTRACT(s.payload,'$.provider')), 'builtin')='builtin';
UPDATE knowledge_sync_jobs s JOIN knowledge_id_migration_039 m ON m.kind='source' AND BINARY m.old_id=BINARY s.source_id SET s.source_id=m.new_id;
UPDATE knowledge_writebacks s JOIN knowledge_id_migration_039 m ON m.kind='source' AND BINARY m.old_id=BINARY s.source_id SET s.source_id=m.new_id;
UPDATE knowledge_tombstones s JOIN knowledge_id_migration_039 m ON m.kind='source' AND BINARY m.old_id=BINARY s.source_id SET s.source_id=m.new_id;
UPDATE knowledge_feedback s JOIN knowledge_id_migration_039 m ON m.kind='source' AND BINARY m.old_id=BINARY s.source_id SET s.source_id=m.new_id;
UPDATE knowledge_governance_issues s JOIN knowledge_id_migration_039 m ON m.kind='source' AND BINARY m.old_id=BINARY s.source_id SET s.source_id=m.new_id;
UPDATE knowledge_citations s JOIN knowledge_id_migration_039 m ON m.kind='source' AND BINARY m.old_id=BINARY s.source_id SET s.source_id=m.new_id;
UPDATE knowledge_sources s JOIN knowledge_id_migration_039 m ON m.kind='source' AND BINARY m.old_id=BINARY s.id SET s.id=m.new_id;

UPDATE project_knowledge_bindings s JOIN knowledge_id_migration_039 m ON m.kind='base' AND BINARY m.old_id=BINARY s.base_id SET s.base_id=m.new_id;
UPDATE knowledge_sources s JOIN knowledge_id_migration_039 m ON m.kind='base' AND BINARY m.old_id=BINARY s.base_id SET s.base_id=m.new_id;
UPDATE knowledge_tombstones s JOIN knowledge_id_migration_039 m ON m.kind='base' AND BINARY m.old_id=BINARY s.base_id SET s.base_id=m.new_id;
UPDATE knowledge_feedback s JOIN knowledge_id_migration_039 m ON m.kind='base' AND BINARY m.old_id=BINARY s.base_id SET s.base_id=m.new_id;
UPDATE knowledge_governance_issues s JOIN knowledge_id_migration_039 m ON m.kind='base' AND BINARY m.old_id=BINARY s.base_id SET s.base_id=m.new_id;
UPDATE knowledge_governance_scans s JOIN knowledge_id_migration_039 m ON m.kind='base' AND BINARY m.old_id=BINARY s.base_id SET s.base_id=m.new_id;
UPDATE knowledge_citations s JOIN knowledge_id_migration_039 m ON m.kind='base' AND BINARY m.old_id=BINARY s.base_id SET s.base_id=m.new_id;
UPDATE knowledge_bases s JOIN knowledge_id_migration_039 m ON m.kind='base' AND BINARY m.old_id=BINARY s.id SET s.id=m.new_id;

-- The version row is written only after both planes and the control updates
-- succeeded. A failed run leaves the old version and is safe to retry.
INSERT INTO knowledge_schema_versions (version, applied_at)
VALUES (58, UTC_TIMESTAMP(3))
ON DUPLICATE KEY UPDATE version = VALUES(version);
COMMIT;
