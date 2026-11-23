-- Historical DDL for the removed page-feedback widget. Do not apply this on a
-- new database: the button and /api/page-feedback are gone, and startup no
-- longer creates these tables. Existing rows are left in place; this script
-- does not drop them.
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
