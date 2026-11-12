-- Optional manual additive DDL. Startup AutoMigrate also adds this column and
-- table. No backfill: replies that ended before finished_at existed keep NULL
-- and are never marked unread, so upgrading does not light up old history.
SET @asst_finished_at_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema=DATABASE() AND table_name='assistant_generations' AND column_name='finished_at')=0,
 'ALTER TABLE assistant_generations ADD COLUMN finished_at DATETIME(3) NULL',
 'SELECT 1');
PREPARE asst_finished_at_stmt FROM @asst_finished_at_ddl;
EXECUTE asst_finished_at_stmt;
DEALLOCATE PREPARE asst_finished_at_stmt;

-- The latest reply each reader has seen per conversation (sidebar unread dot).
CREATE TABLE IF NOT EXISTS assistant_session_reads (
  user_id BIGINT NOT NULL,
  session_id VARCHAR(64) NOT NULL,
  generation_id VARCHAR(64),
  updated_at DATETIME(3),
  PRIMARY KEY (user_id, session_id),
  INDEX idx_assistant_session_reads_session (session_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
