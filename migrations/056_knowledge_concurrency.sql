-- 056: knowledge concurrency and recovery. Safe to execute repeatedly.
-- @plane control
-- CONTROL DATABASE: run this section on the primary database.
CREATE TABLE IF NOT EXISTS knowledge_operation_leases (
 id VARCHAR(191) NOT NULL PRIMARY KEY,
 owner VARCHAR(255) NOT NULL DEFAULT '',
 dirty BOOLEAN NOT NULL DEFAULT FALSE,
 lease_until DATETIME(6) NULL
) DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Existing upstream reviews belong to LightCloud. The version column already
-- exists on built-in deployments, whose existing reviews keep their provider.
UPDATE knowledge_submissions
 SET payload=JSON_SET(COALESCE(payload, JSON_OBJECT()), '$.provider', 'lightcloud')
 WHERE JSON_EXTRACT(payload, '$.provider') IS NULL
 AND NOT EXISTS (
  SELECT 1 FROM information_schema.columns WHERE table_schema=DATABASE()
   AND table_name='knowledge_submissions' AND column_name='version');

SET @ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE()
  AND table_name='knowledge_submissions' AND column_name='version')=0,
 'ALTER TABLE knowledge_submissions ADD COLUMN version BIGINT UNSIGNED NOT NULL DEFAULT 1', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE()
  AND table_name='knowledge_submissions' AND column_name='fingerprint')=0,
 'ALTER TABLE knowledge_submissions ADD COLUMN fingerprint VARCHAR(64) NULL', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE()
  AND table_name='knowledge_writebacks' AND column_name='claim_owner')=0,
 'ALTER TABLE knowledge_writebacks ADD COLUMN claim_owner VARCHAR(255) NOT NULL DEFAULT ''''', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE()
  AND table_name='knowledge_writebacks' AND column_name='claimed_until')=0,
 'ALTER TABLE knowledge_writebacks ADD COLUMN claimed_until DATETIME(6) NULL', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl=IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE()
  AND table_name='knowledge_operation_leases' AND column_name='dirty')=0,
 'ALTER TABLE knowledge_operation_leases ADD COLUMN dirty BOOLEAN NOT NULL DEFAULT FALSE', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Claim one existing automatic candidate per project/fingerprint. Historical
-- duplicates stay readable; only the first candidate owns the deduplication key.
UPDATE knowledge_submissions s
JOIN (
 SELECT id, project_id, fp, ROW_NUMBER() OVER (PARTITION BY project_id,fp ORDER BY id) AS n
 FROM (
  SELECT id,project_id,JSON_UNQUOTE(JSON_EXTRACT(payload,'$.fingerprint')) AS fp
  FROM knowledge_submissions
  WHERE status IN ('pending','publishing','published')
   AND JSON_LENGTH(JSON_EXTRACT(payload,'$.sedimentSignals')) > 0
 ) candidates WHERE fp IS NOT NULL AND fp <> ''
) ranked ON ranked.id=s.id
SET s.fingerprint=ranked.fp
WHERE ranked.n=1 AND s.fingerprint IS NULL
 AND NOT EXISTS (SELECT 1 FROM (SELECT project_id,fingerprint FROM knowledge_submissions WHERE fingerprint IS NOT NULL) claimed
  WHERE claimed.project_id=s.project_id AND claimed.fingerprint=ranked.fp);

SET @ddl=IF(
 (SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE()
  AND table_name='knowledge_submissions' AND index_name='uq_knowledge_submission_fingerprint')=0,
 'CREATE UNIQUE INDEX uq_knowledge_submission_fingerprint ON knowledge_submissions(project_id,fingerprint)', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl=IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE()
  AND table_name='knowledge_sources' AND column_name='lease_version')=0,
 'ALTER TABLE knowledge_sources ADD COLUMN lease_version BIGINT NOT NULL DEFAULT 0', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl=IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE()
  AND table_name='knowledge_submissions' AND column_name='publish_until')=0,
 'ALTER TABLE knowledge_submissions ADD COLUMN publish_until DATETIME(6) NULL', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl=IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE()
  AND table_name='knowledge_submissions' AND column_name='content_key')=0,
 'ALTER TABLE knowledge_submissions ADD COLUMN content_key VARCHAR(64) NOT NULL DEFAULT ''''', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- @plane content
-- CONTENT DATABASE: run this section on the knowledge content database (the
-- same database by default). Fences have no foreign keys into the control plane.
CREATE TABLE IF NOT EXISTS knowledge_content_fences (
 source_id VARCHAR(64) NOT NULL,
 path VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
 token BIGINT NOT NULL DEFAULT 0,
 retracted BOOLEAN NOT NULL DEFAULT FALSE,
 PRIMARY KEY(source_id,path)
) DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

SET @ddl=IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE()
  AND table_name='knowledge_content_fences' AND column_name='token')=0,
 'ALTER TABLE knowledge_content_fences ADD COLUMN token BIGINT NOT NULL DEFAULT 0', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Immutable staging versions keep failed control CAS writes invisible. The
-- empty version key remains the legacy 057 migration target and read fallback.
CREATE TABLE IF NOT EXISTS knowledge_submission_contents (
 submission_id VARCHAR(64) NOT NULL,
 version_key VARCHAR(64) NOT NULL DEFAULT '',
 body MEDIUMTEXT NULL,
 updated_at DATETIME(3) NULL,
 PRIMARY KEY(submission_id,version_key)
) DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
SET @ddl=IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE()
  AND table_name='knowledge_submission_contents' AND column_name='version_key')=0,
 'ALTER TABLE knowledge_submission_contents ADD COLUMN version_key VARCHAR(64) NOT NULL DEFAULT ''''', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
SET @ddl=IF(
 (SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE()
  AND table_name='knowledge_submission_contents' AND index_name='PRIMARY')=1,
 'ALTER TABLE knowledge_submission_contents DROP PRIMARY KEY, ADD PRIMARY KEY(submission_id,version_key)', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl=IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE()
  AND table_name='knowledge_content_fences' AND column_name='path' AND collation_name <> 'utf8mb4_bin')>0,
 'ALTER TABLE knowledge_content_fences MODIFY path VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
