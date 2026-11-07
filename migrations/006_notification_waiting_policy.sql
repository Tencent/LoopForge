-- Manual migration: freeze the waiting notification policy per delivery.
ALTER TABLE notification_deliveries
 ADD COLUMN waiting_policy varchar(32) NOT NULL DEFAULT 'actionable';
