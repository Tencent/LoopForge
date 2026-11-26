package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge/engine/embed"
	"agentflow/internal/knowledge/engine/qdrant"
)

// VectorContentStore 读取内容面（MySQL）的索引块供向量化。向量库只是缓存，
// 事实源是正文切出的块；全量重建与增量更新都只读这些块，不从 Qdrant 取输入。
type VectorContentStore interface {
	KnowledgeChunks(sourceID, path string) []domain.KnowledgeChunk
	KnowledgeChunksBySource(sourceID string) []domain.KnowledgeChunk
	KnowledgeChunksForSources(sourceIDs []string) []domain.KnowledgeChunk
}

// VectorControlStore 解析/更新知识库向量配置，并枚举来源用于全量重建。
type VectorControlStore interface {
	KnowledgeSource(id string) (domain.KnowledgeSource, bool)
	KnowledgeBase(id string) (domain.KnowledgeBase, bool)
	KnowledgeBases(projectID string) []domain.KnowledgeBase
	KnowledgeSources(baseID string) []domain.KnowledgeSource
	SetKnowledgeVectorConfig(id string, enabled bool, model string, dim int, collection string) error
}

// qdrantStore 是向量库的最小依赖面，便于用假实现测试增量/全量重建。
// *qdrant.Client 满足它。
type qdrantStore interface {
	EnsureCollection(ctx context.Context, name string, dim int) error
	DeleteCollection(ctx context.Context, name string) error
	DeleteByFilter(ctx context.Context, name, sourceID, path string) error
	Upsert(ctx context.Context, name string, points []qdrant.Point) error
	Search(ctx context.Context, name string, vector []float32, sources []string, limit int) ([]qdrant.ScoredPoint, error)
	CollectionInfo(ctx context.Context, name string) (qdrant.CollectionInfo, error)
}

var _ qdrantStore = (*qdrant.Client)(nil)

const embedBatchSize = 32

// VectorRuntime 是可选向量通道的实现：读侧做语义检索，写侧做增量索引与
// 全量重建。配置禁用时所有方法都是安全的空操作，Active() 返回 false。
type VectorRuntime struct {
	cfg     VectorConfig
	embed   embed.Provider
	qdrant  qdrantStore
	content VectorContentStore
	control VectorControlStore

	mu sync.Mutex // 串行化全量重建，避免并发删集合

	progressMu sync.RWMutex
	progress   map[string]VectorProgress
}

// VectorProgress 是一次向量重建的只读进度。它只存在处理同步的进程里，
// 用于在知识中心显示「重建向量中 N/M」；多实例部署时其他实例看不到，
// 会继续显示来源的同步状态，不影响正确性。
type VectorProgress struct {
	SourceID string `json:"sourceId"`
	Current  int    `json:"current"`
	Total    int    `json:"total"`
	Phase    string `json:"phase"`
}

var _ VectorChannel = (*VectorRuntime)(nil)

func newVectorRuntime(cfg VectorConfig, content VectorContentStore, control VectorControlStore) *VectorRuntime {
	provider := embed.NewProvider(embed.Config{
		Kind: cfg.EmbeddingKind, URL: cfg.EmbeddingURL, APIKey: cfg.EmbeddingAPIKey,
		Model: cfg.Model, Dim: cfg.Dim, MaxLength: cfg.MaxLength,
	})
	return &VectorRuntime{
		cfg: cfg, embed: provider, qdrant: qdrant.New(cfg.QdrantURL, cfg.QdrantAPIKey),
		content: content, control: control, progress: map[string]VectorProgress{},
	}
}

// ---- 读侧：VectorChannel ----

// vectorEnabledSources 过滤出「所属知识库启用向量且索引契约与当前配置一致」
// 的来源。collection 不一致说明模型已变、尚未重建，暂不参与向量检索。
func (v *VectorRuntime) vectorEnabledSources(sources []string) []string {
	if v.control == nil {
		return nil
	}
	out := make([]string, 0, len(sources))
	seen := map[string]bool{}
	for _, id := range sources {
		if seen[id] {
			continue
		}
		seen[id] = true
		src, ok := v.control.KnowledgeSource(id)
		if !ok {
			continue
		}
		base, ok := v.control.KnowledgeBase(src.BaseID)
		if ok && base.VectorEnabled && base.QdrantCollection == v.cfg.Collection {
			out = append(out, id)
		}
	}
	return out
}

