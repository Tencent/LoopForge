-- Control plane. Run manually on the main database. Safe to repeat.
-- Ciphertext is AES-256-GCM, bound to source ID, using a purpose-specific key
-- derived from AGENTFLOW_SESSION_SECRET (SESSION_SECRET fallback).
SET @ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE()
  AND table_name = 'knowledge_sources' AND column_name = 'credential_ciphertext') = 0,
 'ALTER TABLE knowledge_sources ADD COLUMN credential_ciphertext TEXT NULL',
 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
