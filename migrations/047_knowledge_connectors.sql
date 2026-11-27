-- 047: 个人只读知识库连接器表（knowledge_connectors）
--
-- 背景：LoopForge 不再内置/托管知识库后端引擎，改为由每位用户在自己名下接入
-- 个人只读知识库连接器（如 WeKnora REST、通用只读知识 MCP 等），对话与 SOP
-- 运行时按当前执行人/提问人严格隔离引用。
--
-- 说明：
-- - gorm 增量迁移（autoMigrateIncremental）同样会补建本表；此脚本供预建或运维执行。
-- - secret_enc 保存 AES-256-GCM 加密后的上游凭据 JSON，绝不返回前端。
-- - 历史表 knowledge_settings / knowledge_submissions 保留不做破坏性删表。

CREATE TABLE IF NOT EXISTS knowledge_connectors (
  id            VARCHAR(64) NOT NULL PRIMARY KEY,
  owner_user_id BIGINT NOT NULL DEFAULT 0,
  name          VARCHAR(128) NOT NULL,
  type          VARCHAR(32) NOT NULL,
  endpoint      TEXT NOT NULL,
  config_json   JSON NULL,
  secret_enc    TEXT NULL,
  enabled       TINYINT(1) NOT NULL DEFAULT 1,
  created_at    DATETIME(3) NULL,
  updated_at    DATETIME(3) NULL,
  KEY idx_knowledge_connectors_owner (owner_user_id)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;
