-- Manual additive migration for the floating page feedback widget. Reports and
-- their images are standalone: no foreign keys to projects, issues or users.
-- gorm's incremental DDL creates the same objects on start; this script exists
-- for environments that apply DDL by hand.
CREATE TABLE IF NOT EXISTS page_feedbacks (
  id VARCHAR(64) NOT NULL PRIMARY KEY,
  title VARCHAR(255),
  description TEXT,
  url VARCHAR(2048),
  route VARCHAR(512),
  view VARCHAR(128),
  frame_url VARCHAR(2048),
  context JSON,
  element JSON NULL,
  user_id BIGINT,
  login_name VARCHAR(128),
  chinese_name VARCHAR(128),
  created_at DATETIME(3) NULL,
  INDEX idx_page_feedbacks_created (created_at),
  INDEX idx_page_feedbacks_login (login_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS page_feedback_images (
  id VARCHAR(64) NOT NULL PRIMARY KEY,
  feedback_id VARCHAR(64),
  position BIGINT,
  file_name VARCHAR(255),
  mime_type VARCHAR(64),
  size BIGINT,
  sha256 VARCHAR(64),
  data LONGBLOB,
  created_at DATETIME(3) NULL,
  INDEX idx_page_feedback_images_feedback (feedback_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
