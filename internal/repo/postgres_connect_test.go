package repo

import (
	"os"
	"strings"
	"testing"
)

// TestPostgresConnectOpens is the smoke gate for the Postgres connection
// path: it opens a real database through the same code the platform uses and
// checks the dialect, the connection pool cap and the schema AutoMigrate
// produced. It is skipped when AGENTFLOW_TEST_POSTGRES_URL is unset.
func TestPostgresConnectOpens(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("AGENTFLOW_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_POSTGRES_URL is not set")
	}
	store, err := Open(dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if got := store.DB.Dialector.Name(); got != dialectPostgres {
		t.Fatalf("expected the postgres dialect, got %q", got)
	}
	sqlDB, err := store.DB.DB()
	if err != nil {
		t.Fatalf("sql handle: %v", err)
	}
	if got := sqlDB.Stats().MaxOpenConnections; got != 10 {
		t.Fatalf("connection pool cap = %d, want 10", got)
	}
	var tables int64
	if err := store.DB.Raw("SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema()").Scan(&tables).Error; err != nil {
		t.Fatalf("count tables: %v", err)
	}
	if tables == 0 {
		t.Fatal("AutoMigrate created no tables")
	}
}
