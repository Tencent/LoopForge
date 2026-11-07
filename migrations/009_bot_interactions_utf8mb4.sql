-- MANUAL migration for existing MySQL utf8/utf8mb3 Bot message tables.
-- Startup only creates missing objects; it will not convert an existing table.
-- Preserves all rows. ALTER may rebuild/lock the table; execute during a quiet window.
ALTER TABLE bot_interactions CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
