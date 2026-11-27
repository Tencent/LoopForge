package repo

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The repo layer carries a few MySQL-only constructs that have no Postgres
// equivalent: INSERT IGNORE, ON DUPLICATE KEY UPDATE without a conflict
// target, GET_LOCK/RELEASE_LOCK, UTC_TIMESTAMP(3), INTERVAL ? MICROSECOND,
// CAST(payload AS CHAR), INSTR(...) and the case-insensitive collations.
//
// This test runs the rewritten statements against a real Postgres database.
// Every fixture lives in one transaction that is rolled back, so the test
// database keeps only the schema afterwards.
//
// It is skipped when AGENTFLOW_TEST_POSTGRES_URL is unset.

// postgresSQLRecorder captures the SQL the store sends to Postgres, so a test
// can assert that a rewritten statement — and not its MySQL original — really
// reached the database.
//
// The statements live behind a pointer, so a logger derived through LogMode
// keeps appending to the same list the test reads.
type postgresSQLRecorder struct {
	logger.Interface
	mu    *sync.Mutex
	stmts *[]string
}

func (l *postgresSQLRecorder) LogMode(level logger.LogLevel) logger.Interface {
	return &postgresSQLRecorder{Interface: l.Interface.LogMode(level), mu: l.mu, stmts: l.stmts}
}

func (l *postgresSQLRecorder) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	l.mu.Lock()
	*l.stmts = append(*l.stmts, sql)
	l.mu.Unlock()
}

// contains reports whether any captured statement contains fragment.
func (l *postgresSQLRecorder) contains(fragment string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, stmt := range *l.stmts {
		if strings.Contains(stmt, fragment) {
			return true
		}
	}
	return false
}

// postgresStore opens the test database and returns a store whose statements
// are recorded.
func postgresStore(t *testing.T) (*Store, *postgresSQLRecorder) {
	t.Helper()
	store := OpenPostgresTest(t)
	rec := &postgresSQLRecorder{Interface: logger.Discard, mu: &sync.Mutex{}, stmts: &[]string{}}
	return newStore(store.DB.Session(&gorm.Session{Logger: rec})), rec
}

