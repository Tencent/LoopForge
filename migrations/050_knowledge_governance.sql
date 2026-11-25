-- 050: 知识库长期治理（阶段四）。可重复执行。
--
-- 第一段在主库（控制面）：反馈、知识缺口、健康检查问题与最近扫描摘要。
-- 第二段在内容库（内容面）：knowledge_documents 增加 frontmatter 解析出的
-- 负责人、生命周期状态和更新时间。两段都不含跨库外键或跨库事务；内容面可
-- 单独执行在 AGENTFLOW_KNOWLEDGE_MYSQL_DSN 指向的库上。
--
-- 元数据列只对执行脚本之后新写入/覆盖的文档立即生效；历史行由内容 Repo
-- 在读取时从正文解析作为兜底，下一次覆盖写入会落列。

-- @plane control
-- ===== 第一段：主库（控制面，KnowledgeRepo） =====

CREATE TABLE IF NOT EXISTS knowledge_feedback (
  id          VARCHAR(64)  NOT NULL PRIMARY KEY,
  project_id  VARCHAR(64)  NOT NULL DEFAULT '',
  base_id     VARCHAR(64)  NOT NULL DEFAULT '',
  source_id   VARCHAR(64)  NOT NULL DEFAULT '',
  path        VARCHAR(512) NOT NULL DEFAULT '',
  title       VARCHAR(512) NOT NULL DEFAULT '',
  query_text  VARCHAR(500) NOT NULL DEFAULT '',
  kind        VARCHAR(32)  NOT NULL DEFAULT '',
  note        TEXT         NULL,
  surface     VARCHAR(32)  NOT NULL DEFAULT '',
  actor       VARCHAR(255) NOT NULL DEFAULT '',
  user_id     BIGINT       NOT NULL DEFAULT 0,
  agent_id    VARCHAR(64)  NOT NULL DEFAULT '',
  run_id      VARCHAR(64)  NOT NULL DEFAULT '',
  session_id  VARCHAR(64)  NOT NULL DEFAULT '',
  message_id  VARCHAR(64)  NOT NULL DEFAULT '',
  created_at  DATETIME(3)  NULL,
  KEY idx_knowledge_feedback_project (project_id, created_at)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

-- 同一项目内归一化查询做主键；重复出现只增加计数，不无限追加行。
CREATE TABLE IF NOT EXISTS knowledge_gaps (
  project_id       VARCHAR(64)  NOT NULL DEFAULT '',
  normalized_query VARCHAR(500) NOT NULL DEFAULT '',
  query_text       VARCHAR(500) NOT NULL DEFAULT '',
  occurrence_count INT          NOT NULL DEFAULT 1,
  first_seen       DATETIME(3)  NULL,
  last_seen        DATETIME(3)  NULL,
  PRIMARY KEY (project_id, normalized_query),
  KEY idx_knowledge_gaps_last_seen (last_seen)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

-- 健康检查问题按知识库整批替换；只存定位和中文说明，不存正文。
CREATE TABLE IF NOT EXISTS knowledge_governance_issues (
  id          BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
  base_id     VARCHAR(64)  NOT NULL DEFAULT '',
  source_id   VARCHAR(64)  NOT NULL DEFAULT '',
  path        VARCHAR(512) NOT NULL DEFAULT '',
  title       VARCHAR(512) NOT NULL DEFAULT '',
  issue_type  VARCHAR(32)  NOT NULL DEFAULT '',
  line        INT          NOT NULL DEFAULT 0,
  summary     TEXT         NULL,
  detected_at DATETIME(3)  NULL,
  KEY idx_knowledge_governance_issues_base (base_id, issue_type)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

-- 最近扫描摘要保留在 0 问题时也写入，面板才能显示最近检查时间。
CREATE TABLE IF NOT EXISTS knowledge_governance_scans (
  base_id     VARCHAR(64) NOT NULL PRIMARY KEY,
  scanned_at  DATETIME(3) NULL,
  doc_count   INT         NOT NULL DEFAULT 0,
  issue_count INT         NOT NULL DEFAULT 0
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

-- @plane content
-- ===== 第二段：内容库（内容面，KnowledgeContentRepo） =====

SET @ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE()
  AND table_name = 'knowledge_documents' AND column_name = 'owner') = 0,
 'ALTER TABLE knowledge_documents ADD COLUMN owner VARCHAR(255) NOT NULL DEFAULT ''''',
 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE()
  AND table_name = 'knowledge_documents' AND column_name = 'doc_status') = 0,
 'ALTER TABLE knowledge_documents ADD COLUMN doc_status VARCHAR(32) NOT NULL DEFAULT ''''',
 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE()
  AND table_name = 'knowledge_documents' AND column_name = 'frontmatter_updated_at') = 0,
 'ALTER TABLE knowledge_documents ADD COLUMN frontmatter_updated_at DATE NULL',
 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
