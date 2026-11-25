-- One personal Git SSH key per host and port. Private keys are never shared
-- through repository configuration or returned by the credential API.
CREATE TABLE IF NOT EXISTS user_git_ssh_keys (
  user_id BIGINT NOT NULL,
  id VARCHAR(64) NOT NULL,
  name VARCHAR(80) NOT NULL,
  host TEXT NOT NULL,
  private_key TEXT NOT NULL,
  PRIMARY KEY (user_id, id)
);