// postgresFixtureStore opens the test database and returns a store bound to a
// transaction that is rolled back when the test ends, so the fixtures never
// outlive the test.
func postgresFixtureStore(t *testing.T) (*Store, *postgresSQLRecorder) {
	t.Helper()
	store, rec := postgresStore(t)
	tx := store.DB.Begin()
	if tx.Error != nil {
		t.Fatalf("begin transaction: %v", tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return newStore(tx), rec
}

// The recorder has to survive gorm deriving a logger from it: the derived
// logger has to keep appending to the same statement list, or every
// assertion on the recorded SQL would silently stop seeing the statements.
func TestPostgresSQLRecorderLogModeKeepsTheStatements(t *testing.T) {
	rec := &postgresSQLRecorder{Interface: logger.Discard, mu: &sync.Mutex{}, stmts: &[]string{}}
	derived := rec.LogMode(logger.Info)
	derived.Trace(context.Background(), time.Now(), func() (string, int64) {
		return "SELECT 1", 0
	}, nil)
	if !rec.contains("SELECT 1") {
		t.Fatal("the derived logger did not record its statement")
	}
}

// insertIgnore is MySQL's INSERT IGNORE: the row is skipped when it conflicts
// with any unique key, and the write still succeeds. Postgres spells that
// ON CONFLICT DO NOTHING without a conflict target.
func TestPostgresInsertIgnoreKeepsTheExistingRow(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	values := map[string]any{"id": "pg-insert-ignore", "name": "first", "payload": []byte(`{"v":1}`)}
	inserted, err := insertIgnore(store.DB, "generic_templates", values)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if !inserted {
		t.Fatal("the first insert reported no row")
	}
	values["name"] = "second"
	inserted, err = insertIgnore(store.DB, "generic_templates", values)
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if inserted {
		t.Fatal("the second insert reported a new row")
	}
	var name string
	if err := store.DB.Table("generic_templates").Select("name").Where("id = ?", "pg-insert-ignore").Scan(&name).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if name != "first" {
		t.Fatalf("insertIgnore overwrote the row: name=%q", name)
	}
	if !rec.contains("ON CONFLICT DO NOTHING") {
		t.Fatal("the insert did not use ON CONFLICT DO NOTHING")
	}
}

// A conflict on a secondary unique index has to be skipped as well: that is
// what MySQL's INSERT IGNORE does, and it is why the Postgres form carries no
// conflict target.
func TestPostgresInsertIgnoreSkipsASecondaryUniqueConflict(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	if _, err := insertIgnore(store.DB, "users", map[string]any{
		"id": 910001, "staff_id": 910001, "login_name": "pg-login-conflict", "chinese_name": "一",
	}); err != nil {
		t.Fatalf("seed the login: %v", err)
	}
	// The same login under a different primary key conflicts on the secondary
	// unique index, not on the primary key.
	inserted, err := insertIgnore(store.DB, "users", map[string]any{
		"id": 910002, "staff_id": 910002, "login_name": "pg-login-conflict", "chinese_name": "二",
	})
	if err != nil {
		t.Fatalf("the conflicting insert has to be skipped, not fail: %v", err)
	}
	if inserted {
		t.Fatal("the conflicting insert reported a new row")
	}
	var count int64
	if err := store.DB.Table("users").Where("login_name = ?", "pg-login-conflict").Count(&count).Error; err != nil {
		t.Fatalf("count the users: %v", err)
	}
	if count != 1 {
		t.Fatalf("users with that login = %d, want 1", count)
	}
	if !rec.contains("ON CONFLICT DO NOTHING") {
		t.Fatal("the insert did not use ON CONFLICT DO NOTHING")
	}
}

// upsert is MySQL's ON DUPLICATE KEY UPDATE. Postgres needs a conflict target
// for DO UPDATE, and the primary key constraint carries the table's default
// name.
func TestPostgresUpsertUpdatesTheExistingRow(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	values := map[string]any{"id": "pg-upsert", "name": "first", "payload": []byte(`{"v":1}`)}
	if err := upsert(store.DB, "generic_templates", values); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	values["name"] = "second"
	if err := upsert(store.DB, "generic_templates", values); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	var name string
	if err := store.DB.Table("generic_templates").Select("name").Where("id = ?", "pg-upsert").Scan(&name).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if name != "second" {
		t.Fatalf("upsert did not update the row: name=%q", name)
	}
	if !rec.contains("ON CONFLICT ON CONSTRAINT generic_templates_pkey DO UPDATE") {
		t.Fatal("the upsert did not use the primary key as its conflict target")
	}
}

// upsertColumns updates only the named columns on conflict.
func TestPostgresUpsertColumnsUpdatesOnlyTheGivenColumns(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	values := map[string]any{"id": "pg-upsert-columns", "name": "first", "payload": []byte(`{"v":1}`)}
	if err := upsert(store.DB, "generic_templates", values); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	values["name"] = "second"
	values["payload"] = []byte(`{"v":2}`)
	if err := upsertColumns(store.DB, "generic_templates", values, []string{"name"}); err != nil {
		t.Fatalf("upsert columns: %v", err)
	}
	var row struct {
		Name    string
		Payload []byte
	}
	if err := store.DB.Table("generic_templates").Select("name", "payload").Where("id = ?", "pg-upsert-columns").Scan(&row).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if row.Name != "second" {
		t.Fatalf("name = %q, want second", row.Name)
	}
	var payload struct {
		V int `json:"v"`
	}
	if err := jsonUnmarshal(row.Payload, &payload); err != nil {
		t.Fatalf("decode the payload: %v", err)
	}
	if payload.V != 1 {
		t.Fatalf("payload = %s, want the original (only the name was updated)", row.Payload)
	}
	if !rec.contains("ON CONFLICT ON CONSTRAINT generic_templates_pkey DO UPDATE") {
		t.Fatal("the upsert did not use the primary key as its conflict target")
	}
}

// MySQL's ON DUPLICATE KEY UPDATE treats a conflict on any unique key as a
// conflict and updates that row. Postgres' ON CONFLICT ON CONSTRAINT only
// handles the primary key, so a secondary unique index raises instead of
// silently rewriting somebody else's row.
func TestPostgresUpsertRaisesOnASecondaryUniqueConflict(t *testing.T) {
	store, _ := postgresFixtureStore(t)
	if _, err := insertIgnore(store.DB, "users", map[string]any{
		"id": 910003, "staff_id": 910003, "login_name": "pg-login-upsert", "chinese_name": "一",
	}); err != nil {
		t.Fatalf("seed the login: %v", err)
	}
	// The upsert runs in a savepoint: the failed statement aborts the
	// transaction on Postgres, and the guard has to survive it.
	err := store.DB.Transaction(func(tx *gorm.DB) error {
		return upsert(tx, "users", map[string]any{
			"id": 910004, "staff_id": 910004, "login_name": "pg-login-upsert", "chinese_name": "二",
		})
	})
	if err == nil {
		t.Fatal("the upsert silently overwrote the row that owns the login")
	}
	// The store runs with TranslateError, so the unique violation reaches the
	// caller as gorm's duplicated-key error and not as a raw driver error.
	if !errors.Is(err, gorm.ErrDuplicatedKey) {
		t.Fatalf("the upsert failed with %v, want a duplicated key error", err)
	}
	var name string
	if err := store.DB.Table("users").Select("chinese_name").Where("id = ?", 910003).Scan(&name).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if name != "一" {
		t.Fatalf("the row that owns the login was rewritten: chinese_name=%q", name)
	}
}

// LockIntegration holds a named lock on MySQL. Postgres has no named locks,
// so it takes a session-level advisory lock on a dedicated connection.
func TestPostgresIntegrationLockIsExclusiveAcrossConnections(t *testing.T) {
	store := OpenPostgresTest(t)
	ctx := t.Context()
	unlock, err := store.DeliveryRepo.LockIntegration(ctx, "pg-lock-repo", 2*time.Second)
	if err != nil {
		t.Fatalf("acquire the lock: %v", err)
	}
	defer unlock()
	// The same lock on another connection has to be refused: the advisory lock
	// is session-level, so it is exclusive across connections.
	sqlDB, err := store.DB.DB()
	if err != nil {
		t.Fatalf("sql handle: %v", err)
	}
	other, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("second connection: %v", err)
	}
	defer other.Close()
	var got bool
	if err := other.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtext($1))", integrationLockName("pg-lock-repo")).Scan(&got); err != nil {
		t.Fatalf("advisory lock probe: %v", err)
	}
	if got {
		t.Fatal("the advisory lock was not exclusive across connections")
	}
}

