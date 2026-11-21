-- Manual, idempotent MySQL migration: widen run_artifacts.content to MEDIUMTEXT.
--
-- The engine accepts one artifact body of up to 1 MiB (maxArtifactBytes in
-- internal/engine/artifacts.go), but the column was TEXT, which holds 65,535
-- bytes. A longer deliverable -- a design document of a few thousand lines is
-- enough -- made the whole run write fail with
--   Error 1406 (22001): Data too long for column 'content'
-- so the node never reached a terminal state and the run stayed "running".
--
-- MEDIUMTEXT holds 16 MiB, covering the engine's 1 MiB cap with room for the
-- envelope around it. Fresh installations get the type from AutoMigrate.
-- Existing installations run this script once (repeatable; it is a no-op once
-- the column is MEDIUMTEXT):
--   docker compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -u loopforge loopforge' < migrations/040_run_artifacts_content_mediumtext.sql

SET @artifact_content_charset = (
  SELECT character_set_name FROM information_schema.columns
   WHERE table_schema = DATABASE() AND table_name = 'run_artifacts' AND column_name = 'content');
SET @artifact_content_collation = (
  SELECT collation_name FROM information_schema.columns
   WHERE table_schema = DATABASE() AND table_name = 'run_artifacts' AND column_name = 'content');
SET @artifact_content_nullable = (
  SELECT is_nullable FROM information_schema.columns
   WHERE table_schema = DATABASE() AND table_name = 'run_artifacts' AND column_name = 'content');
SET @artifact_content_ddl = IF(
  (SELECT COUNT(*) FROM information_schema.columns
    WHERE table_schema = DATABASE() AND table_name = 'run_artifacts' AND column_name = 'content'
      AND data_type IN ('text', 'tinytext')) > 0,
  CONCAT('ALTER TABLE run_artifacts MODIFY content MEDIUMTEXT CHARACTER SET ', @artifact_content_charset,
         ' COLLATE ', @artifact_content_collation, IF(@artifact_content_nullable = 'NO', ' NOT NULL', ' NULL')),
  'SELECT 1');
PREPARE artifact_content_stmt FROM @artifact_content_ddl;
EXECUTE artifact_content_stmt;
DEALLOCATE PREPARE artifact_content_stmt;
