-- Codex and Claude Code model catalogs. Agent CLI catalogs stay inside
-- agent_cli_credentials. Apply manually; startup AutoMigrate also adds these
-- columns. Empty means the chat follows the service's published models.

ALTER TABLE user_configs ADD COLUMN codex_models TEXT NULL;
ALTER TABLE user_configs ADD COLUMN claude_code_models TEXT NULL;
