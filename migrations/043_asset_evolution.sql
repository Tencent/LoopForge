-- 043 asset evolution (进化中心 P0 + P1).
-- Optional manual additive DDL: startup AutoMigrate creates the same tables.
-- No data migration and no seed. Existing runs keep no asset pins; new runs
-- pin role prompts / rule bodies at start. Existing agents and rules get a
-- baseline version the first time a proposal touches them.

-- Autonomy level per asset: 0 off, 1 report only, 2 propose + replay (default).
CREATE TABLE IF NOT EXISTS evo_policies (
  asset_kind VARCHAR(16) NOT NULL,
  asset_id VARCHAR(128) NOT NULL,
  project_id VARCHAR(64),
  level BIGINT,
  payload JSON,
  updated_at DATETIME(3) NULL,
  PRIMARY KEY (asset_kind, asset_id),
  INDEX idx_evo_policies_project (project_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS evo_findings (
  id VARCHAR(64) NOT NULL PRIMARY KEY,
  project_id VARCHAR(64),
  status VARCHAR(32),
  asset_kind VARCHAR(16),
  asset_id VARCHAR(128),
  payload JSON,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  INDEX idx_evo_findings_project_status (project_id, status),
  INDEX idx_evo_findings_asset (asset_kind, asset_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS evo_proposals (
  id VARCHAR(64) NOT NULL PRIMARY KEY,
  project_id VARCHAR(64),
  status VARCHAR(32),
  asset_kind VARCHAR(16),
  asset_id VARCHAR(128),
  payload JSON,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  INDEX idx_evo_proposals_project (project_id),
  INDEX idx_evo_proposals_status (status),
  INDEX idx_evo_proposals_asset (asset_kind, asset_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- One historical run replayed with a candidate; payload carries the overrides.
CREATE TABLE IF NOT EXISTS evo_replays (
  id VARCHAR(64) NOT NULL PRIMARY KEY,
  proposal_id VARCHAR(64),
  run_id VARCHAR(64),
  status VARCHAR(32),
  payload JSON,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  INDEX idx_evo_replays_proposal (proposal_id),
  INDEX idx_evo_replays_run (run_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Immutable bodies of role prompts and rules. Rows are never updated.
CREATE TABLE IF NOT EXISTS asset_versions (
  asset_kind VARCHAR(16) NOT NULL,
  asset_id VARCHAR(128) NOT NULL,
  version BIGINT NOT NULL,
  hash VARCHAR(32),
  source VARCHAR(32),
  proposal_id VARCHAR(64),
  payload JSON,
  created_at DATETIME(3) NULL,
  PRIMARY KEY (asset_kind, asset_id, version)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Which asset bodies each run started with.
CREATE TABLE IF NOT EXISTS run_asset_pins (
  run_id VARCHAR(64) NOT NULL,
  asset_kind VARCHAR(16) NOT NULL,
  asset_id VARCHAR(128) NOT NULL,
  version BIGINT,
  hash VARCHAR(32),
  created_at DATETIME(3) NULL,
  PRIMARY KEY (run_id, asset_kind, asset_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Explicit user feedback on runs (thumbs down with a reason).
CREATE TABLE IF NOT EXISTS evo_signals (
  id VARCHAR(64) NOT NULL PRIMARY KEY,
  project_id VARCHAR(64),
  run_id VARCHAR(64),
  kind VARCHAR(32),
  payload JSON,
  created_at DATETIME(3) NULL,
  INDEX idx_evo_signals_project_created (project_id, created_at),
  INDEX idx_evo_signals_run (run_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Runs a human pinned as regression cases every proposal must keep passing.
CREATE TABLE IF NOT EXISTS evo_samples (
  project_id VARCHAR(64) NOT NULL,
  run_id VARCHAR(64) NOT NULL,
  payload JSON,
  created_at DATETIME(3) NULL,
  PRIMARY KEY (project_id, run_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
