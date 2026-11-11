package engine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge/engine/embed"
	"agentflow/internal/knowledge/engine/qdrant"
)

// ---- fakes for the fusion layer and the vector runtime ----

type fakeVectorChannel struct {
	active bool
	hits   []Hit
	err    error
	calls  int
}

func (f *fakeVectorChannel) Active(context.Context, []string) bool { return f.active }
func (f *fakeVectorChannel) Search(_ context.Context, _ string, _ []string, _ int) ([]Hit, error) {
	f.calls++
	return f.hits, f.err
}

// vectorFakeContent satisfies both the engine's ContentStore and the
// VectorContentStore used by the global vector runtime, so a single write path
// can be observed from both sides.
type vectorFakeContent struct {
	*fakeContent
	chunksByPath    map[string][]domain.KnowledgeChunk
	chunksBySource  map[string][]domain.KnowledgeChunk
	allChunkCalls   int
}

func newVectorFakeContent() *vectorFakeContent {
	return &vectorFakeContent{
		fakeContent:    newFakeContent(),
		chunksByPath:   map[string][]domain.KnowledgeChunk{},
		chunksBySource: map[string][]domain.KnowledgeChunk{},
	}
}

func (f *vectorFakeContent) KnowledgeChunks(sourceID, path string) []domain.KnowledgeChunk {
	if v, ok := f.chunksByPath[docKey(sourceID, path)]; ok {
		return v
	}
	// 托管写入测试里，块经 WriteHostedDocument 落在 fakeContent.chunks。
	return f.fakeContent.chunks[docKey(sourceID, path)]
}
func (f *vectorFakeContent) KnowledgeChunksBySource(sourceID string) []domain.KnowledgeChunk {
	return f.chunksBySource[sourceID]
}
func (f *vectorFakeContent) KnowledgeChunksForSources(sourceIDs []string) []domain.KnowledgeChunk {
	f.allChunkCalls++
	out := []domain.KnowledgeChunk{}
	for _, id := range sourceIDs {
		out = append(out, f.chunksBySource[id]...)
	}
	return out
}

type vectorFakeControl struct {
	*fakeControl
	bases   map[string]domain.KnowledgeBase
	setCalls []vectorSetCall
}

type vectorSetCall struct {
	id         string
	enabled    bool
	model      string
	dim        int
	collection string
}

func newVectorFakeControl() *vectorFakeControl {
	return &vectorFakeControl{fakeControl: newFakeControl(), bases: map[string]domain.KnowledgeBase{}}
}
func (f *vectorFakeControl) KnowledgeBase(id string) (domain.KnowledgeBase, bool) {
	b, ok := f.bases[id]
	return b, ok
}
func (f *vectorFakeControl) KnowledgeBases(string) []domain.KnowledgeBase {
	out := make([]domain.KnowledgeBase, 0, len(f.bases))
	for _, b := range f.bases {
		out = append(out, b)
	}
	return out
}
func (f *vectorFakeControl) SetKnowledgeVectorConfig(id string, enabled bool, model string, dim int, collection string) error {
	f.setCalls = append(f.setCalls, vectorSetCall{id, enabled, model, dim, collection})
	if b, ok := f.bases[id]; ok {
		b.VectorEnabled = enabled
		b.EmbeddingModel = model
		b.EmbeddingDim = dim
		b.QdrantCollection = collection
		f.bases[id] = b
	}
	return nil
}

type qdrantCall struct {
	op       string
	name     string
	sourceID string
	path     string
	dim      int
	points   []qdrant.Point
}

type fakeQdrantStore struct {
	calls        []qdrantCall
	info         qdrant.CollectionInfo
	searchPoints []qdrant.ScoredPoint
	searchErr    error
}

