-- 038: 治理降噪。可重复执行。
--
-- 控制面：来源可关闭负责人/过期检查；同步进行中写入阶段；知识缺口可标为
-- 忽略或已处理，并记下最近的用户和会话，供短时间重复合并计数。
-- 不改文档正文。启动版本检查认 knowledge_schema_versions(38)。

-- @plane control

SET @ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE()
  AND table_name = 'knowledge_sources' AND column_name = 'metadata_checks') = 0,
 'ALTER TABLE knowledge_sources ADD COLUMN metadata_checks TINYINT(1) NOT NULL DEFAULT 1',
 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE()
  AND table_name = 'knowledge_sources' AND column_name = 'sync_stage') = 0,
 'ALTER TABLE knowledge_sources ADD COLUMN sync_stage VARCHAR(32) NOT NULL DEFAULT ''''',
 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE()
  AND table_name = 'knowledge_gaps' AND column_name = 'status') = 0,
 'ALTER TABLE knowledge_gaps ADD COLUMN status VARCHAR(16) NOT NULL DEFAULT ''open''',
 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE()
  AND table_name = 'knowledge_gaps' AND column_name = 'last_user_id') = 0,
 'ALTER TABLE knowledge_gaps ADD COLUMN last_user_id BIGINT NOT NULL DEFAULT 0',
 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE()
  AND table_name = 'knowledge_gaps' AND column_name = 'last_session_id') = 0,
 'ALTER TABLE knowledge_gaps ADD COLUMN last_session_id VARCHAR(64) NOT NULL DEFAULT ''''',
 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

INSERT INTO knowledge_schema_versions (version, applied_at)
VALUES (38, UTC_TIMESTAMP(3))
ON DUPLICATE KEY UPDATE version = VALUES(version);
