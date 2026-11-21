-- Personal Codex ChatGPT logins. codex_chatgpt_login is the full auth.json,
-- refresh token included; it is never returned by the settings API and
-- executions receive a copy without the refresh token. The lease serializes
-- refreshes across instances because OpenAI rotates the refresh token.
-- gorm AutoMigrate also adds the columns on process start.

ALTER TABLE user_configs ADD COLUMN codex_chatgpt_login TEXT NULL;
ALTER TABLE user_configs ADD COLUMN codex_chatgpt_login_status VARCHAR(32) NULL;
ALTER TABLE user_configs ADD COLUMN codex_chatgpt_refresh_lease DATETIME(3) NULL;
