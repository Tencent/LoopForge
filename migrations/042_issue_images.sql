CREATE TABLE IF NOT EXISTS issue_images (
  issue_id VARCHAR(64) NOT NULL,
  id VARCHAR(32) NOT NULL,
  name VARCHAR(255),
  media_type VARCHAR(32),
  data MEDIUMBLOB,
  position INT,
  PRIMARY KEY (issue_id, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
