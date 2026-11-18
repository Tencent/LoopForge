-- Optional manual additive DDL. Startup AutoMigrate also adds this column.
-- Personal CNB (cnb.cool) access token, stored with the other user credentials.
-- Required token scopes: repo-code:rw and repo-pr:rw.
-- No backfill: users who never saved a CNB token keep it empty.
SET @cnb_token_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema=DATABASE() AND table_name='user_configs' AND column_name='cnb_token')=0,
 'ALTER TABLE user_configs ADD COLUMN cnb_token TEXT NULL',
 'SELECT 1');
PREPARE cnb_token_stmt FROM @cnb_token_ddl;
EXECUTE cnb_token_stmt;
DEALLOCATE PREPARE cnb_token_stmt;
