package repo

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"
)

// localIntegrationLocks backs LockIntegration on sqlite, which only ever
// serves one process.
var localIntegrationLocks sync.Map

// LockIntegration serializes merges into a repository's integration branch
// across every control-plane instance. On MySQL it holds a named lock on a
// dedicated connection, so the lock also ends if the holder dies. Postgres
// holds a session-level advisory lock on a dedicated connection for the same
// reason. The returned function releases it.
func (r *DeliveryRepo) LockIntegration(ctx context.Context, repositoryID string, wait time.Duration) (func(), error) {
	name := integrationLockName(repositoryID)
	if !r.isMySQL() && !r.isPostgres() {
		// sqlite only ever serves one process, so a local mutex is enough.
		m, _ := localIntegrationLocks.LoadOrStore(name, &sync.Mutex{})
		mu := m.(*sync.Mutex)
		mu.Lock()
		return mu.Unlock, nil
	}
	db, err := r.DB.DB()
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if r.isPostgres() {
		// pg_try_advisory_lock is session-level, so it has to be taken and
		// released on the same connection — conn is a dedicated one. The lock
		// name is hashed by Postgres itself; the hash has no stability promise
		// across versions, so Go does not reimplement it.
		deadline := time.Now().Add(wait)
		for {
			var got bool
			// The statement runs through database/sql, so the placeholder is
			// Postgres' own $1 — gorm's ? rewriting does not apply here.
			if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtext($1))", name).Scan(&got); err != nil {
				_ = conn.Close()
				return nil, err
			}
			if got {
				break
			}
			if !time.Now().Before(deadline) {
				_ = conn.Close()
				return nil, fmt.Errorf("等待仓库 %s 的合入锁超时（%s）", repositoryID, wait)
			}
			time.Sleep(50 * time.Millisecond)
		}
		return func() {
			_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock(hashtext($1))", name)
			_ = conn.Close()
		}, nil
	}
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", name, int(wait/time.Second)).Scan(&got); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !got.Valid || got.Int64 != 1 {
		_ = conn.Close()
		return nil, fmt.Errorf("等待仓库 %s 的合入锁超时（%s）", repositoryID, wait)
	}
	return func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", name)
		_ = conn.Close()
	}, nil
}

// integrationLockName fits MySQL's 64-character lock name limit.
func integrationLockName(repositoryID string) string {
	id := strings.TrimSpace(repositoryID)
	if id == "" {
		id = "_"
	}
	name := "agentflow:integrate:" + id
	if len(name) <= 64 {
		return name
	}
	return "agentflow:integrate:" + hexSHA256(id)[:40]
}
