-- Optional additive DDL. Production startup adds missing tables/columns/indexes
-- through GORM; do not repeat the ALTER if startup has already added the column.
ALTER TABLE notification_deliveries ADD COLUMN target_type varchar(16);
CREATE TABLE IF NOT EXISTS bot_connections (
 bot_id varchar(191) PRIMARY KEY, owner varchar(64), lease_until datetime(3),
 state varchar(32), last_error varchar(255), updated_at datetime(3)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS bot_conversations (
 id varchar(64) PRIMARY KEY, bot_id varchar(191), chat_id varchar(191), chat_type varchar(16),
 session_id varchar(64), updated_at datetime(3), KEY idx_bot_conversations_bot_id(bot_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS bot_interactions (
 id varchar(64) PRIMARY KEY, bot_id varchar(191), conversation_id varchar(64),
 session_id varchar(64), sender_id varchar(191), request_id varchar(191),
 content text, quote text, action varchar(32), delivery_id varchar(64), card_task_id varchar(128),
 status varchar(32), reply text, last_error varchar(255), created_at datetime(3), updated_at datetime(3),
 KEY idx_bot_queue(bot_id,status,created_at), KEY idx_bot_interactions_conversation_id(conversation_id),
 KEY idx_bot_interactions_session_id(session_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
