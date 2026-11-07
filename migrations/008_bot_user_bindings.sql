-- Optional additive DDL. GORM startup adds these tables automatically.
-- No existing user or Bot is bound implicitly and no seed/data migration runs.
CREATE TABLE IF NOT EXISTS bot_user_bindings (
 id varchar(64) PRIMARY KEY, bot_id varchar(191), sender_id varchar(191),
 user_id bigint, created_at datetime(3), updated_at datetime(3),
 KEY idx_bot_user_bindings_user_id(user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS bot_binding_challenges (
 id varchar(64) PRIMARY KEY, binding_id varchar(64), user_id bigint,
 code_hash varchar(64), status varchar(16), attempts bigint,
 expires_at datetime(3), created_at datetime(3),
 KEY idx_bot_binding_challenges_binding_id(binding_id),
 KEY idx_bot_binding_challenges_expires_at(expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
