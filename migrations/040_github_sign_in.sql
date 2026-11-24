-- GitHub sign-in links a GitHub account ID to one LoopForge user.
-- Startup also creates this table when it is missing; this script is for
-- operators who manage the schema by hand.
CREATE TABLE IF NOT EXISTS user_github_identities (
  github_id BIGINT NOT NULL PRIMARY KEY,
  user_id BIGINT NOT NULL,
  login VARCHAR(64),
  UNIQUE KEY uq_user_github_identities_user (user_id)
);
