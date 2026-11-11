package engine

import (
	"context"
	"fmt"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge/engine/qdrant"
)

// blockingEmbedProvider lets the test observe the middle of a multi-batch
// embedding run instead of only the final result.
type blockingEmbedProvider struct {
	*fakeEmbedProvider
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (b *blockingEmbedProvider) EmbedBatch(ctx context.Context, texts []string, isQuery bool) ([][]float32, error) {
	b.calls++
	if b.calls == 2 {
		close(b.entered)
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return b.fakeEmbedProvider.EmbedBatch(ctx, texts, isQuery)
}

func TestSyncSourceReportsVectorRebuildProgress(t *testing.T) {
	cfg := testVectorConfig()
	control := newVectorFakeControl()
	control.sources["progress-src"] = domain.KnowledgeSource{ID: "progress-src", BaseID: "progress-base", Kind: "hosted"}
	control.bases["progress-base"] = domain.KnowledgeBase{
		ID: "progress-base", VectorEnabled: true, QdrantCollection: cfg.Collection,
	}
	content := newVectorFakeContent()
	chunks := make([]domain.KnowledgeChunk, 0, 40)
	for i := 0; i < 40; i++ {
		chunks = append(chunks, domain.KnowledgeChunk{
			ID: int64(i + 1), SourceID: "progress-src", Path: "docs/a.md", Idx: i,
			Title: "标题", Text: fmt.Sprintf("第 %d 块", i), Commit: "1",
		})
	}
	content.chunksBySource["progress-src"] = chunks
	ep := &blockingEmbedProvider{fakeEmbedProvider: &fakeEmbedProvider{dim: cfg.Dim}, entered: make(chan struct{}), release: make(chan struct{})}
	rt := newTestVectorRuntime(cfg, content, control, &deindexQdrant{points: map[uint64]qdrant.Point{}}, ep)
	done := make(chan error, 1)
	go func() { done <- rt.SyncSource(context.Background(), "progress-src") }()
	select {
	case <-ep.entered:
	case err := <-done:
		t.Fatalf("sync finished before the second batch: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("sync did not reach the second embedding batch")
	}
	p, ok := rt.progressFor("progress-src")
	if !ok || p.Total != 40 || p.Current != 32 || p.Phase != "rebuild" {
		t.Fatalf("mid-sync progress = %+v ok=%v", p, ok)
	}
	close(ep.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, ok := rt.progressFor("progress-src"); ok {
		t.Fatal("progress should be cleared after the rebuild")
	}
}
