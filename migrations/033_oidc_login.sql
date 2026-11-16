CREATE TABLE IF NOT EXISTS oidc_identities (
    issuer VARBINARY(512) NOT NULL,
    subject VARBINARY(255) NOT NULL,
    user_id BIGINT NOT NULL,
    PRIMARY KEY (issuer, subject),
    KEY idx_oidc_identities_user (user_id)
);

CREATE TABLE IF NOT EXISTS oidc_logins (
    state VARCHAR(64) NOT NULL PRIMARY KEY,
    browser VARCHAR(64) NOT NULL,
    nonce VARCHAR(64) NOT NULL,
    verifier VARCHAR(64) NOT NULL,
    return_path TEXT NOT NULL,
    expires_at DATETIME(3) NOT NULL,
    KEY idx_oidc_logins_expires (expires_at)
);
