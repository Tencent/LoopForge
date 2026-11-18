-- 037 Bot binding ownership (docs/proposals/enterprise-im.md §5.4 step 4).
-- Run after 036; valid under ONLY_FULL_GROUP_BY (the MySQL 8.4 default).
-- WeCom bindings are scoped by the external Bot ID; this
-- script records the owning configuration where it is unambiguous and lists
-- the rest for manual review. New-channel bindings always carry config_id.

-- Section 1. Report: binding scopes shared by several WeCom configurations.
-- These keep config_id empty; confirm the owner with the project and set it
-- by hand (UPDATE bot_user_bindings SET config_id = ... WHERE bot_id = ...).
SELECT MIN(b.external_app_id) AS bot_scope, COUNT(*) AS configs, GROUP_CONCAT(b.id ORDER BY b.id) AS config_ids,
       (SELECT COUNT(*) FROM bot_user_bindings u WHERE BINARY u.bot_id = BINARY MIN(b.external_app_id) AND u.user_id > 0) AS bound_users
FROM notification_bots b
WHERE b.channel = 'wecom'
GROUP BY BINARY b.external_app_id
HAVING COUNT(*) > 1;

-- Section 2. Report: orphan bindings whose scope matches no configuration.
SELECT u.id, u.bot_id AS bot_scope, u.user_id
FROM bot_user_bindings u
LEFT JOIN notification_bots b ON b.channel = 'wecom' AND BINARY b.external_app_id = BINARY u.bot_id
WHERE b.id IS NULL AND u.user_id > 0 AND (u.config_id IS NULL OR u.config_id = '');

-- Section 3. Backfill unambiguous WeCom ownership. Re-runnable.
START TRANSACTION;
UPDATE bot_user_bindings u
JOIN (SELECT MIN(external_app_id) AS external_app_id, MIN(id) AS config_id FROM notification_bots
      WHERE channel = 'wecom' GROUP BY BINARY external_app_id HAVING COUNT(*) = 1) owner
  ON BINARY owner.external_app_id = BINARY u.bot_id
SET u.config_id = owner.config_id, u.channel = 'wecom'
WHERE u.config_id IS NULL OR u.config_id = '';
UPDATE bot_binding_challenges c
JOIN bot_user_bindings u ON BINARY u.id = BINARY c.binding_id
SET c.config_id = u.config_id, c.channel = u.channel
WHERE (c.config_id IS NULL OR c.config_id = '') AND u.config_id <> '';
COMMIT;

-- Section 4. Native CLI sessions of Bot turns before this release have no
-- mapping. List them as legacy_unmapped for manual review (stage H cleans
-- only mapped sessions); nothing is deleted here.
SELECT 'legacy_unmapped' AS state, i.id AS interaction_id, i.bot_id AS config_id, i.session_id AS bot_session_id, i.created_at
FROM bot_interactions i
LEFT JOIN bot_native_sessions m ON BINARY m.interaction_id = BINARY i.id
WHERE m.id IS NULL AND i.action = '' AND i.status IN ('replied', 'reply_failed')
ORDER BY i.created_at;

-- Section 5. Verification.
SELECT COUNT(*) AS bindings, SUM(config_id IS NULL OR config_id = '') AS without_config FROM bot_user_bindings WHERE user_id > 0;
