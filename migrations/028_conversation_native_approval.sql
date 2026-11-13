-- Optional manual DDL for deployments that provision schema separately.
-- Legacy assistant_sessions payloads without executionTarget decode as cloud;
-- no runner is inferred from personal defaults or retained workspaces.
CREATE TABLE IF NOT EXISTS assistant_native_approvals (
  id VARCHAR(64) NOT NULL PRIMARY KEY,
  user_id BIGINT NOT NULL,
  session_id VARCHAR(64) NOT NULL,
  generation_id VARCHAR(64) NOT NULL,
  actor_id BIGINT NOT NULL,
  tool VARCHAR(160) NOT NULL,
  input LONGBLOB,
  decision VARCHAR(16) NOT NULL,
  expires_at DATETIME(3) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  INDEX idx_native_approval_session (user_id, session_id)
);