func (v *VectorRuntime) Active(_ context.Context, sources []string) bool {
	return v.cfg.Enabled() && len(v.vectorEnabledSources(sources)) > 0
}

func (v *VectorRuntime) Search(ctx context.Context, query string, sources []string, limit int) ([]Hit, error) {
	if !v.cfg.Enabled() {
		return nil, nil
	}
	vecSources := v.vectorEnabledSources(sources)
	if len(vecSources) == 0 {
		return nil, nil
	}
	vec, err := v.embed.Embed(ctx, query, true)
	if err != nil {
		return nil, err
	}
	pts, err := v.qdrant.Search(ctx, v.cfg.Collection, vec, vecSources, limit)
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(pts))
	for _, p := range pts {
		hits = append(hits, hitFromScoredPoint(p))
	}
	return hits, nil
}

// ---- 写侧：增量索引 ----

// SyncDocument 在托管文档写入后增量更新：先删该路径的旧向量（块重排后
// chunk ID 会变，必须按 source+path 删，不能按 ID），再读当前块重新嵌入。
func (v *VectorRuntime) SyncDocument(ctx context.Context, sourceID, path string) error {
	if !v.cfg.Enabled() || v.content == nil || !v.sourceVectorEnabled(sourceID) {
		return nil
	}
	if err := v.qdrant.DeleteByFilter(ctx, v.cfg.Collection, sourceID, path); err != nil {
		return err
	}
	return v.indexChunks(ctx, v.content.KnowledgeChunks(sourceID, path))
}

// SyncSource 在 Git 同步后增量更新整个来源的向量。
func (v *VectorRuntime) SyncSource(ctx context.Context, sourceID string) error {
	if !v.cfg.Enabled() || v.content == nil || !v.sourceVectorEnabled(sourceID) {
		return nil
	}
	if err := v.qdrant.DeleteByFilter(ctx, v.cfg.Collection, sourceID, ""); err != nil {
		return err
	}
	return v.indexSourceChunks(ctx, sourceID)
}

// indexSourceChunks embeds one source's current chunks and publishes progress
// under that source. The full rebuild and the post-sync incremental path share
// it so both show 「重建向量中 N/M」 in the knowledge center.
func (v *VectorRuntime) indexSourceChunks(ctx context.Context, sourceID string) error {
	chunks := v.content.KnowledgeChunksBySource(sourceID)
	if len(chunks) == 0 {
		return nil
	}
	v.setProgress(sourceID, 0, len(chunks))
	defer v.clearProgress(sourceID)
	return v.indexChunksWithProgress(ctx, chunks, sourceID)
}

// DeindexDocument 在文档撤回时删掉该文档路径的向量点，供
// Engine.SetVectorDeindexer 接线。正文与索引块已由 RetractDocument 删除，
// 这里只清 Qdrant 缓存：按 source+path 过滤删除，与增量更新用的是同一个
// 已有删除接口。全局向量未启用时是安全的空操作；对从未建过向量、或向量
// 开关后来被关掉的来源，删除也是幂等的（无匹配点即无操作）。
func (v *VectorRuntime) DeindexDocument(ctx context.Context, sourceID, path string) error {
	if !v.cfg.Enabled() {
		return nil
	}
	return v.qdrant.DeleteByFilter(ctx, v.cfg.Collection, sourceID, path)
}

func (v *VectorRuntime) sourceVectorEnabled(sourceID string) bool {
	if v.control == nil {
		return false
	}
	src, ok := v.control.KnowledgeSource(sourceID)
	if !ok {
		return false
	}
	base, ok := v.control.KnowledgeBase(src.BaseID)
	return ok && base.VectorEnabled && base.QdrantCollection == v.cfg.Collection
}

// indexChunks 分批嵌入并写入，单批不超过 sidecar 的 64 输入上限，也避免
// 一次把整库点都堆进内存。
func (v *VectorRuntime) indexChunks(ctx context.Context, chunks []domain.KnowledgeChunk) error {
	return v.indexChunksWithProgress(ctx, chunks, "")
}

