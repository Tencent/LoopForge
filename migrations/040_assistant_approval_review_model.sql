-- Optional manual additive DDL. Startup AutoMigrate also adds these columns.
-- Stores optional "帮我批准" AI review configuration:
--   - approval_review_mode: "follow" (default), "cli", or "api"
--   - approval_review_model: "<cli>:<modelID>" when mode is "cli"
--   - approval_review_api_json: standalone HTTP API config (protocol, baseUrl, model, apiKey) when mode is "api"

ALTER TABLE assistant_preferences
  ADD COLUMN approval_review_mode VARCHAR(32) NOT NULL DEFAULT '',
  ADD COLUMN approval_review_model VARCHAR(160) NOT NULL DEFAULT '',
  ADD COLUMN approval_review_api_json TEXT NULL;
