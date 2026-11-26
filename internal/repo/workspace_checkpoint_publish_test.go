package repo

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

func TestWorkspaceCheckpointPublishCommitError(t *testing.T) {
	store, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	checkpointPublishCommitError(t, store)
}

func TestMySQLWorkspaceCheckpointPublishCommitError(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("isolated Docker MySQL required")
	}
	store, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	checkpointPublishCommitError(t, store)
}

func checkpointPublishCommitError(t *testing.T, store *Store) {
	t.Helper()
	db, err := store.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	for _, committed := range []bool{true, false} {
		t.Run(fmt.Sprintf("committed=%t", committed), func(t *testing.T) {
			runID := fmt.Sprintf("checkpoint-commit-%d", time.Now().UnixNano())
			if err := store.PutRun(domain.Run{ID: runID, Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := store.DeleteRun(runID); err != nil {
					t.Error(err)
				}
			})
			now := time.Now().UTC()
			token, err := store.BeginWorkspaceUse(runID, now)
			if err != nil {
				t.Fatal(err)
			}
			oldArchive, newArchive := []byte("old checkpoint"), []byte("new checkpoint")
			if err := store.SaveWorkspaceCheckpoint(runID, token, now, oldArchive); err != nil {
				t.Fatal(err)
			}

			pool := &checkpointCommitFaultPool{DB: db, committed: committed, inject: true}
			oldStatementPool, oldConfigPool := store.DB.Statement.ConnPool, store.DB.Config.ConnPool
			store.DB.Statement.ConnPool, store.DB.Config.ConnPool = pool, pool
			defer func() {
				store.DB.Statement.ConnPool, store.DB.Config.ConnPool = oldStatementPool, oldConfigPool
			}()
			if err := store.SaveWorkspaceCheckpointStream(runID, token, now, bytes.NewReader(newArchive)); !errors.Is(err, errCheckpointCommitReply) || pool.inject {
				t.Fatalf("publish commit fault did not fire: %v", err)
			}
			want := oldArchive
			if committed {
				want = newArchive
			}
			if got, err := store.WorkspaceCheckpointArchive(runID); err != nil || !bytes.Equal(got, want) {
				t.Fatalf("readable checkpoint after failed commit reply: got %q, want %q, error %v", got, want, err)
			}
			if err := store.SaveWorkspaceCheckpointStream(runID, token, now, bytes.NewReader(newArchive)); err != nil {
				t.Fatal(err)
			}
			var chunks int64
			if err := store.DB.Model(&workspaceCheckpointChunk{}).Where("run_id = ?", runID).Count(&chunks).Error; err != nil || chunks != 1 {
				t.Fatalf("retry should retain only the published chunk: count %d, error %v", chunks, err)
			}
		})
	}
}

var errCheckpointCommitReply = errors.New("workspace checkpoint commit reply lost")

type checkpointCommitFaultPool struct {
	*sql.DB
	committed bool
	inject    bool
}

func (p *checkpointCommitFaultPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &checkpointCommitFaultTx{Tx: tx, pool: p}, nil
}

type checkpointCommitFaultTx struct {
	*sql.Tx
	pool    *checkpointCommitFaultPool
	publish bool
}

func (tx *checkpointCommitFaultTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := tx.Tx.ExecContext(ctx, query, args...)
	if err == nil && strings.HasPrefix(query, "UPDATE") && strings.Contains(query, "run_workspace_checkpoints") && strings.Contains(query, "archive_id") {
		tx.publish = true
	}
	return result, err
}

func (tx *checkpointCommitFaultTx) Commit() error {
	if !tx.publish || !tx.pool.inject {
		return tx.Tx.Commit()
	}
	tx.pool.inject = false
	finish := tx.Tx.Rollback
	if tx.pool.committed {
		finish = tx.Tx.Commit
	}
	if err := finish(); err != nil {
		return err
	}
	return errCheckpointCommitReply
}
