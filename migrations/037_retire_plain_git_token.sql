-- Run after 036_git_http_credentials.sql, after verifying the backfill and
-- allowing time for rollback. Clear only verified copies of legacy tokens.
UPDATE user_configs c
JOIN user_git_http_credentials h ON h.user_id = c.user_id
  AND h.id = SHA2('github.com', 256)
  AND CAST(h.host AS BINARY) = 'github.com'
SET c.git_token = NULL
WHERE COALESCE(TRIM(c.git_token), '') <> ''
  AND COALESCE(TRIM(c.gongfeng_refresh_token), '') = ''
  AND LOWER(TRIM(c.git_token)) NOT LIKE 'oauth2:%'
  AND LOWER(TRIM(c.git_token)) NOT LIKE 'private:%'
  -- Tokens are case-sensitive even when the database collation is not.
  AND CAST(h.token AS BINARY) = CAST(TRIM(c.git_token) AS BINARY);

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