// A lock that cannot be taken has to time out with the same message MySQL
// produces.
func TestPostgresIntegrationLockTimesOut(t *testing.T) {
	store := OpenPostgresTest(t)
	ctx := t.Context()
	unlock, err := store.DeliveryRepo.LockIntegration(ctx, "pg-lock-timeout", 2*time.Second)
	if err != nil {
		t.Fatalf("acquire the lock: %v", err)
	}
	defer unlock()
	_, err = store.DeliveryRepo.LockIntegration(ctx, "pg-lock-timeout", 300*time.Millisecond)
	if err == nil {
		t.Fatal("the second holder did not time out")
	}
	if !strings.Contains(err.Error(), "等待仓库") {
		t.Fatalf("timeout error = %q, want the MySQL wording", err.Error())
	}
}

// The advisory lock is session-level, so it can only be released by the
// session that took it. When the caller could not read the result back, the
// lock has to be released on that very connection before it goes back to the
// pool — a pooled connection keeps its session, and the session keeps the
// lock until the connection expires.
func TestPostgresAdvisoryLockIsReleasedOnTheSameConnection(t *testing.T) {
	store := OpenPostgresTest(t)
	ctx := t.Context()
	sqlDB, err := store.DB.DB()
	if err != nil {
		t.Fatalf("sql handle: %v", err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("connection: %v", err)
	}
	defer conn.Close()
	name := integrationLockName("pg-lock-release")
	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtext($1))", name).Scan(&got); err != nil {
		t.Fatalf("take the advisory lock: %v", err)
	}
	if !got {
		t.Fatal("the advisory lock was not taken")
	}
	releasePostgresAdvisoryLock(conn, name)
	// Another session can take the lock now, so the first one really let go
	// of it instead of leaving it behind in the pool.
	other, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("second connection: %v", err)
	}
	defer other.Close()
	var otherGot bool
	if err := other.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtext($1))", name).Scan(&otherGot); err != nil {
		t.Fatalf("advisory lock probe: %v", err)
	}
	if !otherGot {
		t.Fatal("the advisory lock was not released on the connection that took it")
	}
	_, _ = other.ExecContext(ctx, "SELECT pg_advisory_unlock(hashtext($1))", name)
}

