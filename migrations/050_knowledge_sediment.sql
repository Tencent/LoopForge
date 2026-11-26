-- 050: 自动沉淀的门禁、发布模式与撤回墓碑（控制面，主库）。可重复执行。
--
-- knowledge_bases 增加沉淀发布模式与敏感规则集；knowledge_tombstones 记录
-- 撤回。墓碑只保存内容指纹和出处元数据，不保存正文。不建跨库外键。

SET @ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE()
  AND table_name = 'knowledge_bases' AND column_name = 'sediment_publish_mode') = 0,
 'ALTER TABLE knowledge_bases ADD COLUMN sediment_publish_mode VARCHAR(16) NOT NULL DEFAULT ''review''',
 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE()
  AND table_name = 'knowledge_bases' AND column_name = 'sediment_rules') = 0,
 'ALTER TABLE knowledge_bases ADD COLUMN sediment_rules JSON NULL',
 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

CREATE TABLE IF NOT EXISTS knowledge_tombstones (
  project_id    VARCHAR(64)  NOT NULL DEFAULT '',
  fingerprint   CHAR(64)     NOT NULL DEFAULT '',
  base_id       VARCHAR(64)  NOT NULL DEFAULT '',
  source_id     VARCHAR(64)  NOT NULL DEFAULT '',
  path          VARCHAR(512) NOT NULL DEFAULT '',
  submission_id VARCHAR(64)  NOT NULL DEFAULT '',
  title         VARCHAR(512) NOT NULL DEFAULT '',
  reason        VARCHAR(255) NOT NULL DEFAULT '',
  created_by    VARCHAR(255) NOT NULL DEFAULT '',
  created_at    DATETIME(3)  NULL,
  PRIMARY KEY (project_id, fingerprint),
  KEY idx_knowledge_tombstones_source (source_id),
  KEY idx_knowledge_tombstones_submission (submission_id)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;
