-- External workflow usage. Optional baseline, manually applied by operators.
-- Production Open only adds missing schema objects; no data migration or seed.
CREATE TABLE IF NOT EXISTS insight_sources (
  id VARCHAR(64) NOT NULL PRIMARY KEY,
  project_id VARCHAR(128),
  token_hash VARCHAR(64),
  payload JSON,
  expires_at DATETIME(3),
  revoked_at DATETIME(3),
  KEY idx_insight_sources_project_id (project_id),
  UNIQUE KEY idx_insight_sources_token_hash (token_hash)
) DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS insight_executions (
  id VARCHAR(64) NOT NULL PRIMARY KEY,
  execution_key VARCHAR(64),
  source_id VARCHAR(64),
  project_id VARCHAR(128),
  user_id BIGINT,
  payload JSON,
  content_hash VARCHAR(64),
  finished_at DATETIME(3),
  received_at DATETIME(3),
  UNIQUE KEY idx_insight_executions_execution_key (execution_key),
  KEY idx_insight_executions_source_id (source_id),
  KEY idx_insight_executions_user_id (user_id),
  KEY idx_insight_project_finished (project_id, finished_at)
) DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
