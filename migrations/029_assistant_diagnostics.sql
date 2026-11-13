-- Optional manual additive DDL. Startup AutoMigrate also adds this table.
-- Diagnostic records survive retries and are deleted with their conversation.
CREATE TABLE IF NOT EXISTS assistant_diagnostics (
  generation_id VARCHAR(64) NOT NULL,
  session_id VARCHAR(64) NOT NULL,
  user_id BIGINT NOT NULL,
  payload JSON,
  updated_at DATETIME(3),
  PRIMARY KEY (generation_id),
  INDEX idx_assistant_diagnostics_session (session_id, user_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
