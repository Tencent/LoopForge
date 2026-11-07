-- Manual additive migration for Bot-initiated SOP handoff and delivery acceptance.
-- No jobs are created or authorized by this script.
ALTER TABLE sop_handoff_jobs
  ADD COLUMN complete BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN reason TEXT,
  ADD COLUMN source_session_id VARCHAR(64),
  ADD COLUMN source_generation_id VARCHAR(64),
  ADD COLUMN source_message_index BIGINT,
  ADD COLUMN source_history_revision BIGINT;
