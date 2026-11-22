-- Personal HTTPS Git credentials, one per user and host[:port]. The id is the
-- hex SHA-256 of the host, so a host keeps one credential and re-running this
-- script is idempotent. Tokens are never returned by the settings API;
-- the owner's workspace requests them from its authenticated Git helper.
CREATE TABLE IF NOT EXISTS user_git_http_credentials (
  user_id BIGINT NOT NULL,
  id VARCHAR(64) NOT NULL,
  name VARCHAR(80) NOT NULL,
  platform VARCHAR(16) NOT NULL,
  host VARCHAR(255) NOT NULL,
  username VARCHAR(128) NOT NULL DEFAULT '',
  token TEXT NOT NULL,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  PRIMARY KEY (user_id, id)
);

-- Run before starting the version that reads personal Git credentials by host.
-- Copy personal access tokens and GitHub device-flow grants to github.com.
-- The deployment adapter keeps its managed grants (a refresh token or an
-- "oauth2:"/"private:" prefix) in user_configs.
-- Preserve existing host credentials and surface other insert errors.
INSERT INTO user_git_http_credentials (
  user_id, id, name, platform, host, username, token, created_at, updated_at
)
SELECT
  c.user_id, SHA2('github.com', 256), 'GitHub', 'github', 'github.com', 'x-access-token',
  TRIM(c.git_token), UTC_TIMESTAMP(3), UTC_TIMESTAMP(3)
FROM user_configs c
WHERE COALESCE(TRIM(c.git_token), '') <> ''
  AND COALESCE(TRIM(c.gongfeng_refresh_token), '') = ''
  AND LOWER(TRIM(c.git_token)) NOT LIKE 'oauth2:%'
  AND LOWER(TRIM(c.git_token)) NOT LIKE 'private:%'
ON DUPLICATE KEY UPDATE id = user_git_http_credentials.id;

-- The user who registered a delivered MR; background polling prefers that
-- user's credential for the MR host. Startup AutoMigrate also adds it.
SET @mr_registered_by_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema=DATABASE() AND table_name='mr_delivery_states' AND column_name='registered_by')=0,
 'ALTER TABLE mr_delivery_states ADD COLUMN registered_by BIGINT NULL',
 'SELECT 1');
PREPARE mr_registered_by_stmt FROM @mr_registered_by_ddl;
EXECUTE mr_registered_by_stmt;
DEALLOCATE PREPARE mr_registered_by_stmt;
