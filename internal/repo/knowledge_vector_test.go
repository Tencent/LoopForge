package repo

import (
	"testing"

	"agentflow/internal/domain"
)

// TestKnowledgeBaseVectorConfig 覆盖 045 新增列：索引契约随知识库行持久化，
// 启用/停用只更新向量列而不动其它字段。
func TestKnowledgeBaseVectorConfig(t *testing.T) {
	s := builtinStore(t)
	base := domain.KnowledgeBase{
		ID: "kb-alpha", ProjectID: "proj-alpha", Name: "项目知识库", Layer: "project",
		VectorEnabled: true, EmbeddingModel: "BAAI/bge-m3", EmbeddingDim: 1024,
		QdrantCollection: "kb_baai_bge_m3_1024",
	}
	if err := s.PutKnowledgeBase(base); err != nil {
		t.Fatal(err)
	}
	got, ok := s.KnowledgeBase("kb-alpha")
	if !ok || !got.VectorEnabled || got.EmbeddingModel != "BAAI/bge-m3" ||
		got.EmbeddingDim != 1024 || got.QdrantCollection != "kb_baai_bge_m3_1024" {
		t.Fatalf("向量契约未持久化: %+v ok=%v", got, ok)
	}

	// 换模型：改契约并停用，Name/Layer 不能被覆盖。
	if err := s.SetKnowledgeVectorConfig("kb-alpha", false, "new-model", 768, "kb_new_model_768"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.KnowledgeBase("kb-alpha")
	if got.VectorEnabled || got.EmbeddingModel != "new-model" || got.EmbeddingDim != 768 ||
		got.QdrantCollection != "kb_new_model_768" {
		t.Fatalf("配置未更新: %+v", got)
	}
	if got.Name != "项目知识库" || got.Layer != "project" || got.ProjectID != "proj-alpha" {
		t.Fatalf("定向更新不应覆盖其它字段: %+v", got)
	}

	// 新库默认不启用向量（纯关键词，只有 MySQL 也必须能用）。
	fresh := domain.KnowledgeBase{ID: "kb-fresh", ProjectID: "proj-beta", Name: "新库", Layer: "project"}
	if err := s.PutKnowledgeBase(fresh); err != nil {
		t.Fatal(err)
	}
	got, _ = s.KnowledgeBase("kb-fresh")
	if got.VectorEnabled || got.QdrantCollection != "" {
		t.Fatalf("新知识库默认应关闭向量: %+v", got)
	}
}

// TestKnowledgeChunksForVectorIndex 覆盖向量索引读方法：按来源读出带自增 ID
// 的块，供增量/全量重建从 MySQL 正文重建向量。
func TestKnowledgeChunksForVectorIndex(t *testing.T) {
	s := builtinStore(t)
	chunks := []domain.KnowledgeChunk{
		{Idx: 0, StartLine: 1, EndLine: 2, Text: "甲文档第一段"},
		{Idx: 1, StartLine: 3, EndLine: 4, Text: "甲文档第二段"},
	}
	if err := s.ReplaceKnowledgeChunks("src1", "a.md", chunks); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceKnowledgeChunks("src1", "b.md", []domain.KnowledgeChunk{{Idx: 0, Text: "乙文档"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceKnowledgeChunks("src2", "c.md", []domain.KnowledgeChunk{{Idx: 0, Text: "丙文档"}}); err != nil {
		t.Fatal(err)
	}

	bySource := s.KnowledgeChunksBySource("src1")
	if len(bySource) != 3 {
		t.Fatalf("src1 应有 3 个块: %d", len(bySource))
	}
	for _, c := range bySource {
		if c.ID == 0 {
			t.Fatal("向量点 ID 需要块的非空自增 ID")
		}
	}
	multi := s.KnowledgeChunksForSources([]string{"src1", "src2"})
	if len(multi) != 4 {
		t.Fatalf("多来源合并读出应有 4 个块: %d", len(multi))
	}
	if len(s.KnowledgeChunksForSources(nil)) != 0 {
		t.Fatal("空来源集合应返回空")
	}
	// 单路径读取仍可用（增量 SyncDocument 用）。
	if len(s.KnowledgeChunks("src1", "a.md")) != 2 {
		t.Fatal("按路径读取块错误")
	}
}
