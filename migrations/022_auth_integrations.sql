CREATE TABLE IF NOT EXISTS auth_integrations (
  provider VARCHAR(32) NOT NULL PRIMARY KEY,
  client_id VARCHAR(80) NOT NULL
);
