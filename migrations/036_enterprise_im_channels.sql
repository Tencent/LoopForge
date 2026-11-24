-- 036 Enterprise IM channels (docs/proposals/enterprise-im.md §5.4).
-- Apply manually in the Bot maintenance window, after stopping every old
-- replica and starting the new version once with
-- AGENTFLOW_BOT_WORKERS_ENABLED=false (its startup incremental DDL adds the
-- columns, indexes and the bot_native_sessions table listed in section 1).
-- Run the sections in order; stop at the first error. Identifier equality
-- uses BINARY so tables with different collations compare exactly.

-- Section 1. Schema check: every row below must report present = 1. If any
-- is 0, the new version has not started yet; start it (workers disabled)
-- instead of hand-writing DDL.
SELECT t.table_name, t.column_name,
       (SELECT COUNT(*) FROM information_schema.columns c
         WHERE c.table_schema = DATABASE() AND BINARY c.table_name = BINARY t.table_name AND BINARY c.column_name = BINARY t.column_name) AS present
FROM (
  SELECT 'notification_bots' AS table_name, 'channel' AS column_name UNION ALL
  SELECT 'notification_bots', 'external_app_id' UNION ALL
  SELECT 'notification_bots', 'tenant_key' UNION ALL
  SELECT 'notification_bots', 'receive_slot' UNION ALL
  SELECT 'notification_bots', 'enabled' UNION ALL
  SELECT 'notification_bots', 'interaction_enabled' UNION ALL
  SELECT 'notification_bots', 'config_version' UNION ALL
  SELECT 'notification_bots', 'credential_version' UNION ALL
  SELECT 'bot_connections', 'lease_until_us' UNION ALL
  SELECT 'bot_connections', 'epoch' UNION ALL
  SELECT 'bot_interactions', 'project_id' UNION ALL
  SELECT 'bot_interactions', 'send_key' UNION ALL
  SELECT 'bot_user_bindings', 'config_id' UNION ALL
  SELECT 'bot_binding_challenges', 'config_id' UNION ALL
  SELECT 'bot_native_sessions', 'native_session_id'
) t;

-- Section 2. Read-only conflict check: enabled interactive configurations
-- that would share one receive slot. Resolve every row (disable or delete
-- the duplicate in the old payload) before section 3, otherwise the unique
-- index uq_notification_bots_receive_slot rejects the backfill.
SELECT COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.channel')), ''), 'wecom') AS channel,
       JSON_UNQUOTE(JSON_EXTRACT(payload, '$.botId')) AS external_app_id,
       COUNT(*) AS enabled_receivers, GROUP_CONCAT(id ORDER BY id) AS config_ids
FROM notification_bots
WHERE JSON_UNQUOTE(JSON_EXTRACT(payload, '$.enabled')) = 'true'
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.interactionEnabled')) = 'true'
GROUP BY 1, 2
HAVING COUNT(*) > 1;

-- Section 3. Backfill physical columns from the JSON payload. Rows already
-- written by the new version (config_version > 0) are left untouched, so the
-- section can be re-run. Legacy rows without a channel are WeCom.
START TRANSACTION;
UPDATE notification_bots
SET channel = COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.channel')), ''), 'wecom'),
    external_app_id = COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.botId')), ''),
    enabled = IF(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.enabled')) = 'true', 1, 0),
    interaction_enabled = IF(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.interactionEnabled')) = 'true', 1, 0)
WHERE config_version = 0;
UPDATE notification_bots
SET receive_slot = IF(enabled = 1 AND interaction_enabled = 1, CONCAT(channel, ':', SHA2(external_app_id, 256)), NULL),
    credential_version = IF(EXISTS (SELECT 1 FROM user_bot_credentials c WHERE BINARY c.bot_id = BINARY notification_bots.id), 1, 0),
    config_version = 1
WHERE config_version = 0;
COMMIT;

-- Section 4. Interaction record project snapshot (record-page access after
-- a Bot is deleted). Re-runnable.
UPDATE bot_interactions i
JOIN notification_bots b ON BINARY b.id = BINARY i.bot_id
SET i.project_id = b.project_id
WHERE i.project_id IS NULL OR i.project_id = '';

-- Section 5. Verification: counts to record before and after the window.
SELECT channel, COUNT(*) AS configs, SUM(receive_slot IS NOT NULL) AS receivers,
       SUM(config_version = 0) AS not_backfilled
FROM notification_bots GROUP BY channel;
SELECT COUNT(*) AS interactions_without_project FROM bot_interactions WHERE project_id IS NULL OR project_id = '';
