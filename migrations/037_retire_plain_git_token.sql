-- Run after 036_git_http_credentials.sql. Personal HTTPS Git credentials
-- now live only in user_git_http_credentials; execution never falls back to
-- user_configs.git_token. Keep the deployment adapter's refreshing OAuth
-- grant, which is exposed through the managed host credential resolver.
UPDATE user_configs c
JOIN user_git_http_credentials h ON h.user_id = c.user_id AND h.host = 'github.com'
SET c.git_token = NULL
WHERE COALESCE(TRIM(c.git_token), '') <> ''
  AND COALESCE(TRIM(c.gongfeng_refresh_token), '') = ''
  AND LOWER(TRIM(c.git_token)) NOT LIKE 'oauth2:%'
  AND LOWER(TRIM(c.git_token)) NOT LIKE 'private:%';


-- All configured credentials are available to the owner's workspace; remove
-- the former exposure toggle from deployments that already ran version 036.
SET @drop_git_http_expose = IF(
 (SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema=DATABASE() AND table_name='user_git_http_credentials' AND column_name='expose_to_workspace') > 0,
 'ALTER TABLE user_git_http_credentials DROP COLUMN expose_to_workspace',
 'SELECT 1');
PREPARE drop_git_http_expose_stmt FROM @drop_git_http_expose;
EXECUTE drop_git_http_expose_stmt;
DEALLOCATE PREPARE drop_git_http_expose_stmt;
