-- Optional manual DDL; repo.Open AutoMigrate also adds these columns.
-- Restoration progress is independent from the SOP's terminal execution state.
ALTER TABLE run_workspace_checkpoints
 ADD COLUMN restore_status VARCHAR(32) NULL,
 ADD COLUMN restore_started DATETIME(3) NULL,
 ADD COLUMN restore_error TEXT NULL;
