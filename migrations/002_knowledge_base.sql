-- 002: 知识库集成（lightcloud kb）基线表
--
-- 背景：侧边栏「知识库」模块保存平台级上游连接（地址、MCP Token、按项目的
-- 知识库来源范围）以及待人工审核后写回知识库的沉淀条目。
--
-- 说明：
-- - gorm 增量迁移（autoMigrateIncremental）同样会补建这两张表；此脚本供运维
--   在只读 DDL 账号或需要预建的环境手动执行，可重复执行。
-- - knowledge_settings.token 为上游 Bearer Token，单独成列，payload 中不含凭据。

CREATE TABLE IF NOT EXISTS knowledge_settings (
  id         VARCHAR(64) NOT NULL PRIMARY KEY,
  payload    JSON NULL,
  token      TEXT NULL,
  updated_at DATETIME(3) NULL
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS knowledge_submissions (
  id          VARCHAR(64) NOT NULL PRIMARY KEY,
  project_id  VARCHAR(64) NULL,
  run_id      VARCHAR(64) NULL,
  artifact_id VARCHAR(64) NULL,
  status      VARCHAR(32) NULL,
  payload     JSON NULL,
  created_at  DATETIME(3) NULL,
  updated_at  DATETIME(3) NULL,
  KEY idx_knowledge_submissions_project (project_id),
  KEY idx_knowledge_submissions_run (run_id),
  KEY idx_knowledge_submissions_artifact (artifact_id),
  KEY idx_knowledge_submissions_status (status)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;
