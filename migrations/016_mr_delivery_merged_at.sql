-- Optional manual additive DDL. Startup AutoMigrate also adds this column/index.
-- Never copy checked_at/updated_at into merged_at: they are observation times.
-- The existing Gongfeng background sync re-queries merged MRs with NULL merged_at
-- and stores merged_at, or resolved_at only when the remote state is merged.
SET @mr_merged_at_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema=DATABASE() AND table_name='mr_delivery_states' AND column_name='merged_at')=0,
 'ALTER TABLE mr_delivery_states ADD COLUMN merged_at DATETIME(3) NULL',
 'SELECT 1');
PREPARE mr_merged_at_stmt FROM @mr_merged_at_ddl;
EXECUTE mr_merged_at_stmt;
DEALLOCATE PREPARE mr_merged_at_stmt;

SET @mr_merged_at_index_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.statistics
  WHERE table_schema=DATABASE() AND table_name='mr_delivery_states' AND index_name='idx_mr_delivery_merged_at')=0,
 'CREATE INDEX idx_mr_delivery_merged_at ON mr_delivery_states (merged_at)',
 'SELECT 1');
PREPARE mr_merged_at_index_stmt FROM @mr_merged_at_index_ddl;
EXECUTE mr_merged_at_index_stmt;
DEALLOCATE PREPARE mr_merged_at_index_stmt;
