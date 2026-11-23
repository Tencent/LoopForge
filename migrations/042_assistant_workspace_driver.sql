-- Optional manual additive DDL; startup AutoMigrate also adds this column.
-- Existing users keep leaving the conversation workspace sandbox to the agent.
SET @assistant_workspace_driver_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema=DATABASE() AND table_name='assistant_preferences' AND column_name='workspace_driver')=0,
 'ALTER TABLE assistant_preferences ADD COLUMN workspace_driver VARCHAR(16) NOT NULL DEFAULT ''''',
 'SELECT 1');
PREPARE assistant_workspace_driver_stmt FROM @assistant_workspace_driver_ddl;
EXECUTE assistant_workspace_driver_stmt;
DEALLOCATE PREPARE assistant_workspace_driver_stmt;