func (v *VectorRuntime) indexChunksWithProgress(ctx context.Context, chunks []domain.KnowledgeChunk, progressSource string) error {
	if len(chunks) == 0 {
		return nil
	}
	for start := 0; start < len(chunks); start += embedBatchSize {
		end := minInt(start+embedBatchSize, len(chunks))
		batch := chunks[start:end]
		texts := make([]string, len(batch))
		for i, c := range batch {
			texts[i] = embeddingText(c)
		}
		vectors, err := v.embed.EmbedBatch(ctx, texts, false)
		if err != nil {
			return err
		}
		if len(vectors) != len(batch) {
			return fmt.Errorf("embedding 返回 %d 个向量，期望 %d", len(vectors), len(batch))
		}
		points := make([]qdrant.Point, 0, len(batch))
		for i, c := range batch {
			points = append(points, qdrant.Point{
				ID:      uint64(c.ID),
				Vector:  vectors[i],
				Payload: chunkPayload(c),
			})
		}
		if err := v.qdrant.Upsert(ctx, v.cfg.Collection, points); err != nil {
			return err
		}
		if progressSource != "" {
			v.setProgress(progressSource, end, len(chunks))
		}
	}
	return nil
}

func (v *VectorRuntime) setProgress(sourceID string, current, total int) {
	if sourceID == "" || total <= 0 {
		return
	}
	v.progressMu.Lock()
	defer v.progressMu.Unlock()
	if v.progress == nil {
		v.progress = map[string]VectorProgress{}
	}
	v.progress[sourceID] = VectorProgress{SourceID: sourceID, Current: current, Total: total, Phase: "rebuild"}
}

func (v *VectorRuntime) clearProgress(sourceID string) {
	if sourceID == "" {
		return
	}
	v.progressMu.Lock()
	defer v.progressMu.Unlock()
	delete(v.progress, sourceID)
}

func (v *VectorRuntime) progressFor(sourceID string) (VectorProgress, bool) {
	v.progressMu.RLock()
	defer v.progressMu.RUnlock()
	p, ok := v.progress[sourceID]
	return p, ok
}

// VectorProgressForSource 供管理接口显示当前实例正在重建的向量进度。
func VectorProgressForSource(sourceID string) (VectorProgress, bool) {
	return vectorRuntime().progressFor(sourceID)
}

// embeddingText 用标题、小节、正文拼出语义输入，帮助口语问句命中短块。
func embeddingText(c domain.KnowledgeChunk) string {
	parts := make([]string, 0, 3)
	if c.Title != "" {
		parts = append(parts, c.Title)
	}
	if c.Heading != "" && c.Heading != c.Title {
		parts = append(parts, c.Heading)
	}
	parts = append(parts, c.Text)
	return joinNonEmpty(parts, "\n")
}

func chunkPayload(c domain.KnowledgeChunk) map[string]any {
	return map[string]any{
		"source_id": c.SourceID, "path": c.Path, "idx": c.Idx,
		"start_line": c.StartLine, "end_line": c.EndLine,
		"title": c.Title, "heading": c.Heading, "commit": c.Commit,
		"text": c.Text,
	}
}

func hitFromScoredPoint(p qdrant.ScoredPoint) Hit {
	return Hit{
		Source:    payloadString(p.Payload, "source_id"),
		Commit:    payloadString(p.Payload, "commit"),
		Path:      payloadString(p.Payload, "path"),
		Title:     payloadString(p.Payload, "title"),
		Heading:   payloadString(p.Payload, "heading"),
		StartLine: payloadInt(p.Payload, "start_line"),
		EndLine:   payloadInt(p.Payload, "end_line"),
		Text:      payloadString(p.Payload, "text"),
		Score:     p.Score,
	}
}

// Qdrant 把 JSON 数字反序列化成 float64，这里宽容地取回整数/字符串字段。
func payloadString(p map[string]any, key string) string {
	if s, ok := p[key].(string); ok {
		return s
	}
	return ""
}

func payloadInt(p map[string]any, key string) int {
	switch x := p[key].(type) {
	case float64:
		return int(x)
	case float32:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	}
	return 0
}

// ---- 管理：启用/停用/换模型重建 ----

