-- 044: Agent 知识库工具调用记录（控制面，KnowledgeRepo）。
--
-- 知识中心的「Agent 最近的检索」在进程重启后仍要能读到角色/会话、工具、
-- 查询、命中数、成功与否、时间和运行 ID。记录由 KnowledgeRepo 写入主库，
-- 每次调用后尽力追加，写入失败不改变工具调用结果。
--
-- 保留窗口按项目裁剪（最近 200 条），由 repo 在写入后删除窗口外的旧行；
-- 表本身只保留有界行数。本脚本可重复执行：建表用 IF NOT EXISTS，不新增
-- 无 IF NOT EXISTS 的索引或列。

CREATE TABLE IF NOT EXISTS knowledge_activity (
  id          BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
  project_id  VARCHAR(64)  NOT NULL DEFAULT '',
  agent_id    VARCHAR(64)  NOT NULL DEFAULT '',
  user_id     BIGINT       NOT NULL DEFAULT 0,
  run_id      VARCHAR(64)  NOT NULL DEFAULT '',
  session_id  VARCHAR(64)  NOT NULL DEFAULT '',
  tool        VARCHAR(64)  NOT NULL DEFAULT '',
  query_text  VARCHAR(500) NOT NULL DEFAULT '',
  hits        INT          NOT NULL DEFAULT 0,
  ok          TINYINT(1)   NOT NULL DEFAULT 0,
  error_text  VARCHAR(512) NOT NULL DEFAULT '',
  latency_ms  BIGINT       NOT NULL DEFAULT 0,
  created_at  DATETIME(3)  NULL,
  KEY idx_knowledge_activity_project (project_id, id)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;
