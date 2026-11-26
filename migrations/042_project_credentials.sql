CREATE TABLE IF NOT EXISTS project_credentials (
    id VARCHAR(64) NOT NULL,
    project_id VARCHAR(64) NOT NULL,
    name VARCHAR(80) NOT NULL,
    kind VARCHAR(32) NOT NULL,
    variables JSON NOT NULL,
    ciphertext MEDIUMBLOB NOT NULL,
    disabled BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_project_credential_name (project_id, name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
