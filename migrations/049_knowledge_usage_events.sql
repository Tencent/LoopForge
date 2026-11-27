-- 049: 知识库文档调用方明细表（knowledge_usage_events）及文档引用计数扩展
--
-- 说明：
-- - 在 knowledge_document_usage_counts 补增 cited_count，记录最终回答/节点输出中实际引用的次数。
-- - 新建 knowledge_usage_events，按「调用方 × 文档」粒度记录每次命中/读取文档的调用上下文（SOP/节点/角色/Chat/检索词/是否已引用）。
-- - 单用户自动保留最近 30 天且最多 2000 条记录；gorm 增量迁移（autoMigrateIncremental）同样会补建本表与列。

SET @kb_doc_usage_cited_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema=DATABASE() AND table_name='knowledge_document_usage_counts' AND column_name='cited_count')=0,
 'ALTER TABLE knowledge_document_usage_counts ADD COLUMN cited_count BIGINT NOT NULL DEFAULT 0 AFTER read_count',
 'SELECT 1');
PREPARE kb_doc_usage_cited_stmt FROM @kb_doc_usage_cited_ddl;
EXECUTE kb_doc_usage_cited_stmt;
DEALLOCATE PREPARE kb_doc_usage_cited_stmt;

CREATE TABLE IF NOT EXISTS knowledge_usage_events (
  id             BIGINT NOT NULL AUTO_INCREMENT,
  owner_user_id  BIGINT NOT NULL DEFAULT 0,
  read_id        VARCHAR(64) NOT NULL DEFAULT '',
  connector_id   VARCHAR(64) NOT NULL,
  document_key   VARCHAR(255) NOT NULL,
  source_id      VARCHAR(191) NOT NULL DEFAULT '',
  source_name    VARCHAR(255) NOT NULL DEFAULT '',
  document_id    VARCHAR(512) NOT NULL DEFAULT '',
  title          VARCHAR(255) NOT NULL DEFAULT '',
  file_name      VARCHAR(255) NOT NULL DEFAULT '',
  action         VARCHAR(32) NOT NULL DEFAULT 'search_hit',
  tool           VARCHAR(128) NOT NULL DEFAULT '',
  query          VARCHAR(255) NOT NULL DEFAULT '',
  cited          TINYINT(1) NOT NULL DEFAULT 0,
  caller_kind    VARCHAR(32) NOT NULL DEFAULT '',
  workflow_id    VARCHAR(64) NOT NULL DEFAULT '',
  workflow_name  VARCHAR(255) NOT NULL DEFAULT '',
  run_id         VARCHAR(64) NOT NULL DEFAULT '',
  node_id        VARCHAR(64) NOT NULL DEFAULT '',
  node_name      VARCHAR(255) NOT NULL DEFAULT '',
  agent_id       VARCHAR(64) NOT NULL DEFAULT '',
  agent_name     VARCHAR(255) NOT NULL DEFAULT '',
  session_id     VARCHAR(64) NOT NULL DEFAULT '',
  session_title  VARCHAR(255) NOT NULL DEFAULT '',
  space_id       VARCHAR(64) NOT NULL DEFAULT '',
  space_title    VARCHAR(255) NOT NULL DEFAULT '',
  issue_id       VARCHAR(64) NOT NULL DEFAULT '',
  issue_title    VARCHAR(255) NOT NULL DEFAULT '',
  project_id     VARCHAR(64) NOT NULL DEFAULT '',
  called_at      DATETIME(3) NOT NULL,
  created_at     DATETIME(3) NULL,
  PRIMARY KEY (id),
  KEY idx_kb_usage_events_owner_time (owner_user_id, called_at),
  KEY idx_kb_usage_events_owner_doc (owner_user_id, connector_id, document_key, called_at),
  KEY idx_kb_usage_events_owner_read (owner_user_id, read_id),
  KEY idx_kb_usage_events_owner_caller (owner_user_id, caller_kind, called_at)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;
