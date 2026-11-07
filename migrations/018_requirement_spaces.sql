-- Manual additive migration for conversation spaces (state layered on a
-- collaboration room, optionally carrying one requirement issue). gorm's incremental DDL creates the same objects on
-- start; this script exists for environments that apply DDL by hand.
ALTER TABLE chat_messages ADD COLUMN thread_id VARCHAR(64) NOT NULL DEFAULT '';
CREATE INDEX idx_chat_messages_thread ON chat_messages (thread_id);

CREATE TABLE IF NOT EXISTS requirement_spaces (
  id VARCHAR(64) NOT NULL PRIMARY KEY,
  project_id VARCHAR(64),
  issue_id VARCHAR(64),
  payload JSON,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  INDEX idx_requirement_spaces_project (project_id),
  INDEX idx_requirement_spaces_issue (issue_id)
);

CREATE TABLE IF NOT EXISTS space_members (
  space_id VARCHAR(64) NOT NULL,
  user_id BIGINT NOT NULL,
  payload JSON,
  created_at DATETIME(3) NULL,
  PRIMARY KEY (space_id, user_id)
);

CREATE TABLE IF NOT EXISTS space_artifacts (
  id VARCHAR(64) NOT NULL PRIMARY KEY,
  space_id VARCHAR(64),
  kind VARCHAR(32),
  version BIGINT,
  payload JSON,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  INDEX idx_space_artifacts_space (space_id)
);

CREATE TABLE IF NOT EXISTS space_artifact_revisions (
  artifact_id VARCHAR(64) NOT NULL,
  version BIGINT NOT NULL,
  space_id VARCHAR(64),
  payload JSON,
  created_at DATETIME(3) NULL,
  PRIMARY KEY (artifact_id, version)
);

CREATE TABLE IF NOT EXISTS space_threads (
  id VARCHAR(64) NOT NULL PRIMARY KEY,
  space_id VARCHAR(64),
  run_id VARCHAR(64),
  payload JSON,
  created_at DATETIME(3) NULL,
  INDEX idx_space_threads_space (space_id)
);
