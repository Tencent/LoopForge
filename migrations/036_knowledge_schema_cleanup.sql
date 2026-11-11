-- 036: 知识库 schema 收尾（内容面 staging、路径 collation、写回可空唯一键）。
--
-- 本脚本可重复执行，分两段，段内不含跨库外键或跨库事务：
--   第一段（内容库）：新增审核正文 staging 表；把内容面路径列改成
--     utf8mb4_bin，避免 Docs/A.md 与 docs/a.md 在唯一键上互相冲突；把升级前
--     留在控制面 knowledge_submissions.payload 里的待审正文搬到 staging，
--     再从 payload 删除 content 字段。
--   第二段（主库）：knowledge_writebacks.submission_id 改为可空唯一；历史
--     空串归一为 NULL；控制面路径列同样改成 utf8mb4_bin。
--
-- 跨库搬迁只在控制库与内容库位于同一个 MySQL 服务器时执行（脚本会查
-- information_schema）。两库在不同服务器时，本段会跳过复制和删除，正文仍
-- 留在控制面 payload；运维需先按 docs/deployment.md 导出/导入 staging，再在
-- 控制库手工执行：
--   UPDATE knowledge_submissions
--   SET payload = JSON_REMOVE(payload, '$.content')
--   WHERE payload IS NOT NULL AND JSON_CONTAINS_PATH(payload, 'one', '$.content');
-- 由安装/升级脚本（scripts/knowledge-schema.sh）执行时，会把主库库名通过
-- @knowledge_control_schema 传进来。

-- @plane content

CREATE TABLE IF NOT EXISTS knowledge_submission_contents (
  submission_id VARCHAR(64) NOT NULL PRIMARY KEY,
  body          MEDIUMTEXT  NULL,
  updated_at    DATETIME(3) NULL
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

-- 内容面路径列：唯一键和主键都按字节比较，大小写不同的路径是两篇文档。
SET @ddl := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
   WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'knowledge_documents'
     AND COLUMN_NAME = 'path' AND COLLATION_NAME <> 'utf8mb4_bin') > 0,
  'ALTER TABLE knowledge_documents MODIFY path VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL',
  'DO 0');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @ddl := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
   WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'knowledge_revisions'
     AND COLUMN_NAME = 'path' AND COLLATION_NAME <> 'utf8mb4_bin') > 0,
  'ALTER TABLE knowledge_revisions MODIFY path VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT ''''',
  'DO 0');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @ddl := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
   WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'knowledge_chunks'
     AND COLUMN_NAME = 'path' AND COLLATION_NAME <> 'utf8mb4_bin') > 0,
  'ALTER TABLE knowledge_chunks MODIFY path VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT ''''',
  'DO 0');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- 升级前待审正文还在控制面 payload。两库同服务器（含同库）时直接跨库复制，
-- 复制成功后再删除 payload.content；检测不到控制库时两件事都跳过，避免把
-- 尚未搬走的正文删掉。
SET @control_schema := IFNULL(@knowledge_control_schema, DATABASE());
SET @has_control_submissions := (
  SELECT COUNT(*) FROM information_schema.TABLES
  WHERE TABLE_SCHEMA = @control_schema AND TABLE_NAME = 'knowledge_submissions'
);
SET @copy_submission_bodies := IF(@has_control_submissions = 0, 'DO 0', CONCAT(
  'INSERT INTO knowledge_submission_contents (submission_id, body, updated_at) ',
  'SELECT id, JSON_UNQUOTE(JSON_EXTRACT(payload, ''$.content'')), updated_at ',
  'FROM `', REPLACE(@control_schema, '`', '``'), '`.knowledge_submissions ',
  'WHERE payload IS NOT NULL ',
  'AND JSON_EXTRACT(payload, ''$.content'') IS NOT NULL ',
  'AND JSON_UNQUOTE(JSON_EXTRACT(payload, ''$.content'')) <> '''' ',
  'ON DUPLICATE KEY UPDATE body = VALUES(body), updated_at = VALUES(updated_at)'
));
PREPARE stmt FROM @copy_submission_bodies; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @strip_submission_bodies := IF(@has_control_submissions = 0, 'DO 0', CONCAT(
  'UPDATE `', REPLACE(@control_schema, '`', '``'), '`.knowledge_submissions ',
  'SET payload = JSON_REMOVE(payload, ''$.content'') ',
  'WHERE payload IS NOT NULL AND JSON_CONTAINS_PATH(payload, ''one'', ''$.content'')'
));
PREPARE stmt FROM @strip_submission_bodies; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- @plane control

-- 写回记录没有审核条目时允许为空：唯一索引按 NULL 处理，不再用空串占位。
SET @ddl := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
   WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'knowledge_writebacks'
     AND COLUMN_NAME = 'submission_id'
     AND (IS_NULLABLE = 'NO' OR COLUMN_DEFAULT IS NOT NULL)) > 0,
  'ALTER TABLE knowledge_writebacks MODIFY submission_id VARCHAR(64) NULL DEFAULT NULL',
  'DO 0');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

UPDATE knowledge_writebacks SET submission_id = NULL WHERE submission_id = '';

-- 控制面路径列只做展示/反馈关联，不参与正文唯一键；统一成按字节比较，避免
-- 同一个文档在控制面因大小写产生两条不同记录。
SET @ddl := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
   WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'knowledge_writebacks'
     AND COLUMN_NAME = 'path' AND COLLATION_NAME <> 'utf8mb4_bin') > 0,
  'ALTER TABLE knowledge_writebacks MODIFY path VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT ''''',
  'DO 0');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @ddl := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
   WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'knowledge_tombstones'
     AND COLUMN_NAME = 'path' AND COLLATION_NAME <> 'utf8mb4_bin') > 0,
  'ALTER TABLE knowledge_tombstones MODIFY path VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT ''''',
  'DO 0');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @ddl := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
   WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'knowledge_feedback'
     AND COLUMN_NAME = 'path' AND COLLATION_NAME <> 'utf8mb4_bin') > 0,
  'ALTER TABLE knowledge_feedback MODIFY path VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT ''''',
  'DO 0');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @ddl := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
   WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'knowledge_governance_issues'
     AND COLUMN_NAME = 'path' AND COLLATION_NAME <> 'utf8mb4_bin') > 0,
  'ALTER TABLE knowledge_governance_issues MODIFY path VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT ''''',
  'DO 0');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @ddl := IF(
  (SELECT COUNT(*) FROM information_schema.COLUMNS
   WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'knowledge_citations'
     AND COLUMN_NAME = 'path' AND COLLATION_NAME <> 'utf8mb4_bin') > 0,
  'ALTER TABLE knowledge_citations MODIFY path VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT ''''',
  'DO 0');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;
