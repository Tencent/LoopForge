-- 001: run 观测表的 Postgres 生成列基线
--
-- 对应 MySQL 的 migrations/001_run_obs_generated_columns.sql。
--
-- 背景：run_telemetry / run_logs 的事件体整体存 JSON payload，但运行详情的
-- 遥测筛选（internal/repo/runs.go adapterSQLFilter）与分析 coverage 聚合
-- （internal/repo/analytics.go queryAnalyticsCoverage）在 Postgres 上直接引用
-- 生成列 adapter / level。历史上由启动时自动执行的 migrations/schema.sql
-- 创建；2026-09 整改（迁移移出启动路径）后基线需手动执行，故落成此脚本。
--
-- 说明：
-- - 仅 Postgres 需要；sqlite/内存测试走代码内 JSON 解析兜底。
-- - Postgres 的 ALTER TABLE ... ADD COLUMN 不支持 IF NOT EXISTS，所以用
--   DO $$ ... $$ 块做幂等；CREATE INDEX IF NOT EXISTS 支持，可放在块外。
-- - 生成列的表达式必须是 immutable 的：payload::jsonb #>> '{...}' 是 immutable
--   的，在 GENERATED ALWAYS AS (...) STORED 里合法。
-- - STORED 生成列在大表上新增会触发表重建，请由运维选择窗口执行。
-- - gorm AutoMigrate 不感知本脚本：已建库后再执行本脚本即可，AutoMigrate
--   不会删除已存在的生成列，也不会去 ALTER 它们依赖的 payload 列的类型。
-- - 本脚本必须在 run_telemetry / run_logs 建好之后执行（平台启动时由
--   gorm AutoMigrate 建表）。

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = current_schema()
       AND table_name = 'run_telemetry'
       AND column_name = 'adapter'
  ) THEN
    ALTER TABLE run_telemetry
      ADD COLUMN adapter VARCHAR(255)
        GENERATED ALWAYS AS (COALESCE(NULLIF(payload::jsonb #>> '{source,adapter}', ''), 'unknown')) STORED;
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_run_telemetry_run_adapter ON run_telemetry (run_id, adapter);

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = current_schema()
       AND table_name = 'run_logs'
       AND column_name = 'level'
  ) THEN
    ALTER TABLE run_logs
      ADD COLUMN level VARCHAR(32)
        GENERATED ALWAYS AS (COALESCE(NULLIF(payload::jsonb #>> '{level}', ''), 'info')) STORED;
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_run_logs_run_level ON run_logs (run_id, level);
