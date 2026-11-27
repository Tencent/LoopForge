-- Import the single-connection OpenSandbox preview after starting the build
-- that creates execution_clusters. Existing installations without the preview
-- columns have no settings to import.
SET @opensandbox_preview_columns = (
  SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'platform_runner_settings'
    AND column_name IN ('open_sandbox_url', 'open_sandbox_api_key', 'open_sandbox_secure')
);
SET @import_opensandbox = IF(
  @opensandbox_preview_columns = 3,
  'INSERT IGNORE INTO execution_clusters (id, name, url, api_key, secure_access, enabled, project_ids)
   SELECT ''opensandbox-preview'', ''OpenSandbox 试用连接'', open_sandbox_url, COALESCE(open_sandbox_api_key, ''''), COALESCE(open_sandbox_secure, 0), 1, ''[]''
     FROM platform_runner_settings WHERE id = 1 AND COALESCE(open_sandbox_url, '''') <> ''''',
  'SELECT 1'
);
PREPARE import_opensandbox FROM @import_opensandbox;
EXECUTE import_opensandbox;
DEALLOCATE PREPARE import_opensandbox;
