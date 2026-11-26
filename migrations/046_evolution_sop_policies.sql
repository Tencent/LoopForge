-- Per-SOP RSI evolution policy (enable toggle, multi-select optimization preferences,
-- configurable batch trigger size, and optional analyzer model override).
CREATE TABLE IF NOT EXISTS evo_sop_policies (
    project_id  VARCHAR(64)  NOT NULL,
    workflow_id VARCHAR(128) NOT NULL,
    payload     JSON,
    updated_at  DATETIME(3),
    PRIMARY KEY (project_id, workflow_id),
    INDEX idx_evo_sop_policies_project (project_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
