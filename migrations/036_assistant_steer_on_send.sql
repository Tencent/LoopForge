-- Optional manual additive DDL; startup AutoMigrate also adds this column.
-- Existing users keep queueing messages sent during a running reply.
SET @assistant_steer_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema=DATABASE() AND table_name='assistant_preferences' AND column_name='steer_on_send')=0,
 'ALTER TABLE assistant_preferences ADD COLUMN steer_on_send TINYINT(1) NOT NULL DEFAULT 0',
 'SELECT 1');
PREPARE assistant_steer_stmt FROM @assistant_steer_ddl;
EXECUTE assistant_steer_stmt;
DEALLOCATE PREPARE assistant_steer_stmt;
