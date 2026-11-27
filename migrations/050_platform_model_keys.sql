-- 050: 平台共享模型凭据按 CLI 分开存放（platform_model_keys）
--
-- 说明：
-- - 平台管理 → 共享模型 Key 从「只支持 CodeBuddy」扩展为按 CLI（codebuddy /
--   codex / claude-code）各存一份 Key、Base URL（CodeBuddy 为网络环境）与允许模型。
-- - 新表由 gorm 增量迁移（autoMigrateIncremental）补建；本脚本负责把旧表
--   platform_codebuddy_keys 里的那一行搬到新表的 codebuddy 行，供不依赖代码回退的
--   部署使用。代码在 platform_model_keys 没有 codebuddy 行时仍会读取旧表，因此
--   本脚本可择机执行，执行后旧表保留不动（代码不再写入它）。
-- - 旧表在页面上「停用共享 Key」时才会被清空。

CREATE TABLE IF NOT EXISTS platform_model_keys (
  provider             VARCHAR(32) NOT NULL,
  api_key              TEXT NOT NULL,
  base_url             TEXT NOT NULL,
  internet_environment VARCHAR(64) NOT NULL DEFAULT '',
  models               TEXT NOT NULL,
  updated_by           BIGINT NOT NULL DEFAULT 0,
  updated_at           DATETIME(3) NULL,
  PRIMARY KEY (provider)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

INSERT INTO platform_model_keys (provider, api_key, base_url, internet_environment, models, updated_by, updated_at)
SELECT 'codebuddy', api_key, '', internet_environment, models, updated_by, updated_at
FROM platform_codebuddy_keys
WHERE id = 1 AND TRIM(api_key) <> ''
  AND NOT EXISTS (SELECT 1 FROM platform_model_keys WHERE provider = 'codebuddy');
