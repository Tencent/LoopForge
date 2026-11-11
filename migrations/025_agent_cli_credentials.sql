-- Personal credentials for the agent CLIs (OpenCode, Cursor, Antigravity,
-- Kimi, Pi, DeepSeek Harness). CodeBuddy, Codex and Claude Code keep their
-- existing columns. Apply manually; startup does not run this script.
-- gorm AutoMigrate also adds the column on process start.

ALTER TABLE user_configs ADD COLUMN agent_cli_credentials TEXT NULL;
