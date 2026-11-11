-- 028: 内置知识引擎（阶段一最小切片）的控制面与内容面表。
--
-- 分两段，段内都不含跨库外键或跨库事务。同一 MySQL 时整份执行；配置了独立
-- 内容库时，第一段在主库执行，第二段在内容库执行。由安装/升级脚本
-- （scripts/knowledge-schema.sh）或运维手动执行；进程启动不会跑这份脚本。
-- 段落用 "-- @plane control|content" 标记，脚本据此选择目标库。
--
-- 表结构与 internal/repo 的 row 模型一致。gorm 的增量 DDL 只会补缺失的表和
-- 列，补不出带 ngram 解析器的全文索引，所以检索要用的全文索引由第二段补建：
-- 即便表已由 autoMigrate 建好，仍须执行本脚本。
--
-- 本脚本可重复执行：dev.sh 每次启动都会执行，运维也可能重跑。建表一律用
-- IF NOT EXISTS、删表用 IF EXISTS；MySQL 的 ADD FULLTEXT INDEX 没有
-- IF NOT EXISTS，因此先查 information_schema.STATISTICS，索引已存在就跳过。

-- @plane control
-- ===== 第一段：主库（控制面，KnowledgeRepo） =====

-- 外部客户端与「地址加 Token」配置下线，这张表不再使用。
DROP TABLE IF EXISTS knowledge_settings;

CREATE TABLE IF NOT EXISTS knowledge_bases (
  id            VARCHAR(64)  NOT NULL PRIMARY KEY,
  project_id    VARCHAR(64)  NULL,
  name          VARCHAR(255) NOT NULL DEFAULT '',
  layer         VARCHAR(32)  NOT NULL DEFAULT 'project',
  owner_user_id BIGINT       NOT NULL DEFAULT 0,
  created_at    DATETIME(3)  NULL,
  updated_at    DATETIME(3)  NULL,
  KEY idx_knowledge_bases_project (project_id),
  UNIQUE KEY uq_knowledge_bases_project_layer (project_id, layer)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS knowledge_sources (
  id             VARCHAR(64)   NOT NULL PRIMARY KEY,
  base_id        VARCHAR(64)   NOT NULL DEFAULT '',
  kind           VARCHAR(32)   NOT NULL DEFAULT 'hosted',
  name           VARCHAR(255)  NOT NULL DEFAULT '',
  git_url        VARCHAR(1024) NOT NULL DEFAULT '',
  branch         VARCHAR(255)  NOT NULL DEFAULT '',
  path_prefix    VARCHAR(512)  NOT NULL DEFAULT '',
  head_commit    VARCHAR(128)  NOT NULL DEFAULT '',
  head_revision  BIGINT        NOT NULL DEFAULT 0,
  status         VARCHAR(32)   NOT NULL DEFAULT '',
  index_status   VARCHAR(32)   NOT NULL DEFAULT '',
  doc_count      INT           NOT NULL DEFAULT 0,
  last_error     TEXT          NULL,
  last_synced_at DATETIME(3)   NULL,
  lease_owner    VARCHAR(255)  NOT NULL DEFAULT '',
  lease_until    DATETIME(3)   NULL,
  KEY idx_knowledge_sources_base (base_id),
  KEY idx_knowledge_sources_status (status)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS project_knowledge_bindings (
  project_id VARCHAR(64) NOT NULL,
  base_id    VARCHAR(64) NOT NULL,
  permission VARCHAR(32) NOT NULL DEFAULT 'submit',
  PRIMARY KEY (project_id, base_id)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS knowledge_sync_jobs (
  id            BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
  source_id     VARCHAR(64)  NOT NULL DEFAULT '',
  kind          VARCHAR(32)  NOT NULL DEFAULT '',
  status        VARCHAR(32)  NOT NULL DEFAULT '',
  from_commit   VARCHAR(128) NOT NULL DEFAULT '',
  to_commit     VARCHAR(128) NOT NULL DEFAULT '',
  changed_count INT          NOT NULL DEFAULT 0,
  deleted_count INT          NOT NULL DEFAULT 0,
  `log`         TEXT         NULL,
  started_at    DATETIME(3)  NULL,
  finished_at   DATETIME(3)  NULL,
  KEY idx_knowledge_sync_jobs_source (source_id)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

-- @plane content
-- ===== 第二段：内容库（内容面，KnowledgeContentRepo） =====

CREATE TABLE IF NOT EXISTS knowledge_documents (
  source_id  VARCHAR(64)  NOT NULL,
  path       VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  title      VARCHAR(512) NOT NULL DEFAULT '',
  body       MEDIUMTEXT   NULL,
  line_count INT          NOT NULL DEFAULT 0,
  sha256     VARCHAR(64)  NOT NULL DEFAULT '',
  `commit`   VARCHAR(128) NOT NULL DEFAULT '',
  revision   BIGINT       NOT NULL DEFAULT 0,
  updated_at DATETIME(3)  NULL,
  PRIMARY KEY (source_id, path)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS knowledge_revisions (
  id         VARCHAR(64)  NOT NULL PRIMARY KEY,
  source_id  VARCHAR(64)  NOT NULL DEFAULT '',
  path       VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
  revision   BIGINT       NOT NULL DEFAULT 0,
  body       MEDIUMTEXT   NULL,
  author     VARCHAR(255) NOT NULL DEFAULT '',
  created_at DATETIME(3)  NULL,
  UNIQUE KEY uq_knowledge_revisions_path_rev (source_id, path, revision)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS knowledge_chunks (
  id         BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
  source_id  VARCHAR(64)  NOT NULL DEFAULT '',
  path       VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
  idx        INT          NOT NULL DEFAULT 0,
  heading    VARCHAR(512) NOT NULL DEFAULT '',
  title      VARCHAR(512) NOT NULL DEFAULT '',
  start_line INT          NOT NULL DEFAULT 0,
  end_line   INT          NOT NULL DEFAULT 0,
  `commit`   VARCHAR(128) NOT NULL DEFAULT '',
  revision   BIGINT       NOT NULL DEFAULT 0,
  `text`     MEDIUMTEXT   NULL,
  UNIQUE KEY uq_knowledge_chunks_path_idx (source_id, path, idx)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

-- 全文索引只建一次。用动态 SQL 绕过 MySQL 不支持 IF NOT EXISTS 的限制；
-- 已存在时执行 DO 0（空操作），整份脚本重跑不报 1061。
SET @has_ft_knowledge_chunks_text := (
  SELECT COUNT(*)
  FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'knowledge_chunks'
    AND INDEX_NAME = 'ft_knowledge_chunks_text'
);
SET @add_ft_knowledge_chunks_text := IF(
  @has_ft_knowledge_chunks_text = 0,
  'ALTER TABLE knowledge_chunks ADD FULLTEXT INDEX ft_knowledge_chunks_text (title, heading, `text`) WITH PARSER ngram',
  'DO 0'
);
PREPARE add_ft_knowledge_chunks_text FROM @add_ft_knowledge_chunks_text;
EXECUTE add_ft_knowledge_chunks_text;
DEALLOCATE PREPARE add_ft_knowledge_chunks_text;
