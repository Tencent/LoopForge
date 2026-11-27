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
		"  postgres://u:p@h:5432/d  ":                                            "postgres",
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
	if want := "pool_max_conns=10"; !strings.Contains(got, want) {
		t.Fatalf("postgresDSN missing %q: %s", want, got)
	}
	again := postgresDSN(got)
	if again != got {
		t.Fatalf("postgresDSN is not idempotent: %s -> %s", got, again)
	}
	kept := postgresDSN("postgres://u:p@h:5432/d?sslmode=require&pool_max_conns=4")
	if !strings.Contains(kept, "sslmode=require") || !strings.Contains(kept, "pool_max_conns=4") {
		t.Fatalf("postgresDSN overwrote caller params: %s", kept)
	}
}