func (f *fakeQdrantStore) EnsureCollection(_ context.Context, name string, dim int) error {
	f.calls = append(f.calls, qdrantCall{op: "ensure", name: name, dim: dim})
	return nil
}
func (f *fakeQdrantStore) DeleteCollection(_ context.Context, name string) error {
	f.calls = append(f.calls, qdrantCall{op: "delete-collection", name: name})
	return nil
}
func (f *fakeQdrantStore) DeleteByFilter(_ context.Context, name, sourceID, path string) error {
	f.calls = append(f.calls, qdrantCall{op: "delete", name: name, sourceID: sourceID, path: path})
	return nil
}
func (f *fakeQdrantStore) Upsert(_ context.Context, name string, points []qdrant.Point) error {
	f.calls = append(f.calls, qdrantCall{op: "upsert", name: name, points: points})
	return nil
}
func (f *fakeQdrantStore) Search(_ context.Context, name string, _ []float32, _ []string, _ int) ([]qdrant.ScoredPoint, error) {
	f.calls = append(f.calls, qdrantCall{op: "search", name: name})
	return f.searchPoints, f.searchErr
}
func (f *fakeQdrantStore) CollectionInfo(context.Context, string) (qdrant.CollectionInfo, error) {
	return f.info, nil
}
func (f *fakeQdrantStore) ops() []string {
	out := []string{}
	for _, c := range f.calls {
		out = append(out, c.op)
	}
	return out
}
func (f *fakeQdrantStore) lastUpsert() ([]qdrant.Point, bool) {
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].op == "upsert" {
			return f.calls[i].points, true
		}
	}
	return nil, false
}

type fakeEmbedProvider struct {
	dim   int
	calls int
	texts []string
}

func (f *fakeEmbedProvider) Name() string                       { return "fake" }
func (f *fakeEmbedProvider) Available() bool                    { return true }
func (f *fakeEmbedProvider) Dim() int                          { return f.dim }
func (f *fakeEmbedProvider) Probe(context.Context) error        { return nil }
func (f *fakeEmbedProvider) Embed(ctx context.Context, text string, isQuery bool) ([]float32, error) {
	v, err := f.EmbedBatch(ctx, []string{text}, isQuery)
	if err != nil {
		return nil, err
	}
	return v[0], nil
}
func (f *fakeEmbedProvider) EmbedBatch(_ context.Context, texts []string, _ bool) ([][]float32, error) {
	f.calls++
	f.texts = append(f.texts, texts...)
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, f.dim)
		v[0] = 1 // 单位向量
		out[i] = v
	}
	return out, nil
}

// ---- helpers ----

func testVectorConfig() VectorConfig {
	return VectorConfig{
		QdrantURL: "http://qdrant:6333", QdrantAPIKey: "",
		Collection: "kb_model_1024",
		EmbeddingKind: embed.Sidecar, EmbeddingURL: "http://embedding:8081/embed",
		EmbeddingAPIKey: "", Model: "model", Dim: 1024, MaxLength: 256,
	}
}

func newTestVectorRuntime(cfg VectorConfig, content VectorContentStore, control VectorControlStore, q qdrantStore, ep embed.Provider) *VectorRuntime {
	return &VectorRuntime{cfg: cfg, embed: ep, qdrant: q, content: content, control: control}
}

// setGlobalVectorForTest swaps the process-global runtime and returns a restore
// func, so tests never leak an enabled runtime into each other.
func setGlobalVectorForTest(rt *VectorRuntime) func() {
	vectorMu.Lock()
	prev := globalVector
	globalVector = rt
	vectorMu.Unlock()
	return func() {
		vectorMu.Lock()
		globalVector = prev
		vectorMu.Unlock()
	}
}

func makeChunks(source, path string, ids ...int64) []domain.KnowledgeChunk {
	out := make([]domain.KnowledgeChunk, 0, len(ids))
	for i, id := range ids {
		out = append(out, domain.KnowledgeChunk{
			ID: id, SourceID: source, Path: path, Idx: i,
			StartLine: i*3 + 1, EndLine: i*3 + 3,
			Title: "标题", Heading: "小节", Commit: "1",
			Text: fmt.Sprintf("%s 第%d块 数据库连接串配置", path, i),
		})
	}
	return out
}