// EnableBase 给一个知识库启用向量：固定索引契约快照到该行，再把它现有来源
// 增量写进共享 collection。collection 按模型+维度派生，多个库共用、按
// source_id 过滤。
func (v *VectorRuntime) EnableBase(ctx context.Context, baseID string) error {
	if !v.cfg.Enabled() {
		return errors.New("平台未配置 Qdrant/embedding，无法启用向量检索")
	}
	if v.control == nil {
		return errors.New("向量控制面不可用")
	}
	base, ok := v.control.KnowledgeBase(baseID)
	if !ok {
		return fmt.Errorf("知识库 %s 不存在", baseID)
	}
	if err := v.qdrant.EnsureCollection(ctx, v.cfg.Collection, v.cfg.Dim); err != nil {
		return err
	}
	if err := v.control.SetKnowledgeVectorConfig(baseID, true, v.cfg.Model, v.cfg.Dim, v.cfg.Collection); err != nil {
		return err
	}
	for _, src := range v.control.KnowledgeSources(base.ID) {
		if err := v.SyncSource(ctx, src.ID); err != nil {
			return err
		}
	}
	return nil
}

// DisableBase 停用向量：删掉该库来源的向量点，并清空行上的索引契约。
func (v *VectorRuntime) DisableBase(ctx context.Context, baseID string) error {
	if v.control == nil {
		return errors.New("向量控制面不可用")
	}
	base, ok := v.control.KnowledgeBase(baseID)
	if !ok {
		return fmt.Errorf("知识库 %s 不存在", baseID)
	}
	collection := base.QdrantCollection
	if collection == "" {
		collection = v.cfg.Collection
	}
	if collection != "" {
		for _, src := range v.control.KnowledgeSources(base.ID) {
			_ = v.qdrant.DeleteByFilter(ctx, collection, src.ID, "")
		}
	}
	return v.control.SetKnowledgeVectorConfig(baseID, false, "", 0, "")
}

// Rebuild 全量重建：删集合后按当前契约重建，并把 sources 的全部块从
// MySQL 重新嵌入。用于「换模型换 collection 并全量重建」或显式重建。
func (v *VectorRuntime) Rebuild(ctx context.Context, sourceIDs []string) error {
	if !v.cfg.Enabled() || v.content == nil {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	runCtx, release, claimed, err := v.claimRebuild(ctx)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	defer release()
	return v.rebuild(runCtx, sourceIDs)
}
func (v *VectorRuntime) rebuild(ctx context.Context, sourceIDs []string) error {
	if err := v.setRebuildDirty(ctx, true); err != nil {
		return err
	}
	// 删集合可保证不残留旧模型/旧块的点；不存在时忽略 404。
	_ = v.qdrant.DeleteCollection(ctx, v.cfg.Collection)
	if err := v.qdrant.EnsureCollection(ctx, v.cfg.Collection, v.cfg.Dim); err != nil {
		return err
	}
	for _, sourceID := range sourceIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := v.indexSourceChunks(ctx, sourceID); err != nil {
			return err
		}
	}
	return v.setRebuildDirty(ctx, false)
}

func (v *VectorRuntime) enabledSourceIDs() []string {
	if v.control == nil {
		return nil
	}
	out := []string{}
	for _, b := range v.control.KnowledgeBases("") {
		if !b.VectorEnabled {
			continue
		}
		for _, s := range v.control.KnowledgeSources(b.ID) {
			out = append(out, s.ID)
		}
	}
	return out
}

// Reconcile 在 worker 启动时跑一次：检测「模型已变、知识库行上的 collection
// 还是旧值」或集合缺失/维度不符，触发全量重建并把行刷成当前契约。模型变更
// 需要重启（环境变量变化），启动时这里完成重建，运行期不反复扫库。
func (v *VectorRuntime) Reconcile(ctx context.Context) error {
	if !v.cfg.Enabled() || v.control == nil || v.content == nil {
		return nil
	}
	runCtx, release, claimed, err := v.claimRebuild(ctx)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	defer release()
	ctx = runCtx
	stale, err := v.rebuildDirty()
	if err != nil {
		return err
	}
	for _, b := range v.control.KnowledgeBases("") {
		if b.VectorEnabled && b.QdrantCollection != v.cfg.Collection {
			stale = true
			break
		}
	}
	info, _ := v.qdrant.CollectionInfo(ctx, v.cfg.Collection)
	if !info.Exists || (info.Dim != 0 && info.Dim != v.cfg.Dim) {
		stale = true
	}
	if !stale {
		return nil
	}
	if err := v.rebuild(ctx, v.enabledSourceIDs()); err != nil {
		return err
	}
	for _, b := range v.control.KnowledgeBases("") {
		if b.VectorEnabled {
			if err := v.control.SetKnowledgeVectorConfig(b.ID, true, v.cfg.Model, v.cfg.Dim, v.cfg.Collection); err != nil {
				return err
			}
		}
	}
	return nil
}

