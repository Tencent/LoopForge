-- Trusted Git SSH server public keys managed by platform administrators.
CREATE TABLE IF NOT EXISTS platform_git_ssh_hosts (
  host VARCHAR(260) NOT NULL PRIMARY KEY,
  public_keys TEXT NOT NULL,
  revision VARCHAR(64) NOT NULL
);
