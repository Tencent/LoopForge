-- Personal default engine for requirement clarification, stored as a
-- model id with the engine prefix ("codex:gpt-5.5"; unprefixed is CodeBuddy,
-- empty means "no choice" and keeps CodeBuddy). It is a preference, not a
-- secret, so the API returns it as is. Apply manually; startup does not run
-- this script.
-- gorm AutoMigrate also adds the column on process start.

ALTER TABLE user_configs ADD COLUMN clarify_model VARCHAR(191) NOT NULL DEFAULT '';
