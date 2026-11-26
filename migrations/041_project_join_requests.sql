-- Optional manual additive DDL. Startup AutoMigrate also creates this table.
-- Requests to join a project, reviewed by the project owner, maintainers or
-- platform admins. Approval inserts the applicant into project_members.
CREATE TABLE IF NOT EXISTS project_join_requests (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  project_id VARCHAR(64) NOT NULL,
  user_id BIGINT NOT NULL,
  role VARCHAR(32) NOT NULL,
  reason TEXT NULL,
  status VARCHAR(16) NOT NULL,
  reviewer_user_id BIGINT NULL,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  reviewed_at DATETIME(3) NULL,
  INDEX idx_project_join_requests_project (project_id),
  INDEX idx_project_join_requests_user (user_id)
);
