-- Optional manual DDL; repo.Open AutoMigrate also adds this table.
-- One durable snapshot and idle-use lease per run; no baseline data migration.
CREATE TABLE IF NOT EXISTS run_workspace_checkpoints (
 run_id VARCHAR(64) NOT NULL PRIMARY KEY,
 archive MEDIUMBLOB,
 sha256 VARCHAR(64),
 last_used_at DATETIME(3),
 saved_at DATETIME(3),
 follow_up BOOLEAN NOT NULL DEFAULT FALSE,
 error TEXT,
 lease_token VARCHAR(64),
 lease_until DATETIME(3)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
