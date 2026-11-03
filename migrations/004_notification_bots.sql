-- Manual deployment migration: project Bots, plaintext user-owned credentials and durable notifications.
-- Apply on MySQL before enabling Bot notifications. Contains no seed/data rewrite.
CREATE TABLE IF NOT EXISTS notification_bots (
 id varchar(191) NOT NULL,
 project_id varchar(191) NOT NULL,
 payload json NOT NULL,
 PRIMARY KEY (id), KEY idx_notification_bots_project_id (project_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS user_bot_credentials (
 bot_id varchar(191) NOT NULL,
 ciphertext text NOT NULL,
 PRIMARY KEY (bot_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS notification_deliveries (
 id varchar(64) NOT NULL,
 project_id varchar(191) NOT NULL,
 bot_id varchar(191) NOT NULL,
 run_id varchar(191) DEFAULT NULL,
 node_id varchar(191) DEFAULT NULL,
 event_type varchar(64) NOT NULL,
 target_id text NOT NULL,
 target_name varchar(255) NOT NULL,
 content text NOT NULL,
 status varchar(40) NOT NULL,
 attempts bigint NOT NULL DEFAULT 0,
 attempt bigint NOT NULL DEFAULT 0,
 rework_count bigint NOT NULL DEFAULT 0,
 next_attempt_at datetime(3) NOT NULL,
 lease_until datetime(3) DEFAULT NULL,
 lease_token varchar(64) DEFAULT NULL,
 last_error varchar(255) DEFAULT NULL,
 created_at datetime(3) NOT NULL,
 sent_at datetime(3) DEFAULT NULL,
 PRIMARY KEY (id),
 KEY idx_notification_deliveries_project_id (project_id),
 KEY idx_notification_deliveries_bot_id (bot_id),
 KEY idx_notification_due (status,next_attempt_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