// ---- 1. 向量关闭时行为不变 ----

func TestVectorOffBehaviorUnchanged(t *testing.T) {
	restore := setGlobalVectorForTest(&VectorRuntime{cfg: VectorConfig{}})
	defer restore()

	index := &fakeIndex{hits: []Hit{
		{Source: "allowed", Commit: "1", Path: "a.md", StartLine: 1, EndLine: 3, Text: "麒麟内容", Score: 3},
		{Source: "allowed", Commit: "1", Path: "b.md", StartLine: 2, EndLine: 4, Text: "貔貅内容", Score: 2},
	}}
	control := newFakeControl()
	content := newFakeContent()
	e := NewEngine(control, content, index) // 走全局禁用运行时

	got, err := e.Search(context.Background(), "麒麟", []string{"allowed"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != "lexical" || got.Degraded || got.DegradeReason != "" {
		t.Fatalf("向量关闭时应是纯关键词、不降级、无降级原因: %+v", got)
	}
	if index.opts.Limit != DefaultSearchLimit {
		t.Fatalf("向量关闭时给关键词适配器的调用参数必须不变: %+v", index.opts)
	}
	if index.calls != 1 {
		t.Fatalf("关键词适配器应只被调用一次: %d", index.calls)
	}
	if len(got.Results) != 2 || got.Results[0].Citation != "allowed@1:a.md#L1-L3" || got.Results[0].Score != 3 {
		t.Fatalf("结果映射应与纯关键词版本一致: %+v", got.Results)
	}

	// 部署级配置禁用时 Active 必为 false，任何来源都不触发向量通道。
	if vectorRuntime().Active(context.Background(), []string{"allowed"}) {
		t.Fatal("禁用配置下 Active 不应为 true")
	}
	// 显式 nil 向量通道同样保持纯关键词。
	hr := NewHybridRetriever(index, nil)
	rr, err := hr.Retrieve(context.Background(), "麒麟", []string{"allowed"}, DefaultSearchLimit)
	if err != nil || rr.Mode != "lexical" || rr.Degraded || len(rr.Hits) != 2 {
		t.Fatalf("nil 向量通道应等价于纯关键词: %+v err=%v", rr, err)
	}
}

// ---- 2. 向量通道故障时降级且标记 ----

func TestVectorChannelFailureDegradesAndMarks(t *testing.T) {
	index := &fakeIndex{hits: []Hit{
		{Source: "allowed", Commit: "1", Path: "a.md", StartLine: 1, EndLine: 3, Text: "麒麟内容", Score: 3},
	}}
	vec := &fakeVectorChannel{active: true, err: errors.New("qdrant 连接被拒绝")}
	hr := NewHybridRetriever(index, vec)

	rr, err := hr.Retrieve(context.Background(), "麒麟", []string{"allowed"}, DefaultSearchLimit)
	if err != nil {
		t.Fatal(err)
	}
	if rr.Mode != "lexical" || !rr.Degraded || rr.Reason == "" {
		t.Fatalf("向量故障应降级为关键词并标记: %+v", rr)
	}
	// 降级原因只给安全文案，不回传底层错误细节。
	if strings.Contains(rr.Reason, "qdrant") || strings.Contains(rr.Reason, "连接被拒绝") {
		t.Fatalf("降级原因不应泄漏底层错误: %q", rr.Reason)
	}
	if len(rr.Hits) != 1 || rr.Hits[0].Path != "a.md" {
		t.Fatalf("降级后仍应返回关键词命中: %+v", rr.Hits)
	}

	// 经 Engine.Search 时标记应透到 JSON 契约字段上。
	e := NewEngineWithRetriever(newFakeControl(), newFakeContent(), hr)
	got, err := e.Search(context.Background(), "麒麟", []string{"allowed"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Degraded || got.DegradeReason == "" || got.Mode != "lexical" || len(got.Results) != 1 {
		t.Fatalf("Engine.Search 未透传降级标记: %+v", got)
	}
}

// ---- 融合 ----

func TestHybridFusesBothChannels(t *testing.T) {
	index := &fakeIndex{hits: []Hit{
		{Source: "s", Commit: "1", Path: "a.md", StartLine: 1, EndLine: 3, Text: "A", Score: 1},
		{Source: "s", Commit: "1", Path: "b.md", StartLine: 1, EndLine: 3, Text: "B", Score: 1},
	}}
	vec := &fakeVectorChannel{active: true, hits: []Hit{
		{Source: "s", Commit: "1", Path: "a.md", StartLine: 1, EndLine: 3, Text: "A"}, // 两路都命中
		{Source: "s", Commit: "1", Path: "c.md", StartLine: 1, EndLine: 3, Text: "C"}, // 纯向量命中
	}}
	hr := NewHybridRetriever(index, vec)
	rr, err := hr.Retrieve(context.Background(), "问句", []string{"s"}, DefaultSearchLimit)
	if err != nil {
		t.Fatal(err)
	}
	if rr.Mode != "hybrid" || rr.Degraded {
		t.Fatalf("两路成功应为 hybrid 且不降级: %+v", rr)
	}
	if len(rr.Hits) != 3 {
		t.Fatalf("融合应保留两路并集: %+v", rr.Hits)
	}
	// a.md 两路都命中，RRF 分最高，应排第一；纯向量命中也应进入结果。
	if rr.Hits[0].Path != "a.md" {
		t.Fatalf("双通道命中应排最前: %+v", rr.Hits)
	}
	seenC := false
	for _, h := range rr.Hits {
		if h.Path == "c.md" {
			seenC = true
		}
	}
	if !seenC {
		t.Fatal("纯向量命中未进入融合结果")
	}
	// 启用混合时关键词召回窗口放宽，供 RRF 使用。
	if index.opts.Limit != keywordWindow {
		t.Fatalf("混合时关键词窗口应为 %d: %+v", keywordWindow, index.opts)
	}
}

func TestFuseRRFAndLimit(t *testing.T) {
	kw := []Hit{
		{Source: "s", Path: "a.md", StartLine: 1, EndLine: 2, Text: "A", Score: 5},
		{Source: "s", Path: "b.md", StartLine: 1, EndLine: 2, Text: "B", Score: 3},
		{Source: "s", Path: "c.md", StartLine: 1, EndLine: 2, Text: "C", Score: 1},
	}
	vec := []Hit{
		{Source: "s", Path: "d.md", StartLine: 1, EndLine: 2, Text: "D"},
		{Source: "s", Path: "a.md", StartLine: 1, EndLine: 2, Text: "A"},
	}
	got := Fuse("", kw, vec, 3)
	if len(got) != 3 {
		t.Fatalf("limit 应截断到 3: %d", len(got))
	}
	// a.md 在两路都出现，RRF 分应最高。
	if got[0].Path != "a.md" {
		t.Fatalf("双路命中应排第一: %+v", got)
	}
	// 输入无重复块时输出也应无重复。
	seen := map[string]bool{}
	for _, h := range got {
		if seen[h.Path] {
			t.Fatalf("融合结果出现重复块: %s", h.Path)
		}
		seen[h.Path] = true
	}
	// limit<=0 不截断。
	all := Fuse("", kw, vec, 0)
	if len(all) != 4 {
		t.Fatalf("limit<=0 应保留全部并集: %d", len(all))
	}
}

// ---- 3. 换模型需重建 ----

func TestCollectionNameChangesWithModelOrDim(t *testing.T) {
	a := CollectionName("BAAI/bge-m3", 1024)
	b := CollectionName("BAAI/bge-m3", 768)
	c := CollectionName("other/model", 1024)
	if a == b || a == c {
		t.Fatalf("模型或维度变化必须换 collection: a=%s b=%s c=%s", a, b, c)
	}
	if strings.ContainsAny(a, "/ .") {
		t.Fatalf("collection 名应只含安全字符: %s", a)
	}
}

func TestModelChangeTriggersFullRebuild(t *testing.T) {
	cfg := testVectorConfig()
	control := newVectorFakeControl()
	// 知识库仍指向旧 collection（模型已变、重启后待重建）。
	control.bases["base1"] = domain.KnowledgeBase{
		ID: "base1", VectorEnabled: true, EmbeddingModel: "old-model",
		EmbeddingDim: 1024, QdrantCollection: "kb_old_model_1024",
	}
	control.sources["src1"] = domain.KnowledgeSource{ID: "src1", BaseID: "base1"}
	content := newVectorFakeContent()
	content.chunksBySource["src1"] = makeChunks("src1", "a.md", 11, 12, 13)
	q := &fakeQdrantStore{info: qdrant.CollectionInfo{Exists: true, Dim: cfg.Dim}}
	ep := &fakeEmbedProvider{dim: cfg.Dim}
	rt := newTestVectorRuntime(cfg, content, control, q, ep)

	if err := rt.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	ops := q.ops()
	if !contains(ops, "delete-collection") || !contains(ops, "ensure") || !contains(ops, "upsert") {
		t.Fatalf("换模型应删旧集合/建新集合/全量重建: %v", ops)
	}
	points, ok := q.lastUpsert()
	if !ok || len(points) != 3 {
		t.Fatalf("全量重建应把所有块重新嵌入写入: points=%v ok=%v", points, ok)
	}
	if ep.calls == 0 {
		t.Fatal("全量重建应调用 embedding 重新编码")
	}
	// 行上的索引契约应刷成当前模型/维度/collection。
	if len(control.setCalls) != 1 {
		t.Fatalf("应把知识库行刷成新契约: %+v", control.setCalls)
	}
	want := vectorSetCall{"base1", true, cfg.Model, cfg.Dim, cfg.Collection}
	if !reflect.DeepEqual(control.setCalls[0], want) {
		t.Fatalf("新契约错误: got %+v want %+v", control.setCalls[0], want)
	}
}

func TestNoRebuildWhenContractCurrent(t *testing.T) {
	cfg := testVectorConfig()
	control := newVectorFakeControl()
	control.bases["base1"] = domain.KnowledgeBase{
		ID: "base1", VectorEnabled: true, QdrantCollection: cfg.Collection,
	}
	content := newVectorFakeContent()
	q := &fakeQdrantStore{info: qdrant.CollectionInfo{Exists: true, Dim: cfg.Dim}}
	rt := newTestVectorRuntime(cfg, content, control, q, &fakeEmbedProvider{dim: cfg.Dim})

	if err := rt.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(q.calls) != 0 {
		t.Fatalf("契约一致且集合维度正确时不应重建: %v", q.ops())
	}
}

// ---- 4. 增量更新 ----

func TestSyncDocumentIncrementalReplacesVectors(t *testing.T) {
	cfg := testVectorConfig()
	control := newVectorFakeControl()
	control.bases["base1"] = domain.KnowledgeBase{ID: "base1", VectorEnabled: true, QdrantCollection: cfg.Collection}
	control.sources["src1"] = domain.KnowledgeSource{ID: "src1", BaseID: "base1"}
	content := newVectorFakeContent()
	q := &fakeQdrantStore{info: qdrant.CollectionInfo{Exists: true, Dim: cfg.Dim}}
	rt := newTestVectorRuntime(cfg, content, control, q, &fakeEmbedProvider{dim: cfg.Dim})

	// 首次写入：三个块。
	content.chunksByPath[docKey("src1", "a.md")] = makeChunks("src1", "a.md", 101, 102, 103)
	if err := rt.SyncDocument(context.Background(), "src1", "a.md"); err != nil {
		t.Fatal(err)
	}
	// 必须先按 source+path 删旧点，再写新点（块重排后 chunk ID 会变）。
	if q.calls[0].op != "delete" || q.calls[0].sourceID != "src1" || q.calls[0].path != "a.md" {
		t.Fatalf("增量更新应先删该文档旧向量: %+v", q.calls)
	}
	points, ok := q.lastUpsert()
	if !ok || len(points) != 3 || points[0].ID != 101 {
		t.Fatalf("应写入当前三个块的向量: %+v", points)
	}

	// 覆盖后块 ID 全变：旧 ID 的点必须被删掉，只留新 ID。
	q.calls = nil
	content.chunksByPath[docKey("src1", "a.md")] = makeChunks("src1", "a.md", 201, 202)
	if err := rt.SyncDocument(context.Background(), "src1", "a.md"); err != nil {
		t.Fatal(err)
	}
	if q.calls[0].op != "delete" {
		t.Fatalf("覆盖时也应先删旧点: %+v", q.calls)
	}
	newPoints, _ := q.lastUpsert()
	if len(newPoints) != 2 || newPoints[0].ID != 201 {
		t.Fatalf("覆盖后应只写新块的向量: %+v", newPoints)
	}
}

func TestSyncDocumentNoopWhenKBDisabled(t *testing.T) {
	cfg := testVectorConfig()
	control := newVectorFakeControl()
	control.bases["base1"] = domain.KnowledgeBase{ID: "base1", VectorEnabled: false}
	control.sources["src1"] = domain.KnowledgeSource{ID: "src1", BaseID: "base1"}
	content := newVectorFakeContent()
	content.chunksByPath[docKey("src1", "a.md")] = makeChunks("src1", "a.md", 1)
	q := &fakeQdrantStore{}
	rt := newTestVectorRuntime(cfg, content, control, q, &fakeEmbedProvider{dim: cfg.Dim})

	if err := rt.SyncDocument(context.Background(), "src1", "a.md"); err != nil {
		t.Fatal(err)
	}
	if len(q.calls) != 0 {
		t.Fatalf("知识库未启用向量时不应触碰向量库: %v", q.ops())
	}
}

func TestEngineWriteDocumentTriggersIncrementalVector(t *testing.T) {
	cfg := testVectorConfig()
	control := newVectorFakeControl()
	control.bases["base1"] = domain.KnowledgeBase{ID: "base1", VectorEnabled: true, QdrantCollection: cfg.Collection}
	control.sources["src1"] = domain.KnowledgeSource{ID: "src1", BaseID: "base1", Kind: "hosted"}
	content := newVectorFakeContent()
	q := &fakeQdrantStore{}
	ep := &fakeEmbedProvider{dim: cfg.Dim}
	// 全局运行时与引擎共用同一份 content/control，模拟生产里都是 *repo.Store。
	restore := setGlobalVectorForTest(newTestVectorRuntime(cfg, content, control, q, ep))
	defer restore()

	e := NewEngine(control, content, &fakeIndex{})
	if _, err := e.WriteDocument(context.Background(), "src1", "guides/a.md", testMarkdown); err != nil {
		t.Fatal(err)
	}
	// WriteDocument 重建块后，afterDocumentIndexed 应把当前块写入向量库。
	points, ok := q.lastUpsert()
	if !ok || len(points) == 0 {
		t.Fatalf("托管写入后应增量更新向量: ops=%v points=%v", q.ops(), points)
	}
}

func TestSyncSourceIncremental(t *testing.T) {
	cfg := testVectorConfig()
	control := newVectorFakeControl()
	control.bases["base1"] = domain.KnowledgeBase{ID: "base1", VectorEnabled: true, QdrantCollection: cfg.Collection}
	control.sources["git1"] = domain.KnowledgeSource{ID: "git1", BaseID: "base1"}
	content := newVectorFakeContent()
	content.chunksBySource["git1"] = makeChunks("git1", "a.md", 1, 2)
	content.chunksBySource["git1"] = append(content.chunksBySource["git1"], makeChunks("git1", "b.md", 3)...)
	q := &fakeQdrantStore{}
	rt := newTestVectorRuntime(cfg, content, control, q, &fakeEmbedProvider{dim: cfg.Dim})

	if err := rt.SyncSource(context.Background(), "git1"); err != nil {
		t.Fatal(err)
	}
	// 整个来源：删全部旧点（path 为空），再写全来源块。
	if q.calls[0].op != "delete" || q.calls[0].path != "" {
		t.Fatalf("来源同步应按 source 删旧点: %+v", q.calls)
	}
	points, _ := q.lastUpsert()
	if len(points) != 3 {
		t.Fatalf("应写入该来源全部块的向量: %d", len(points))
	}
}

// ---- 读侧：向量通道 Active/Search ----

func TestVectorRuntimeActiveRequiresEnabledKB(t *testing.T) {
	cfg := testVectorConfig()
	control := newVectorFakeControl()
	control.bases["base1"] = domain.KnowledgeBase{ID: "base1", VectorEnabled: true, QdrantCollection: cfg.Collection}
	control.sources["enabled"] = domain.KnowledgeSource{ID: "enabled", BaseID: "base1"}
	control.bases["base2"] = domain.KnowledgeBase{ID: "base2", VectorEnabled: false}
	control.sources["disabled"] = domain.KnowledgeSource{ID: "disabled", BaseID: "base2"}
	// stale：启用了但 collection 还是旧值，模型已变、尚未重建。
	control.bases["base3"] = domain.KnowledgeBase{ID: "base3", VectorEnabled: true, QdrantCollection: "old"}
	control.sources["stale"] = domain.KnowledgeSource{ID: "stale", BaseID: "base3"}
	rt := newTestVectorRuntime(cfg, newVectorFakeContent(), control, &fakeQdrantStore{}, &fakeEmbedProvider{dim: cfg.Dim})

	if !rt.Active(context.Background(), []string{"enabled"}) {
		t.Fatal("已启用且契约一致的来源应激活向量通道")
	}
	if rt.Active(context.Background(), []string{"disabled", "stale"}) {
		t.Fatal("未启用或契约过期的来源不应激活向量通道")
	}
	if rt.Active(context.Background(), []string{"unknown"}) {
		t.Fatal("未知来源不应激活向量通道")
	}
}

func TestVectorRuntimeSearchBuildsHitsFromPayload(t *testing.T) {
	cfg := testVectorConfig()
	control := newVectorFakeControl()
	control.bases["base1"] = domain.KnowledgeBase{ID: "base1", VectorEnabled: true, QdrantCollection: cfg.Collection}
	control.sources["src1"] = domain.KnowledgeSource{ID: "src1", BaseID: "base1"}
	q := &fakeQdrantStore{searchPoints: []qdrant.ScoredPoint{
		{ID: 7, Score: 0.9, Payload: map[string]any{
			"source_id": "src1", "path": "a.md", "commit": "1",
			"title": "标题", "heading": "小节",
			"start_line": float64(10), "end_line": float64(13), "text": "命中正文",
		}},
	}}
	rt := newTestVectorRuntime(cfg, newVectorFakeContent(), control, q, &fakeEmbedProvider{dim: cfg.Dim})
	hits, err := rt.Search(context.Background(), "问句", []string{"src1"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("应返回一个向量命中: %+v", hits)
	}
	h := hits[0]
	if h.Source != "src1" || h.Path != "a.md" || h.StartLine != 10 || h.EndLine != 13 || h.Text != "命中正文" {
		t.Fatalf("载荷字段还原错误（含 float64 数字）: %+v", h)
	}
}

func contains(list []string, want string) bool {
	for _, x := range list {
		if x == want {
			return true
		}
	}
	return false
}
