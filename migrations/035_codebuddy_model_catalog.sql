-- Personal CodeBuddy model catalog, next to the Codex and Claude Code catalogs
-- from 027. Apply manually; startup AutoMigrate also adds this column. Empty
-- follows the models the CodeBuddy CLI publishes for the account.

ALTER TABLE user_configs ADD COLUMN codebuddy_models TEXT NULL;