// The heartbeat age has to be measured on the clock that stamped the
// heartbeat, which is the database's. Postgres' now() is frozen when the
// transaction starts and clock_timestamp() is not, so two reads in one
// transaction have to differ.
func TestPostgresHeartbeatClockIsTheDatabaseClock(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	first, ok := store.AssistantRepo.heartbeatNow(store.DB)
	if !ok {
		t.Fatal("the database clock could not be read")
	}
	time.Sleep(1100 * time.Millisecond)
	second, ok := store.AssistantRepo.heartbeatNow(store.DB)
	if !ok {
		t.Fatal("the database clock could not be read")
	}
	if !second.After(first) {
		t.Fatalf("the heartbeat clock is frozen inside the transaction: %s then %s", first, second)
	}
	if !rec.contains("clock_timestamp()") {
		t.Fatal("the heartbeat clock was not read from the database")
	}
	// The clock has to come from the database: a closed connection cannot
	// answer it, while this host's clock would still be there.
	closed := OpenPostgresTest(t)
	sqlDB, err := closed.DB.DB()
	if err != nil {
		t.Fatalf("sql handle: %v", err)
	}
	_ = sqlDB.Close()
	if _, ok := closed.AssistantRepo.heartbeatNow(closed.DB); ok {
		t.Fatal("the heartbeat clock was answered without the database")
	}
}

// The generation clock stamps the heartbeat of a running turn, and the lease
// renewal clock stamps the renewal of a workspace lease. Both have to be the
// database's wall clock.
func TestPostgresClocksAreTheDatabaseWallClock(t *testing.T) {
	store, rec := postgresFixtureStore(t)

	// The generation clock.
	g := domain.AssistantGeneration{ID: "pg-generation", SessionID: "pg-generation-session", UserID: 7, Status: "running"}
	if err := store.AssistantRepo.BeginAssistantGeneration(g); err != nil {
		t.Fatalf("begin the generation: %v", err)
	}
	firstGeneration := postgresGenerationUpdatedAt(t, store, "pg-generation-session")
	time.Sleep(1100 * time.Millisecond)
	if err := store.AssistantRepo.HeartbeatAssistantGeneration(g); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	secondGeneration := postgresGenerationUpdatedAt(t, store, "pg-generation-session")
	if !secondGeneration.After(firstGeneration) {
		t.Fatalf("the generation clock is frozen inside the transaction: %s then %s", firstGeneration, secondGeneration)
	}

	// The lease renewal clock.
	if err := store.DB.Exec("INSERT INTO runs (id) VALUES ('pg-lease-run')").Error; err != nil {
		t.Fatalf("insert the run: %v", err)
	}
	token, err := store.RunsRepo.BeginWorkspaceUse("pg-lease-run", time.Now().UTC())
	if err != nil {
		t.Fatalf("take the workspace lease: %v", err)
	}
	firstLease := postgresLeaseRenewedAt(t, store, "pg-lease-run")
	time.Sleep(1100 * time.Millisecond)
	if err := store.RunsRepo.RenewWorkspaceUse("pg-lease-run", token, time.Now().UTC()); err != nil {
		t.Fatalf("renew the workspace lease: %v", err)
	}
	secondLease := postgresLeaseRenewedAt(t, store, "pg-lease-run")
	if !secondLease.After(firstLease) {
		t.Fatalf("the lease renewal clock is frozen inside the transaction: %s then %s", firstLease, secondLease)
	}
	if !rec.contains("clock_timestamp()") {
		t.Fatal("the clocks were not read from the database")
	}
}

