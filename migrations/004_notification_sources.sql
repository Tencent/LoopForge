-- Manual migration for flexible templates and scheduled-AI notification audit metadata.
-- Node/assistant/schedule configuration remains in existing JSON payloads.
ALTER TABLE notification_deliveries
 ADD COLUMN source_type varchar(32) NOT NULL DEFAULT 'run',
 ADD COLUMN source_id varchar(191) NOT NULL DEFAULT '',
 ADD COLUMN source_name varchar(255) NOT NULL DEFAULT '',
 ADD COLUMN content_source varchar(32) DEFAULT NULL,
 ADD INDEX idx_notification_source (source_type,source_id);
