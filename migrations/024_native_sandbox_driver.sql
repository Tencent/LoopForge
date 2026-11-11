-- Manual, idempotent MySQL migration: merge the seatbelt / bwrap sandbox
-- drivers into the single "native" option.
--
-- native is the OS lightweight process sandbox. The executing machine picks
-- seatbelt on macOS and bwrap on Linux (domain.ResolveSandboxDriver), so a
-- profile no longer has to match the runner's OS.
--
-- Safe to run before or after deploying: the new binary still executes rows
-- that keep seatbelt / bwrap, and stores native whenever such a profile is
-- saved again. Running this only makes the stored value and the UI agree.
-- Profile ids are unchanged, so workflows referencing them need no update.

UPDATE sandboxes
SET payload = JSON_SET(payload, '$.driver', 'native')
WHERE JSON_UNQUOTE(JSON_EXTRACT(payload, '$.driver')) IN ('seatbelt', 'bwrap');