// lateHeartbeatOnly lets the database confirm that a heartbeat is late. The
// Postgres branch measures the age on the database's clock.
func TestPostgresLateHeartbeatFilterUsesTheDatabaseClock(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	if err := store.DB.Exec("INSERT INTO assistant_generations (session_id, user_id, generation_id, status, payload, updated_at) VALUES (?, 1, ?, 'running', ?::jsonb, clock_timestamp() - INTERVAL '120 seconds')",
		"pg-late-session", "pg-late-generation", "{}").Error; err != nil {
		t.Fatalf("insert the late turn: %v", err)
	}
	if err := store.DB.Exec("INSERT INTO assistant_generations (session_id, user_id, generation_id, status, payload, updated_at) VALUES (?, 1, ?, 'running', ?::jsonb, clock_timestamp())",
		"pg-fresh-session", "pg-fresh-generation", "{}").Error; err != nil {
		t.Fatalf("insert the fresh turn: %v", err)
	}
	var rows []struct{ SessionID string }
	if err := store.AssistantRepo.lateHeartbeatOnly(store.DB.Table("assistant_generations").Select("session_id")).Find(&rows).Error; err != nil {
		t.Fatalf("apply the late heartbeat filter: %v", err)
	}
	if len(rows) != 1 || rows[0].SessionID != "pg-late-session" {
		t.Fatalf("the late heartbeat filter matched %+v, want only the late turn", rows)
	}
	if !rec.contains("INTERVAL '1 microsecond'") {
		t.Fatal("the late heartbeat filter did not measure the age on the database clock")
	}
}

// The renewal age of a workspace lease is measured on the clock that stamped
// the renewal, which is the database's.
func TestPostgresAbandonedWorkspaceUseIsMeasuredOnTheDatabaseClock(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	now := time.Now().UTC()
	if err := store.DB.Exec("INSERT INTO runs (id) VALUES ('pg-abandoned-run'), ('pg-renewed-run')").Error; err != nil {
		t.Fatalf("insert the runs: %v", err)
	}
	// A lease whose holder stopped renewing two minutes ago.
	if err := store.DB.Exec("INSERT INTO run_workspace_checkpoints (run_id, last_used_at, lease_token, lease_until, lease_renewed_at) VALUES ('pg-abandoned-run', now(), 'pg-token', now() + INTERVAL '10 minutes', clock_timestamp() - INTERVAL '120 seconds')").Error; err != nil {
		t.Fatalf("insert the abandoned lease: %v", err)
	}
	// A lease that was renewed just now: its holder is alive.
	if err := store.DB.Exec("INSERT INTO run_workspace_checkpoints (run_id, last_used_at, lease_token, lease_until, lease_renewed_at) VALUES ('pg-renewed-run', now(), 'pg-token', now() + INTERVAL '10 minutes', clock_timestamp())").Error; err != nil {
		t.Fatalf("insert the renewed lease: %v", err)
	}
	released, err := store.RunsRepo.ReleaseAbandonedWorkspaceUse("pg-abandoned-run", "pg-token", now)
	if err != nil {
		t.Fatalf("release the abandoned use: %v", err)
	}
	if !released {
		t.Fatal("the abandoned workspace use was not released")
	}
	released, err = store.RunsRepo.ReleaseAbandonedWorkspaceUse("pg-renewed-run", "pg-token", now)
	if err != nil {
		t.Fatalf("release the renewed use: %v", err)
	}
	if released {
		t.Fatal("a renewed workspace use was released")
	}
	if !rec.contains("INTERVAL '1 microsecond'") {
		t.Fatal("the renewal age was not measured on the database clock")
	}
}

