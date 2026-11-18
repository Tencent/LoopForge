-- 手动增量 DDL：进化资产互斥、后续改进队列与提案 CAS。
-- 维护窗口执行：先停止旧版本实例，避免新旧二进制混跑绕过互斥与 CAS。
-- 不包含 seed 或数据修复；预检如返回记录，需先人工检查并取消冲突候选。

SELECT asset_kind, asset_id, COUNT(*) AS current_proposals
FROM evo_proposals
WHERE status IN ('drafting', 'validating', 'pending', 'canary')
   OR (status = 'released' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.watch.status')) IN ('watching', 'degraded'))
GROUP BY asset_kind, asset_id
HAVING COUNT(*) > 1;

CREATE TABLE IF NOT EXISTS evo_asset_gates (
  asset_kind VARCHAR(16) NOT NULL,
  asset_id VARCHAR(128) NOT NULL,
  PRIMARY KEY (asset_kind, asset_id)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS evo_queue (
  id VARCHAR(64) NOT NULL PRIMARY KEY,
  project_id VARCHAR(64),
  status VARCHAR(32),
  finding_id VARCHAR(64),
  asset_kind VARCHAR(16),
  asset_id VARCHAR(128),
  payload JSON,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  UNIQUE INDEX idx_evo_queue_finding (finding_id),
  INDEX idx_evo_queue_project_status (project_id, status),
  INDEX idx_evo_queue_asset (asset_kind, asset_id)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

SET @evo_revision_ddl = (
  SELECT IF(COUNT(*) = 0,
    'ALTER TABLE evo_proposals ADD COLUMN revision BIGINT NOT NULL DEFAULT 0',
    'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'evo_proposals' AND column_name = 'revision'
);
PREPARE evo_revision_stmt FROM @evo_revision_ddl;
EXECUTE evo_revision_stmt;
DEALLOCATE PREPARE evo_revision_stmt;
