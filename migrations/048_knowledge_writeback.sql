-- 048: Git 来源知识写回记录（控制面，主库）。可重复执行。
--
-- 审核队列把条目发布到 Git 来源时，先推送一个写回分支，再按托管平台创建
-- 合并请求。本表记录分支、提交、合并请求链接与状态；合并后由同步器把
-- 新文档增量纳入索引。内容面不在这里，表上不建跨库外键。

CREATE TABLE IF NOT EXISTS knowledge_writebacks (
  id            VARCHAR(64)   NOT NULL PRIMARY KEY,
  submission_id VARCHAR(64)   NULL DEFAULT NULL,
  source_id     VARCHAR(64)   NOT NULL DEFAULT '',
  project_id    VARCHAR(64)   NOT NULL DEFAULT '',
  path          VARCHAR(512)  NOT NULL DEFAULT '',
  branch        VARCHAR(255)  NOT NULL DEFAULT '',
  `commit`      VARCHAR(64)   NOT NULL DEFAULT '',
  mr_url        VARCHAR(1024) NOT NULL DEFAULT '',
  mr_provider   VARCHAR(32)   NOT NULL DEFAULT '',
  mr_iid        INT           NOT NULL DEFAULT 0,
  mr_state      VARCHAR(32)   NOT NULL DEFAULT '',
  status        VARCHAR(32)   NOT NULL DEFAULT '',
  note          TEXT          NULL,
  error_text    TEXT          NULL,
  attempts      INT           NOT NULL DEFAULT 0,
  checked_at    DATETIME(3)   NULL,
  created_at    DATETIME(3)   NULL,
  updated_at    DATETIME(3)   NULL,
  UNIQUE KEY uq_knowledge_writebacks_submission (submission_id),
  KEY idx_knowledge_writebacks_source (source_id),
  KEY idx_knowledge_writebacks_status (status, checked_at)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;