// The evolution asset write is guarded by an optimistic-concurrency check on
// the stored payload. Postgres' CAST(x AS CHAR) is bpchar(1) — only the first
// character — so the guard has to compare the jsonb documents themselves.
func TestPostgresEvolutionAssetGuardComparesTheJSONDocument(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	if err := store.DB.Exec("INSERT INTO agents (id, name, payload, updated_at) VALUES ('pg-agent', '实现工程师', ?::jsonb, now())",
		`{"id":"pg-agent","name":"实现工程师","version":1,"prompt":"old"}`).Error; err != nil {
		t.Fatalf("insert the agent: %v", err)
	}
	// The same document with a different key order and whitespace. jsonb
	// compares the documents, so this is the same baseline.
	baseline := []byte("{\n  \"prompt\": \"old\",\n  \"version\": 1,\n  \"name\": \"实现工程师\",\n  \"id\": \"pg-agent\"\n}")
	if err := store.CatalogRepo.writeEvolutionAsset(store.DB, domain.EvoAssetAgent, "pg-agent", "new prompt", baseline); err != nil {
		t.Fatalf("write the asset: %v", err)
	}
	var row struct {
		Payload []byte
	}
	if err := store.DB.Table("agents").Select("payload").Where("id = ?", "pg-agent").Scan(&row).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	var asset domain.AgentDefinition
	if err := jsonUnmarshal(row.Payload, &asset); err != nil {
		t.Fatalf("decode the payload: %v", err)
	}
	if asset.Prompt != "new prompt" {
		t.Fatalf("prompt = %q, want the new prompt", asset.Prompt)
	}
	if !rec.contains("::jsonb") {
		t.Fatal("the guard did not compare the jsonb document")
	}
}

// A stale baseline has to be refused: the guard compares the document, so a
// different document does not match.
func TestPostgresEvolutionAssetGuardRefusesAStaleBaseline(t *testing.T) {
	store, _ := postgresFixtureStore(t)
	if err := store.DB.Exec("INSERT INTO agents (id, name, payload, updated_at) VALUES ('pg-stale-agent', '实现工程师', ?::jsonb, now())",
		`{"id":"pg-stale-agent","name":"实现工程师","version":1,"prompt":"old"}`).Error; err != nil {
		t.Fatalf("insert the agent: %v", err)
	}
	stale := []byte(`{"id":"pg-stale-agent","name":"实现工程师","version":1,"prompt":"something else"}`)
	if err := store.CatalogRepo.writeEvolutionAsset(store.DB, domain.EvoAssetAgent, "pg-stale-agent", "new prompt", stale); err != ErrEvoStale {
		t.Fatalf("write the asset = %v, want ErrEvoStale", err)
	}
}

// The room message that carries an attachment is found by searching the
// payload text. Postgres' CAST(payload AS CHAR) is bpchar(1), so the search
// has to run on the jsonb text — and MySQL's LIKE is case-insensitive, so
// Postgres needs ILIKE.
func TestPostgresRoomAttachmentLookupFindsTheAttachment(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	if err := store.DB.Exec("INSERT INTO chat_messages (id, room_id, user_id, kind, payload, created_at) VALUES ('pg-room-message', 'pg-room', 1, 'text', ?::jsonb, now())",
		`{"id":"pg-room-message","roomId":"pg-room","attachments":[{"id":"att-pg-1","name":"a.txt"}]}`).Error; err != nil {
		t.Fatalf("insert the message: %v", err)
	}
	message, ok := store.RoomsRepo.MessageWithAttachment("pg-room", "att-pg-1")
	if !ok {
		t.Fatal("the message that carries the attachment was not found")
	}
	if message.ID != "pg-room-message" {
		t.Fatalf("message = %q, want the one that carries the attachment", message.ID)
	}
	if _, ok := store.RoomsRepo.MessageWithAttachment("pg-room", "att-pg-missing"); ok {
		t.Fatal("a missing attachment was found")
	}
	if !rec.contains("payload::text ILIKE") {
		t.Fatal("the attachment lookup did not search the jsonb text case-insensitively")
	}
}

