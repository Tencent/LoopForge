-- Personal Forgejo API tokens are scoped to an exact HTTPS instance.
CREATE TABLE IF NOT EXISTS user_forge_tokens (
  user_id BIGINT NOT NULL,
  id VARCHAR(64) NOT NULL,
  instance VARCHAR(255) NOT NULL,
  token TEXT NOT NULL,
  PRIMARY KEY (user_id, id)
);
