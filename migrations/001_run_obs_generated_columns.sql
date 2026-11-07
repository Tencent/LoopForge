-- 001: run 观测表的 MySQL 生成列基线
--
-- 背景：run_telemetry / run_logs 的事件体整体存 JSON payload，但运行详情的
-- 遥测筛选（internal/repo/runs.go adapterSQLFilter）与分析 coverage 聚合
-- （internal/repo/analytics.go queryAnalyticsCoverage）在 MySQL 上直接引用
-- 生成列 adapter / level。历史上由启动时自动执行的 migrations/schema.sql
-- 创建；2026-09 整改（迁移移出启动路径）后基线需手动执行，故落成此脚本。
--
-- 说明：
-- - 仅 MySQL 需要；sqlite/内存测试走代码内 JSON 解析兜底（isMySQL() 分支）。
-- - STORED 生成列在大表上新增会触发表重建，请由运维选择窗口执行。
-- - MySQL 8.0 不支持 ADD COLUMN IF NOT EXISTS，本脚本不可重复执行；
--   可先查 information_schema.columns 确认列不存在再执行。
-- - gorm AutoMigrate 不感知本脚本：已建库后再执行本脚本即可，AutoMigrate
--   不会删除已存在的生成列。

ALTER TABLE run_telemetry
  ADD COLUMN adapter VARCHAR(255)
    GENERATED ALWAYS AS (COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.source.adapter')), ''), 'unknown')) STORED,
  ADD KEY idx_run_telemetry_run_adapter (run_id, adapter);

ALTER TABLE run_logs
  ADD COLUMN level VARCHAR(32)
    GENERATED ALWAYS AS (COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(payload, '$.level')), ''), 'info')) STORED,
  ADD KEY idx_run_logs_run_level (run_id, level);
