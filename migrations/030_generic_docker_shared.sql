-- Manual, idempotent MySQL migration: mark the built-in generic-docker sandbox
-- as shared.
--
-- The public catalog seed stored it without the shared flag, so conversation
-- workspaces (which fall back to generic-docker) and sandbox lists filtered by
-- project could not see it. The application already treats generic-docker as
-- shared when reading; running this only makes the stored value and the UI
-- agree.

UPDATE sandboxes
SET payload = JSON_SET(payload, '$.shared', true)
WHERE id = 'generic-docker'
  AND COALESCE(JSON_EXTRACT(payload, '$.shared'), false) <> true;
