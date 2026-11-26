-- 052: Agent 知识库引用记录（控制面，KnowledgeRepo）。
--
-- 检索、读原文和精确匹配每次命中写一行，记下运行或会话、Agent、来源、
-- 版本和行范围。治理用它统计引用次数，以及时间窗内从未被引用的文档。
-- 写入失败不影响工具结果。每个项目只保留最近 90 天、且不超过 2000 行；
-- 窗口外的行由写入路径删除。
--
-- 本脚本可重复执行：建表用 IF NOT EXISTS，索引写在建表语句里，
-- 不另执行无 IF NOT EXISTS 的 ALTER 或 CREATE INDEX。

CREATE TABLE IF NOT EXISTS knowledge_citations (
  id          BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
  project_id  VARCHAR(64)  NOT NULL DEFAULT '',
  agent_id    VARCHAR(64)  NOT NULL DEFAULT '',
  user_id     BIGINT       NOT NULL DEFAULT 0,
  run_id      VARCHAR(64)  NOT NULL DEFAULT '',
  session_id  VARCHAR(64)  NOT NULL DEFAULT '',
  tool        VARCHAR(64)  NOT NULL DEFAULT '',
  source_id   VARCHAR(64)  NOT NULL DEFAULT '',
  path        VARCHAR(512) NOT NULL DEFAULT '',
  commit      VARCHAR(128) NOT NULL DEFAULT '',
  start_line  INT          NOT NULL DEFAULT 0,
  end_line    INT          NOT NULL DEFAULT 0,
  chunk_id    BIGINT       NOT NULL DEFAULT 0,
  base_id     VARCHAR(64)  NOT NULL DEFAULT '',
  layer       VARCHAR(32)  NOT NULL DEFAULT '',
  created_at  DATETIME(3)  NULL,
  KEY idx_knowledge_citation_project (project_id, id),
  KEY idx_knowledge_citation_doc (project_id, source_id, path)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;
