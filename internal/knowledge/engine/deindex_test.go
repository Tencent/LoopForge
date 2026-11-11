package engine

import (
	"context"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge/engine/qdrant"
)

// deindexContent 同时满足 ContentStore / VectorContentStore / 撤回删除接口，
// 让 WriteDocument 写入的正文与块、以及 RetractDocument 的删除都在同一份
// 内存状态上可见。
type deindexContent struct {
	*fakeContent
}

func (d *deindexContent) KnowledgeChunks(sourceID, path string) []domain.KnowledgeChunk {
	return d.fakeContent.chunks[docKey(sourceID, path)]
}

func (d *deindexContent) KnowledgeChunksBySource(sourceID string) []domain.KnowledgeChunk {
	out := []domain.KnowledgeChunk{}
	for k, cs := range d.fakeContent.chunks {
		if strings.HasPrefix(k, sourceID+"\x00") {
			out = append(out, cs...)
		}
	}
	return out
}

func (d *deindexContent) KnowledgeChunksForSources(ids []string) []domain.KnowledgeChunk {
	out := []domain.KnowledgeChunk{}
	for _, id := range ids {
		out = append(out, d.KnowledgeChunksBySource(id)...)
	}
	return out
}

func (d *deindexContent) DeleteKnowledgeDocument(sourceID, path string) error {
	delete(d.fakeContent.docs, docKey(sourceID, path))
	delete(d.fakeContent.chunks, docKey(sourceID, path))
	return nil
}

// deindexQdrant 是有状态的假向量库：Upsert 存点、DeleteByFilter 按
// source+path 删点、Search 只返回仍存在的点。用它验证撤回后向量点真的消失。
type deindexQdrant struct {
	points  map[uint64]qdrant.Point
	deletes []string
}

func (q *deindexQdrant) EnsureCollection(context.Context, string, int) error { return nil }
func (q *deindexQdrant) DeleteCollection(context.Context, string) error      { return nil }
func (q *deindexQdrant) CollectionInfo(context.Context, string) (qdrant.CollectionInfo, error) {
	return qdrant.CollectionInfo{Exists: true, Dim: 1024}, nil
}
func (q *deindexQdrant) Upsert(_ context.Context, _ string, points []qdrant.Point) error {
	for _, p := range points {
		q.points[p.ID] = p
	}
	return nil
}
func (q *deindexQdrant) DeleteByFilter(_ context.Context, _, sourceID, path string) error {
	q.deletes = append(q.deletes, sourceID+"|"+path)
	for id, p := range q.points {
		if payloadString(p.Payload, "source_id") != sourceID {
			continue
		}
		if path != "" && payloadString(p.Payload, "path") != path {
			continue
		}
		delete(q.points, id)
	}
	return nil
}
func (q *deindexQdrant) Search(_ context.Context, _ string, _ []float32, sources []string, _ int) ([]qdrant.ScoredPoint, error) {
	allowed := map[string]bool{}
	for _, s := range sources {
		allowed[s] = true
	}
	out := []qdrant.ScoredPoint{}
	for id, p := range q.points {
		if len(sources) > 0 && !allowed[payloadString(p.Payload, "source_id")] {
			continue
		}
		out = append(out, qdrant.ScoredPoint{ID: id, Score: 0.9, Payload: p.Payload})
	}
	return out, nil
}

// docKeywordIndex 像 MySQL 全文索引一样从内容面块取候选：文档被撤回删除后
// 关键词通道自然也不再命中它。
type docKeywordIndex struct{ content *deindexContent }

func (k *docKeywordIndex) Search(_ context.Context, _ string, sources []string, _ SearchOptions) ([]Hit, error) {
	out := []Hit{}
	for _, src := range sources {
		for _, d := range k.content.KnowledgeDocuments(src) {
			out = append(out, Hit{
				Source: src, Commit: d.Commit, Path: d.Path, Title: d.Title,
				StartLine: 1, EndLine: d.LineCount, Text: d.Body, Score: 5,
			})
		}
	}
	return out, nil
}

// TestRetractRemovesVectorsAndHybridStopsHitting 验证父线程要求的接线：
// 向量启用时撤回文档，SetVectorDeindexer 装上的回调会调用已有 Qdrant 删除
// 接口去掉向量点，撤回后混合检索不再命中该文档。
func TestRetractRemovesVectorsAndHybridStopsHitting(t *testing.T) {
	cfg := testVectorConfig()
	control := newVectorFakeControl()
	control.sources["src1"] = domain.KnowledgeSource{ID: "src1", BaseID: "base1", Kind: "hosted", Name: "托管"}
	control.bases["base1"] = domain.KnowledgeBase{
		ID: "base1", VectorEnabled: true, QdrantCollection: cfg.Collection,
	}
	content := &deindexContent{fakeContent: newFakeContent()}
	q := &deindexQdrant{points: map[uint64]qdrant.Point{}}
	rt := newTestVectorRuntime(cfg, content, control, q, &fakeEmbedProvider{dim: cfg.Dim})
	restore := setGlobalVectorForTest(rt)
	defer restore()

	// 用 NewEngine（走真实装配：融合层 + 全局向量运行时 + 默认去索引钩子）。
	e := NewEngine(control, content, &docKeywordIndex{content: content})

	ctx := context.Background()
	if _, err := e.WriteDocument(ctx, "src1", "guides/a.md", "# 部署\n\n青鸾新羽麒麟服务器配置"); err != nil {
		t.Fatal(err)
	}
	if len(q.points) == 0 {
		t.Fatal("写入后应有向量点（增量索引未生效）")
	}
	hitsBefore := searchPaths(t, e, ctx, "青鸾新羽麒麟", []string{"src1"})
	if len(hitsBefore) == 0 || hitsBefore[0] != "guides/a.md" {
		t.Fatalf("撤回前混合检索应命中该文档: %+v", hitsBefore)
	}

	// 撤回：删正文/块，并经钩子删向量点。写入时的 SyncDocument 已经有一次
	// replace-on-write 删除，这里清空记录，只断言撤回这次新增的删除。
	q.deletes = nil
	if err := e.RetractDocument(ctx, "src1", "guides/a.md"); err != nil {
		t.Fatal(err)
	}
	if len(q.deletes) != 1 || q.deletes[0] != "src1|guides/a.md" {
		t.Fatalf("撤回应触发按 source+path 的向量删除: %+v", q.deletes)
	}
	if len(q.points) != 0 {
		t.Fatalf("撤回后向量点应被清空: %d 个残留", len(q.points))
	}

	// 撤回后混合检索不再命中该文档（关键词块已删、向量点已删）。
	hitsAfter := searchPaths(t, e, ctx, "青鸾新羽麒麟", []string{"src1"})
	if len(hitsAfter) != 0 {
		t.Fatalf("撤回后混合检索不应再命中: %+v", hitsAfter)
	}
}

func searchPaths(t *testing.T, e *Engine, ctx context.Context, query string, sources []string) []string {
	t.Helper()
	res, err := e.Search(ctx, query, sources)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, h := range res.Results {
		out = append(out, h.Path)
	}
	return out
}