// 进程级管理入口，供 httpapi 与 worker 调用；无需把运行时到处传。
func EnableVectorForBase(ctx context.Context, baseID string) error {
	return vectorRuntime().EnableBase(ctx, baseID)
}
func DisableVectorForBase(ctx context.Context, baseID string) error {
	return vectorRuntime().DisableBase(ctx, baseID)
}
func ReconcileVector(ctx context.Context) error {
	return vectorRuntime().Reconcile(ctx)
}

// 写路径的 best-effort 钩子：向量是缓存，增量失败只记日志，不影响主库写入，
// 检索会自动降级为关键词。
func afterDocumentIndexed(ctx context.Context, sourceID, path string) {
	if err := vectorRuntime().SyncDocument(ctx, sourceID, path); err != nil {
		log.Printf("knowledge vector: sync document %s:%s: %v", sourceID, path, err)
	}
}
func afterSourceSynced(ctx context.Context, sourceID string) {
	if err := vectorRuntime().SyncSource(ctx, sourceID); err != nil {
		log.Printf("knowledge vector: sync source %s: %v", sourceID, err)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func joinNonEmpty(parts []string, sep string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += sep
		}
		out += p
	}
	return out
}

type vectorLeaseStore interface {
	ClaimKnowledgeOperation(string, string, time.Duration) (bool, error)
	RenewKnowledgeOperation(string, string, time.Duration) (bool, error)
	ReleaseKnowledgeOperation(string, string) error
}

func (v *VectorRuntime) claimRebuild(ctx context.Context) (context.Context, func(), bool, error) {
	store, ok := v.control.(vectorLeaseStore)
	if !ok {
		return ctx, func() {}, true, nil
	}
	id := "vector-" + sha256Hex(v.cfg.QdrantURL+"/"+v.cfg.Collection)
	owner := fmt.Sprintf("rebuild-%d-%d", time.Now().UnixNano(), revisionIDSeq.Add(1))
	ttl := time.Minute
	claimed, err := store.ClaimKnowledgeOperation(id, owner, ttl)
	if err != nil || !claimed {
		if err == nil {
			err = ErrVectorRebuildBusy
		}
		return ctx, func() {}, claimed, err
	}
	runCtx, cancel := context.WithCancel(context.WithValue(ctx, vectorLeaseKey{}, vectorLeaseClaim{id: id, owner: owner}))
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(ttl / 3)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				ok, err := store.RenewKnowledgeOperation(id, owner, ttl)
				if err != nil || !ok {
					cancel()
					return
				}
			}
		}
	}()
	return runCtx, func() { cancel(); <-done; _ = store.ReleaseKnowledgeOperation(id, owner) }, true, nil
}

var ErrVectorRebuildBusy = errors.New("向量索引正在由其他实例重建，将在租约到期后重试")

type vectorLeaseKey struct{}
type vectorLeaseClaim struct{ id, owner string }

func (v *VectorRuntime) setRebuildDirty(ctx context.Context, dirty bool) error {
	claim, ok := ctx.Value(vectorLeaseKey{}).(vectorLeaseClaim)
	if !ok {
		return nil
	}
	if state, ok := v.control.(interface {
		SetKnowledgeOperationDirty(string, string, bool) error
	}); ok {
		return state.SetKnowledgeOperationDirty(claim.id, claim.owner, dirty)
	}
	return nil
}
func (v *VectorRuntime) rebuildDirty() (bool, error) {
	if state, ok := v.control.(interface{ KnowledgeOperationDirty(string) (bool, error) }); ok {
		return state.KnowledgeOperationDirty("vector-" + sha256Hex(v.cfg.QdrantURL+"/"+v.cfg.Collection))
	}
	return false, nil
}
