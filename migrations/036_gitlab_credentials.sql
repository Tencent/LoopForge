-- Optional manual additive DDL. Startup AutoMigrate also adds these columns.
SET @gitlab_token_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema=DATABASE() AND table_name='user_configs' AND column_name='gitlab_token')=0,
 'ALTER TABLE user_configs ADD COLUMN gitlab_token TEXT NULL',
 'SELECT 1');
PREPARE gitlab_token_stmt FROM @gitlab_token_ddl;
EXECUTE gitlab_token_stmt;
DEALLOCATE PREPARE gitlab_token_stmt;

SET @gitlab_url_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema=DATABASE() AND table_name='user_configs' AND column_name='gitlab_url')=0,
 'ALTER TABLE user_configs ADD COLUMN gitlab_url TEXT NULL',
 'SELECT 1');
PREPARE gitlab_url_stmt FROM @gitlab_url_ddl;
EXECUTE gitlab_url_stmt;
DEALLOCATE PREPARE gitlab_url_stmt;

SET @supports_gitlab_pat_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema=DATABASE() AND table_name='runners' AND column_name='supports_gitlab_pat')=0,
 'ALTER TABLE runners ADD COLUMN supports_gitlab_pat BOOLEAN NOT NULL DEFAULT FALSE',
 'SELECT 1');
PREPARE supports_gitlab_pat_stmt FROM @supports_gitlab_pat_ddl;
EXECUTE supports_gitlab_pat_stmt;
DEALLOCATE PREPARE supports_gitlab_pat_stmt;
