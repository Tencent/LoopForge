-- Keep polling with the personal Forgejo token used for this delivery.
ALTER TABLE mr_delivery_states ADD COLUMN forge_user_id BIGINT NOT NULL DEFAULT 0;
