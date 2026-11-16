-- Run against existing schemas before starting the updated platform.
ALTER TABLE user_configs ADD COLUMN gitlab_token TEXT NULL;
ALTER TABLE user_configs ADD COLUMN gitlab_url TEXT NULL;
ALTER TABLE runners ADD COLUMN supports_gitlab_pat BOOLEAN NOT NULL DEFAULT FALSE;
