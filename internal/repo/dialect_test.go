package repo

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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

func TestJSONPathToPGArray(t *testing.T) {
	cases := map[string]string{
		"$.name":        "{name}",
		"$.a.b":         "{a,b}",
		"$.messages[0]": "{messages,0}",
		"$.a[1].b":      "{a,1,b}",
		"$":             "{}",
	}
	for path, want := range cases {
		if got := jsonPathToPGArray(path); got != want {
			t.Fatalf("jsonPathToPGArray(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestDialectExpressions(t *testing.T) {
	// The helpers only read the dialect name, so a handle without a live
	// database is enough to pin every branch's SQL.
	mysqlDeps := dialectOnlyDeps(t, dialectMySQL)
	postgresDeps := dialectOnlyDeps(t, dialectPostgres)
	sqliteDeps := dialectOnlyDeps(t, dialectSQLite)

	cases := []struct {
		name     string
		expr     func(*deps) string
		mysql    string
		postgres string
		sqlite   string
	}{
		{
			name:     "jsonTextLiteral",
			expr:     func(d *deps) string { return d.jsonTextLiteral("payload", "$.a.b") },
			mysql:    "JSON_UNQUOTE(JSON_EXTRACT(payload, '$.a.b'))",
			postgres: "payload::jsonb #>> '{a,b}'",
			sqlite:   "json_extract(payload, '$.a.b')",
		},
		{
			name:     "jsonTextParam",
			expr:     func(d *deps) string { return d.jsonTextParam("payload") },
			mysql:    "JSON_UNQUOTE(JSON_EXTRACT(payload, ?))",
			postgres: "payload::jsonb #>> string_to_array(ltrim(?, '$.'), '.')",
			sqlite:   "json_extract(payload, ?)",
		},
		{
			name:     "jsonBuildObject",
			expr:     func(d *deps) string { return d.jsonBuildObject("'k'", "v") },
			mysql:    "JSON_OBJECT('k', v)",
			postgres: "jsonb_build_object('k', v)",
			sqlite:   "json_object('k', v)",
		},
		{
			name:     "likeOperator",
			expr:     func(d *deps) string { return d.likeOperator() },
			mysql:    "LIKE",
			postgres: "ILIKE",
			sqlite:   "LIKE",
		},
		{
			name:     "nowMillisLiteral",
			expr:     func(d *deps) string { return d.nowMillisLiteral() },
			mysql:    "UTC_TIMESTAMP(3)",
			postgres: "clock_timestamp()",
			sqlite:   "UTC_TIMESTAMP(3)",
		},
	}
	for _, c := range cases {
		if got := c.expr(mysqlDeps); got != c.mysql {
			t.Errorf("%s(mysql) = %q, want %q", c.name, got, c.mysql)
		}
		if got := c.expr(postgresDeps); got != c.postgres {
			t.Errorf("%s(postgres) = %q, want %q", c.name, got, c.postgres)
		}
		if got := c.expr(sqliteDeps); got != c.sqlite {
			t.Errorf("%s(sqlite) = %q, want %q", c.name, got, c.sqlite)
		}
	}
}

// TestDialectLengthAndTypeExpressions pins the parts of the two long
// Postgres expressions that a typo would hide: the jsonb functions and their
// lowercase type names.
func TestDialectLengthAndTypeExpressions(t *testing.T) {
	postgresDeps := dialectOnlyDeps(t, dialectPostgres)

	length := postgresDeps.jsonLengthLiteral("payload", "$.messages")
	for _, want := range []string{"jsonb_typeof(", "jsonb_array_length(", "jsonb_object_keys(", "COALESCE("} {
		if !strings.Contains(length, want) {
			t.Errorf("jsonLengthLiteral is missing %q: %s", want, length)
		}
	}
	if strings.Contains(length, "JSON_LENGTH") {
		t.Errorf("jsonLengthLiteral kept the MySQL function: %s", length)
	}

	kind := postgresDeps.jsonTypeLiteral("payload", "$.a")
	for _, want := range []string{"jsonb_typeof(", "'string'", "'number'", "'INTEGER'", "'DOUBLE'", "'OBJECT'", "'ARRAY'"} {
		if !strings.Contains(kind, want) {
			t.Errorf("jsonTypeLiteral is missing %q: %s", want, kind)
		}
	}
	if strings.Contains(kind, "JSON_TYPE") {
		t.Errorf("jsonTypeLiteral kept the MySQL function: %s", kind)
	}
}

// dialectOnlyDeps returns deps whose dialect is the named one, without a live
// database: the expression helpers only read the dialect name.
func dialectOnlyDeps(t *testing.T, name string) *deps {
	t.Helper()
	var dialector gorm.Dialector
	switch name {
	case dialectMySQL:
		dialector = mysql.New(mysql.Config{DSN: "u:p@tcp(127.0.0.1:1)/d", SkipInitializeWithVersion: true})
	case dialectPostgres:
		dialector = postgres.New(postgres.Config{DSN: "postgres://u:p@127.0.0.1:1/d"})
	default:
		dialector = sqlite.Open("file:agentflow_dialect_expr_test?mode=memory&cache=shared")
	}
	db, err := gorm.Open(dialector, &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open %s dialect: %v", name, err)
	}
	return &deps{DB: db}
}

func TestPostgresDSNStripsPoolParamsFromUnparsableDSN(t *testing.T) {
	// A DSN the URL parser rejects cannot connect anyway, but a pool_*
	// parameter must still not reach the server as a startup parameter.
	got := postgresDSN("postgres://u:p%zz@127.0.0.1:5432/d?pool_max_conns=4&application_name=loopforge")
	if strings.Contains(got, "pool_") {
		t.Fatalf("postgresDSN kept pgxpool-only params: %s", got)
	}
	if want := "application_name=loopforge"; !strings.Contains(got, want) {
		t.Fatalf("postgresDSN dropped an unrelated param: %s", got)
	}
}
