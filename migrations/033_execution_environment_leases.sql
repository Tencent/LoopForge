-- Independent sandbox manager lease state. Apply manually before enabling the
-- SQL-backed manager. The unique environment/request keys make allocation
-- retries and concurrent manager instances serialize on the database.
CREATE TABLE execution_environment_leases (
  lease_id varchar(80) NOT NULL,
  request_id varchar(160) NOT NULL,
  environment_id varchar(255) NOT NULL,
  workspace_id varchar(255) NOT NULL,
  generation int NOT NULL,
  worker_id varchar(255) NOT NULL,
  workspace varchar(1024) NOT NULL DEFAULT '',
  host varchar(255) NOT NULL DEFAULT '',
  expires_at datetime(6) NOT NULL,
  released_at datetime(6) NULL,
  created_at datetime(6) NOT NULL,
  updated_at datetime(6) NOT NULL,
  PRIMARY KEY (lease_id),
  UNIQUE KEY uq_execution_environment_leases_environment (environment_id),
  KEY idx_execution_environment_leases_expiry (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE execution_environment_lease_requests (
  request_id varchar(160) NOT NULL,
  environment_id varchar(255) NOT NULL,
  workspace_id varchar(255) NOT NULL,
  generation int NOT NULL,
  worker_id varchar(255) NOT NULL,
  lease_id varchar(80) NOT NULL,
  created_at datetime(6) NOT NULL,
  PRIMARY KEY (request_id),
  KEY idx_execution_environment_lease_requests_lease (lease_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
