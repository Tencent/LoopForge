-- 048: 知识库与文档调用计数表（knowledge_connector_usage_counts / knowledge_document_usage_counts）
--
-- 说明：
-- - 记录每位用户名下各知识库连接器与文档被 Agent/模型工具调用的累计次数。
-- - 仅统计真实模型工具调用（model_tool_call），不统计后台目录探测（catalog_probe）。
-- - gorm 增量迁移（autoMigrateIncremental）同样会补建本表；此脚本供预建或运维执行。

CREATE TABLE IF NOT EXISTS knowledge_connector_usage_counts (
  owner_user_id      BIGINT NOT NULL DEFAULT 0,
  connector_id       VARCHAR(64) NOT NULL,
  call_count         BIGINT NOT NULL DEFAULT 0,
  search_count       BIGINT NOT NULL DEFAULT 0,
  read_count         BIGINT NOT NULL DEFAULT 0,
  document_hit_count BIGINT NOT NULL DEFAULT 0,
  error_count        BIGINT NOT NULL DEFAULT 0,
  last_called_at     DATETIME(3) NULL,
  created_at         DATETIME(3) NULL,
  updated_at         DATETIME(3) NULL,
  PRIMARY KEY (owner_user_id, connector_id)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS knowledge_document_usage_counts (
  owner_user_id    BIGINT NOT NULL DEFAULT 0,
  connector_id     VARCHAR(64) NOT NULL,
  document_key     VARCHAR(255) NOT NULL,
  source_id        VARCHAR(191) NOT NULL DEFAULT '',
  source_name      VARCHAR(255) NOT NULL DEFAULT '',
  document_id      VARCHAR(512) NOT NULL DEFAULT '',
  title            VARCHAR(255) NOT NULL DEFAULT '',
  file_name        VARCHAR(255) NOT NULL DEFAULT '',
  call_count       BIGINT NOT NULL DEFAULT 0,
  search_hit_count BIGINT NOT NULL DEFAULT 0,
  read_count       BIGINT NOT NULL DEFAULT 0,
  last_called_at   DATETIME(3) NULL,
  created_at       DATETIME(3) NULL,
  updated_at       DATETIME(3) NULL,
  PRIMARY KEY (owner_user_id, connector_id, document_key),
  KEY idx_knowledge_doc_usage_owner_calls (owner_user_id, call_count)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;
