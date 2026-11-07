-- Optional manual DDL; repo.Open AutoMigrate also adds this table.
-- Stores the latest generation per private conversation. No baseline data.
CREATE TABLE IF NOT EXISTS assistant_generations (
  session_id VARCHAR(64) NOT NULL PRIMARY KEY,
  user_id BIGINT NOT NULL,
  generation_id VARCHAR(64) NOT NULL,
  status VARCHAR(24) NOT NULL,
  cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
  payload JSON,
  updated_at DATETIME(3),
  INDEX idx_assistant_generations_user_id (user_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
