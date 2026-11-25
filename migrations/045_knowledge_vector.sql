-- 045: 知识库向量检索配置（阶段二提前）。
--
-- 只给控制面 knowledge_bases 增加 4 个列，全部在主库执行。向量本身不落
-- MySQL（Qdrant 是缓存，可从正文/索引块重建），所以不新增内容表。
--
-- 向量按知识库开启：vector_enabled 默认 0，且平台未配置 Qdrant/embedding
-- 时引擎完全不调用向量通道，行为与 043 的纯关键词检索一致（只有 MySQL
-- 也能用）。embedding_model / embedding_dim / qdrant_collection 是「索引
-- 契约」快照：换模型必须换 collection 并全量重建，避免不同模型向量混用。
--
-- 可重复执行：MySQL 的 ALTER TABLE ... ADD COLUMN 没有 IF NOT EXISTS，因此
-- 逐列查 information_schema.COLUMNS，缺列才用动态 SQL 补；已存在执行 DO 0。
-- 与 043 补全文索引用的是同一种写法。

SET @kb_db := DATABASE();

SET @has_vector_enabled := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = @kb_db AND TABLE_NAME = 'knowledge_bases' AND COLUMN_NAME = 'vector_enabled'
);
SET @add_vector_enabled := IF(@has_vector_enabled = 0,
  'ALTER TABLE knowledge_bases ADD COLUMN vector_enabled TINYINT(1) NOT NULL DEFAULT 0',
  'DO 0');
PREPARE stmt FROM @add_vector_enabled; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_embedding_model := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = @kb_db AND TABLE_NAME = 'knowledge_bases' AND COLUMN_NAME = 'embedding_model'
);
SET @add_embedding_model := IF(@has_embedding_model = 0,
  'ALTER TABLE knowledge_bases ADD COLUMN embedding_model VARCHAR(255) NOT NULL DEFAULT ''''',
  'DO 0');
PREPARE stmt FROM @add_embedding_model; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_embedding_dim := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = @kb_db AND TABLE_NAME = 'knowledge_bases' AND COLUMN_NAME = 'embedding_dim'
);
SET @add_embedding_dim := IF(@has_embedding_dim = 0,
  'ALTER TABLE knowledge_bases ADD COLUMN embedding_dim INT NOT NULL DEFAULT 0',
  'DO 0');
PREPARE stmt FROM @add_embedding_dim; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @has_qdrant_collection := (
  SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = @kb_db AND TABLE_NAME = 'knowledge_bases' AND COLUMN_NAME = 'qdrant_collection'
);
SET @add_qdrant_collection := IF(@has_qdrant_collection = 0,
  'ALTER TABLE knowledge_bases ADD COLUMN qdrant_collection VARCHAR(255) NOT NULL DEFAULT ''''',
  'DO 0');
PREPARE stmt FROM @add_qdrant_collection; EXECUTE stmt; DEALLOCATE PREPARE stmt;
