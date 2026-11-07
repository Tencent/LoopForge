-- Optional manual MySQL DDL for installations that pre-apply additive columns.
-- Execute once, only after checking that these columns are absent.
-- Normal repo.Open AutoMigrate adds missing columns; no data migration is needed.
ALTER TABLE runs
  ADD COLUMN workspace_editor_id BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN workspace_edit_until DATETIME(3) NULL;
