-- Optional manual additive DDL. No seed or historical auto-authorization.
CREATE TABLE IF NOT EXISTS assistant_group_followups (
 run_id VARCHAR(64) NOT NULL PRIMARY KEY,
 session_id VARCHAR(64) NOT NULL,
 project_id VARCHAR(128) NOT NULL,
 owner_id BIGINT NOT NULL,
 actor_id BIGINT NOT NULL,
 model VARCHAR(256),
 human_generation_id VARCHAR(64) NOT NULL,
 status VARCHAR(24) NOT NULL,
 handled JSON,
 rounds INT NOT NULL DEFAULT 0,
 active_generation_id VARCHAR(64),
 last_error TEXT,
 next_check_at DATETIME(3),
 lease_token VARCHAR(64),
 lease_until DATETIME(3),
 updated_at DATETIME(3),
 INDEX idx_group_followup_due (status, next_check_at)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