// The three payload searches share one expression. MySQL's INSTR is
// case-insensitive under the utf8mb4_unicode_ci collation, so the needle and
// the payload are both lowercased on Postgres.
func TestPostgresPayloadContainsFindsTheFailureText(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	if err := store.DB.Exec("INSERT INTO assistant_generations (session_id, user_id, generation_id, status, payload, updated_at) VALUES ('pg-notice-session', 1, 'pg-notice-generation', 'failed', ?::jsonb, clock_timestamp())",
		`{"error":"the CLI is not installed"}`).Error; err != nil {
		t.Fatalf("insert the generation: %v", err)
	}
	// The needle is in a different case than the payload: MySQL's collation
	// makes INSTR case-insensitive, so Postgres has to be too.
	var rows []struct{ SessionID string }
	query := wherePayloadHas(store.DB.Table("assistant_generations").Select("session_id"), store.DB, "the cli IS not installed", "")
	if err := query.Find(&rows).Error; err != nil {
		t.Fatalf("search the payload: %v", err)
	}
	if len(rows) != 1 || rows[0].SessionID != "pg-notice-session" {
		t.Fatalf("the payload search matched %+v, want the generation that keeps the text", rows)
	}
	rows = nil
	query = wherePayloadHas(store.DB.Table("assistant_generations").Select("session_id"), store.DB, "not the needle", "")
	if err := query.Find(&rows).Error; err != nil {
		t.Fatalf("search the payload: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("the payload search matched %+v, want nothing", rows)
	}
	if !rec.contains("strpos(lower(payload::text), lower(") {
		t.Fatal("the payload search did not use the jsonb text")
	}
}

// The production shape of wherePayloadHas passes the engine-specific sentence
// and the generic one, so the query carries two needles and the OR of both
// expressions. Every payload that keeps either sentence has to come back —
// the same rows MySQL's branch returns.
func TestPostgresPayloadSearchMatchesBothNeedles(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	fixtures := []struct {
		sessionID string
		payload   string
	}{
		{"pg-two-missing", `{"error":"the CLI is not installed"}`},
		{"pg-two-generic", `{"error":"the engine could not be started"}`},
		{"pg-two-other", `{"error":"something else entirely"}`},
	}
	for _, fixture := range fixtures {
		if err := store.DB.Exec("INSERT INTO assistant_generations (session_id, user_id, generation_id, status, payload, updated_at) VALUES (?, 1, ?, 'failed', ?::jsonb, clock_timestamp())",
			fixture.sessionID, fixture.sessionID, fixture.payload).Error; err != nil {
			t.Fatalf("insert the generation %s: %v", fixture.sessionID, err)
		}
	}
	// The needles are spelled in a different case than the payloads: MySQL's
	// collation makes INSTR case-insensitive, so Postgres has to be too.
	missing := "the cli is not installed"
	generic := "the ENGINE could not be started"
	var found []struct{ SessionID string }
	query := wherePayloadHas(store.DB.Table("assistant_generations").Select("session_id"), store.DB, missing, generic)
	if err := query.Find(&found).Error; err != nil {
		t.Fatalf("search the payload: %v", err)
	}
	matched := map[string]bool{}
	for _, row := range found {
		matched[row.SessionID] = true
	}
	if len(found) != 2 || !matched["pg-two-missing"] || !matched["pg-two-generic"] {
		t.Fatalf("the payload search matched %+v, want the two generations that keep one of the two sentences", found)
	}
	if matched["pg-two-other"] {
		t.Fatal("the generation that keeps neither sentence was matched")
	}
	// Both needles have to be bound, and in order: the engine-specific
	// sentence first, the generic one second. gorm's Explain renders the
	// bound values into the statement, so the recorded SQL shows which
	// needle went into which side of the OR.
	if !rec.contains("strpos(lower(payload::text), lower('the cli is not installed')) > 0 OR strpos(lower(payload::text), lower('the ENGINE could not be started')) > 0") {
		t.Fatal("the payload search did not bind both needles")
	}
}

// payloadMentions reports which attachment ids appear in a payload.
func TestPostgresPayloadMentionsFindsTheAttachmentID(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	if err := store.DB.Exec("INSERT INTO chat_messages (id, room_id, user_id, kind, payload, created_at) VALUES ('pg-mention-message', 'pg-mention-room', 1, 'text', ?::jsonb, now())",
		`{"id":"pg-mention-message","roomId":"pg-mention-room","attachments":[{"id":"att-pg-2","name":"b.txt"}]}`).Error; err != nil {
		t.Fatalf("insert the message: %v", err)
	}
	found, err := payloadMentions(store.DB, "chat_messages", []string{"att-pg-2", "att-pg-missing"})
	if err != nil {
		t.Fatalf("scan the payloads: %v", err)
	}
	if !found["att-pg-2"] {
		t.Fatal("the attachment id in the payload was not found")
	}
	if found["att-pg-missing"] {
		t.Fatal("an attachment id that is not in the payload was found")
	}
	if !rec.contains("strpos(lower(payload::text), lower(") {
		t.Fatal("the payload scan did not use the jsonb text")
	}
}

// The execution dashboard narrows its candidates by the payload text before
// the fields decide.
func TestPostgresScheduleExecutionSearchFindsTheErrorText(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	if err := store.DB.Exec("INSERT INTO schedule_job_executions (id, job_id, status, payload, started_at) VALUES ('pg-execution', 'pg-job', 'failed', ?::jsonb, now())",
		`{"id":"pg-execution","jobId":"pg-job","jobName":"nightly","status":"failed","error":"the runner TIMED OUT","startedAt":"2026-01-01T00:00:00Z"}`).Error; err != nil {
		t.Fatalf("insert the execution: %v", err)
	}
	items, total := store.CatalogRepo.ScheduleJobExecutionPage(ScheduleJobExecutionPageQuery{
		JobIDs: []string{"pg-job"}, Search: "timed out", Limit: 10,
	})
	if total != 1 || len(items) != 1 {
		t.Fatalf("the search returned %d rows (total %d), want the execution that failed", len(items), total)
	}
	items, total = store.CatalogRepo.ScheduleJobExecutionPage(ScheduleJobExecutionPageQuery{
		JobIDs: []string{"pg-job"}, Search: "not the needle", Limit: 10,
	})
	if total != 0 || len(items) != 0 {
		t.Fatalf("the search returned %d rows (total %d), want nothing", len(items), total)
	}
	if !rec.contains("strpos(lower(payload::text), lower(") {
		t.Fatal("the execution search did not use the jsonb text")
	}
}

// postgresGenerationUpdatedAt reads the heartbeat of a generation.
func postgresGenerationUpdatedAt(t *testing.T, store *Store, sessionID string) time.Time {
	t.Helper()
	var stamp sql.NullTime
	if err := store.DB.Table("assistant_generations").Select("updated_at").Where("session_id = ?", sessionID).Row().Scan(&stamp); err != nil {
		t.Fatalf("read the heartbeat: %v", err)
	}
	if !stamp.Valid {
		t.Fatalf("the generation %q has no heartbeat", sessionID)
	}
	return stamp.Time
}

// postgresLeaseRenewedAt reads the renewal stamp of a workspace lease.
func postgresLeaseRenewedAt(t *testing.T, store *Store, runID string) time.Time {
	t.Helper()
	var stamp sql.NullTime
	if err := store.DB.Table("run_workspace_checkpoints").Select("lease_renewed_at").Where("run_id = ?", runID).Row().Scan(&stamp); err != nil {
		t.Fatalf("read the lease renewal: %v", err)
	}
	if !stamp.Valid {
		t.Fatalf("the workspace lease %q has no renewal stamp", runID)
	}
	return stamp.Time
}
