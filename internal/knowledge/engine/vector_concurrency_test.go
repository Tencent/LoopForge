package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	"agentflow/internal/knowledge/engine/qdrant"
	"agentflow/internal/repo"
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

type blockedRebuildQdrant struct {
	*fakeQdrantStore
	entered, release chan struct{}
	deletes          atomic.Int32
}

func (q *blockedRebuildQdrant) DeleteCollection(ctx context.Context, name string) error {
	if q.deletes.Add(1) == 1 {
		close(q.entered)
		<-q.release
	}
	return nil
}
func TestMultipleWorkersClaimVectorRebuild(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MySQL DSN not set")
	}
	st, err := repo.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, mode := range []string{"manual", "startup"} {
		t.Run(mode, func(t *testing.T) {
			cfg := testVectorConfig()
			q := &blockedRebuildQdrant{fakeQdrantStore: &fakeQdrantStore{}, entered: make(chan struct{}), release: make(chan struct{})}
			one := &VectorRuntime{cfg: cfg, control: st, content: newVectorFakeContent(), qdrant: q, embed: &fakeEmbedProvider{dim: cfg.Dim}}
			two := &VectorRuntime{cfg: cfg, control: st, content: newVectorFakeContent(), qdrant: q, embed: &fakeEmbedProvider{dim: cfg.Dim}}
			done := make(chan error, 1)
			go func() {
				if mode == "startup" {
					done <- one.Reconcile(context.Background())
				} else {
					done <- one.Rebuild(context.Background(), nil)
				}
			}()
			<-q.entered
			second := make(chan error, 1)
			go func() {
				if mode == "startup" {
					second <- two.Reconcile(context.Background())
				} else {
					second <- two.Rebuild(context.Background(), nil)
				}
			}()
			select {
			case <-second:
			case <-time.After(time.Second):
				t.Error("second rebuild did not return")
			}
			if q.deletes.Load() != 1 {
				t.Error("workers concurrently deleted the vector collection")
			}
			close(q.release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}

}

func TestDeleteSourceDeindexesAllVectors(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MySQL DSN not set")
	}
	st, err := repo.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := knowledge.NewService(st, nil, nil)
	base, err := svc.EnsureHostedBase("concurrency-deindex")
	if err != nil {
		t.Fatal(err)
	}
	src := domain.KnowledgeSource{ID: "concurrency-deindex-source", BaseID: base.ID, Kind: "git", Status: "ready"}
	if err := st.PutKnowledgeSource(src); err != nil {
		t.Fatal(err)
	}
	defer st.DeleteKnowledgeSource(src.ID)
	defer st.DeleteKnowledgeSourceContent(src.ID)
	e := NewEngine(st, st, nil)
	cfg := testVectorConfig()
	q := &deindexQdrant{points: map[uint64]qdrant.Point{}}
	q.points[1] = qdrant.Point{ID: 1, Payload: map[string]any{"source_id": src.ID, "path": "a.md"}}
	q.points[2] = qdrant.Point{ID: 2, Payload: map[string]any{"source_id": src.ID, "path": "b.md"}}
	rt := &VectorRuntime{cfg: cfg, qdrant: q}
	e.SetVectorDeindexer(rt.DeindexDocument)
	svc = knowledge.NewService(st, nil, e)
	if err := svc.DeleteSource("concurrency-deindex", src.ID); err != nil {
		t.Fatal(err)
	}
	if len(q.points) != 0 {
		t.Fatalf("orphaned vectors=%d", len(q.points))
	}
	if _, found := st.KnowledgeSource(src.ID); found {
		t.Fatal("source still exists")
	}
	if err := st.PutKnowledgeSource(src); err != nil {
		t.Fatal(err)
	}
	if len(q.points) != 0 {
		t.Fatal("reregistered source exposed old vectors")
	}
}

type observedRebuildControl struct {
	*repo.Store
	busy chan struct{}
	done chan struct{}
}

func (c *observedRebuildControl) ClaimKnowledgeOperation(id, owner string, ttl time.Duration) (bool, error) {
	ok, err := c.Store.ClaimKnowledgeOperation(id, owner, ttl)
	if !ok {
		select {
		case c.busy <- struct{}{}:
		default:
		}
	}
	return ok, err
}
func (c *observedRebuildControl) ReleaseKnowledgeOperation(id, owner string) error {
	err := c.Store.ReleaseKnowledgeOperation(id, owner)
	if c.done != nil {
		select {
		case c.done <- struct{}{}:
		default:
		}
	}
	return err
}
func TestWorkerRetriesRebuildAfterCrashedOwnerExpires(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MySQL DSN not set")
	}
	st, err := repo.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := testVectorConfig()
	id := "vector-" + sha256Hex(cfg.QdrantURL+"/"+cfg.Collection)
	if ok, err := st.ClaimKnowledgeOperation(id, "crashed", time.Minute); err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	defer st.ReleaseKnowledgeOperation(id, "crashed")
	q := &blockedRebuildQdrant{fakeQdrantStore: &fakeQdrantStore{}, entered: make(chan struct{}), release: make(chan struct{})}
	close(q.release)
	control := &observedRebuildControl{Store: st, busy: make(chan struct{}, 1), done: make(chan struct{}, 1)}
	rt := &VectorRuntime{cfg: cfg, control: control, content: newVectorFakeContent(), qdrant: q, embed: &fakeEmbedProvider{dim: cfg.Dim}}
	restore := setGlobalVectorForTest(rt)
	defer restore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := NewSyncWorker(NewEngine(st, st, nil), &syncFakeControl{}, st, t.TempDir(), "replacement")
	w.StartSyncLoop(ctx, true)
	select {
	case <-control.busy:
	case <-time.After(time.Second):
		t.Fatal("replacement did not attempt initial reconciliation")
	}
	if err := st.DB.Exec("UPDATE knowledge_operation_leases SET lease_until=TIMESTAMPADD(SECOND,-1,UTC_TIMESTAMP(6)) WHERE id=?", id).Error; err != nil {
		t.Fatal(err)
	}
	select {
	case <-q.entered:
	case <-time.After(6 * time.Second):
		t.Error("replacement never retried after lease expiry")
	}
	cancel()
	select {
	case <-control.done:
	case <-time.After(time.Second):
		t.Error("rebuild did not release lease")
	}
}
func TestReconcileResumesPersistedPartialRebuild(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MySQL DSN not set")
	}
	st, err := repo.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := testVectorConfig()
	id := "vector-" + sha256Hex(cfg.QdrantURL+"/"+cfg.Collection)
	state, ok := any(st).(interface {
		SetKnowledgeOperationDirty(string, string, bool) error
	})
	if !ok {
		t.Fatal("partial rebuild has no persistent recovery state")
	}
	if claimed, err := st.ClaimKnowledgeOperation(id, "crashed", time.Minute); err != nil || !claimed {
		t.Fatal(err)
	}
	if err := state.SetKnowledgeOperationDirty(id, "crashed", true); err != nil {
		t.Fatal(err)
	}
	st.ReleaseKnowledgeOperation(id, "crashed")
	q := &fakeQdrantStore{info: qdrant.CollectionInfo{Exists: true, Dim: cfg.Dim}}
	rt := &VectorRuntime{cfg: cfg, control: st, content: newVectorFakeContent(), qdrant: q, embed: &fakeEmbedProvider{dim: cfg.Dim}}
	if err := rt.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !contains(q.ops(), "delete-collection") {
		t.Fatal("partially populated collection was accepted as complete")
	}
}
