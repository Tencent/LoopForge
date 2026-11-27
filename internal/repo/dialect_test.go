package repo

import (
	"strings"
	"testing"
)

func TestDSNDialect(t *testing.T) {
	cases := map[string]string{
		"postgres://postgres.cesar:pw@127.0.0.1:5432/loopforge?sslmode=disable": "postgres",
		"postgresql://postgres.cesar:pw@127.0.0.1:5432/loopforge":               "postgres",
		"loopforge:pw@tcp(127.0.0.1:33315)/loopforge":                           "mysql",
		"  postgres://u:p@h:5432/d  ":                                           "postgres",
	}
	for dsn, want := range cases {
		if got := dsnDialect(dsn); got != want {
			t.Fatalf("dsnDialect(%q) = %q, want %q", dsn, got, want)
		}
	}
}

func TestPostgresDSNNormalizes(t *testing.T) {
	got := postgresDSN("postgres://postgres.cesar:pw@127.0.0.1:5432/loopforge")
	if want := "sslmode=disable"; !strings.Contains(got, want) {
		t.Fatalf("postgresDSN missing %q: %s", want, got)
	}
	again := postgresDSN(got)
	if again != got {
		t.Fatalf("postgresDSN is not idempotent: %s -> %s", got, again)
	}
	kept := postgresDSN("postgres://u:p@h:5432/d?sslmode=require")
	if !strings.Contains(kept, "sslmode=require") {
		t.Fatalf("postgresDSN overwrote caller params: %s", kept)
	}
}

func TestPostgresDSNStripsPoolParams(t *testing.T) {
	// pgx forwards every unknown setting to the server as a startup
	// parameter, and Postgres rejects pgxpool-only pool parameters with
	// "unrecognized configuration parameter". The pool size is enforced by
	// database/sql instead.
	got := postgresDSN("postgres://u:p@127.0.0.1:5432/d?pool_max_conns=4&pool_min_conns=1&application_name=loopforge")
	if strings.Contains(got, "pool_") {
		t.Fatalf("postgresDSN kept pgxpool-only params: %s", got)
	}
	if want := "application_name=loopforge"; !strings.Contains(got, want) {
		t.Fatalf("postgresDSN dropped an unrelated param: %s", got)
	}
}

func TestPostgresDSNKeepsTLSSForRemoteHosts(t *testing.T) {
	// Only loopback hosts default to a plaintext connection; a remote
	// database keeps pgx's own TLS default.
	got := postgresDSN("postgres://u:p@db.example.com:5432/d")
	if strings.Contains(got, "sslmode") {
		t.Fatalf("postgresDSN disabled TLS for a remote host: %s", got)
	}
}
