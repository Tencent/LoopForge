-- Optional manual additive DDL. Startup AutoMigrate also adds these columns.
-- A running turn records the foreground workspace lease it holds, so a reader
-- that reaps the turn after its process died releases exactly that lease.
-- No backfill: turns started before the columns existed record nothing and
-- keep the old behaviour (their lease runs out after 15 minutes).
-- run_workspace_checkpoints.lease_renewed_at stamps each lease take and renewal
-- with the database clock, so a replica judges how long ago another replica
-- renewed without comparing two hosts' clocks. A lease taken before the column
-- existed has no stamp and is judged by its lease_until as before.
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

SET @ws_lease_renewed_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema=DATABASE() AND table_name='run_workspace_checkpoints' AND column_name='lease_renewed_at')=0,
 'ALTER TABLE run_workspace_checkpoints ADD COLUMN lease_renewed_at DATETIME(3) NULL',
 'SELECT 1');
PREPARE ws_lease_renewed_stmt FROM @ws_lease_renewed_ddl;
EXECUTE ws_lease_renewed_stmt;
DEALLOCATE PREPARE ws_lease_renewed_stmt;
