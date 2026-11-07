-- Manual, idempotent MySQL migration. The platform also creates this table
-- on startup; the script documents it for operators who manage DDL by hand.
-- Holds at most one row (id = 1): the CodeBuddy API Key a platform
-- administrator shares with users who have no key of their own, and the
-- models (a JSON array, default first) it may run.
CREATE TABLE IF NOT EXISTS platform_codebuddy_keys (
  id BIGINT NOT NULL PRIMARY KEY,
  api_key TEXT NOT NULL,
  internet_environment VARCHAR(64) NOT NULL DEFAULT '',
  models TEXT NOT NULL,
  updated_by BIGINT NOT NULL DEFAULT 0,
  updated_at DATETIME(3) NULL
);
