package repo

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
// dedicated connection, so the lock also ends if the holder dies. The
// returned function releases it.
func (r *DeliveryRepo) LockIntegration(ctx context.Context, repositoryID string, wait time.Duration) (func(), error) {
	name := integrationLockName(repositoryID)
	if !r.isMySQL() {
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
	sum := sha256.Sum256([]byte(id))
	return "agentflow:integrate:" + hex.EncodeToString(sum[:])[:40]
}
