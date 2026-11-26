-- Widen legacy artifact storage to accommodate the 1 MiB artifact contract.
-- The Compose startup runs this automatically. Run before starting the new
-- platform in other deployment pipelines. Safe to repeat.
SET @loopforge_artifact_ddl = (
  SELECT CONCAT(
    'ALTER TABLE `run_artifacts` MODIFY COLUMN `content` MEDIUMTEXT',
    ' CHARACTER SET ', CHARACTER_SET_NAME, ' COLLATE ', COLLATION_NAME,
    IF(IS_NULLABLE = 'YES', ' NULL', ' NOT NULL')
  )
  FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'run_artifacts'
    AND COLUMN_NAME = 'content'
    AND DATA_TYPE = 'text'
);
SET @loopforge_artifact_ddl = COALESCE(@loopforge_artifact_ddl, 'DO 0');
PREPARE loopforge_artifact_stmt FROM @loopforge_artifact_ddl;
EXECUTE loopforge_artifact_stmt;
DEALLOCATE PREPARE loopforge_artifact_stmt;
