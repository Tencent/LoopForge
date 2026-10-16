-- Optional additive DDL; startup incremental GORM migration adds these columns.
ALTER TABLE notification_deliveries
 ADD COLUMN report_request text,
 ADD COLUMN report_state varchar(32),
 ADD COLUMN report_model varchar(191),
 ADD COLUMN report_error varchar(255);
