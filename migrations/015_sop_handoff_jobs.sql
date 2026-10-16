-- Optional manual additive DDL; startup also adds missing objects incrementally.
-- No history is rewritten and no job is auto-authorized.
CREATE TABLE IF NOT EXISTS sop_handoff_jobs (
 run_id VARCHAR(64) NOT NULL PRIMARY KEY,
 id VARCHAR(64) NOT NULL,
 node_id VARCHAR(128) NOT NULL,
 node_name TEXT,
 actor_id BIGINT NOT NULL,
 status VARCHAR(24) NOT NULL,
 phase VARCHAR(24) NOT NULL,
 error TEXT,
 summary_path TEXT,
 summary_artifact_id VARCHAR(128),
 summary_preview TEXT,
 summary LONGBLOB,
 model_source_sha VARCHAR(64),
 created_at DATETIME(3),
 updated_at DATETIME(3),
 owner VARCHAR(128),
 lease_until DATETIME(3),
 attempt INT NOT NULL DEFAULT 0,
 UNIQUE KEY uq_sop_handoff_job (id),
 INDEX idx_sop_handoff_runnable (status, lease_until)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Idempotent addition for existing workspace-checkpoint tables.
SET @handoff_lease_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema=DATABASE() AND table_name='run_workspace_checkpoints' AND column_name='lease_handoff_id')=0,
 'ALTER TABLE run_workspace_checkpoints ADD COLUMN lease_handoff_id VARCHAR(64) NULL',
 'SELECT 1');
PREPARE handoff_lease_stmt FROM @handoff_lease_ddl;
EXECUTE handoff_lease_stmt;
DEALLOCATE PREPARE handoff_lease_stmt;
