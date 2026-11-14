-- Optional manual additive DDL. Startup AutoMigrate also adds these columns.
-- A running turn records the foreground workspace lease it holds, so a reader
-- that reaps the turn after its process died releases exactly that lease.
-- No backfill: turns started before the columns existed record nothing and
-- keep the old behaviour (their lease runs out after 15 minutes).
SET @asst_ws_run_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema=DATABASE() AND table_name='assistant_generations' AND column_name='workspace_run_id')=0,
 'ALTER TABLE assistant_generations ADD COLUMN workspace_run_id VARCHAR(64) NULL',
 'SELECT 1');
PREPARE asst_ws_run_stmt FROM @asst_ws_run_ddl;
EXECUTE asst_ws_run_stmt;
DEALLOCATE PREPARE asst_ws_run_stmt;

SET @asst_ws_lease_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema=DATABASE() AND table_name='assistant_generations' AND column_name='workspace_lease')=0,
 'ALTER TABLE assistant_generations ADD COLUMN workspace_lease VARCHAR(64) NULL',
 'SELECT 1');
PREPARE asst_ws_lease_stmt FROM @asst_ws_lease_ddl;
EXECUTE asst_ws_lease_stmt;
DEALLOCATE PREPARE asst_ws_lease_stmt;
