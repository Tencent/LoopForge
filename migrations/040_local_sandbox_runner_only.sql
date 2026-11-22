-- Manual, idempotent MySQL migration: local sandboxes are runner-bound and
-- only serve conversation workspaces.
--
-- A local sandbox now needs runnerId (the runner owner's own execution
-- machine) and SOPs may not use it. The default SOP sandbox is generic-docker.
--
-- This moves the platform-owned workflows off the legacy "local" row. Every
-- other workflow or sandbox that still uses an unbound local profile fails
-- with an explicit error until its owner switches the workflow to another
-- sandbox or binds the local profile to a runner; no row is rewritten for them.

UPDATE workflows
SET payload = JSON_SET(payload, '$.sandboxProfileId', 'generic-docker')
WHERE id IN ('welcome-workflow', 'scheduled-baseline-align')
  AND COALESCE(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.sandboxProfileId')), '') IN ('', 'local');
