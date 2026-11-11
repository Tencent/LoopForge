package repo

import (
	"os"
	"strings"
	"testing"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

func builtinStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// TestBuiltinKnowledgeControlPlane 覆盖 S1 控制面：创建知识库、项目绑定与来源。
func TestBuiltinKnowledgeControlPlane(t *testing.T) {
	s := builtinStore(t)
	base := domain.KnowledgeBase{
		ID: "kb-alpha", ProjectID: "proj-alpha", Name: "项目知识库",
		Layer: "project", OwnerUserID: 7,
	}
	if err := s.PutKnowledgeBase(base); err != nil {
		t.Fatal(err)
	}
	got, ok := s.KnowledgeBase("kb-alpha")
	if !ok || got.ProjectID != "proj-alpha" || got.Layer != "project" || got.Name != "项目知识库" {
		t.Fatalf("base = %+v ok=%v", got, ok)
	}
	if bases := s.KnowledgeBases("proj-alpha"); len(bases) != 1 || bases[0].ID != "kb-alpha" {
		t.Fatalf("bases = %+v", bases)
	}
	if bases := s.KnowledgeBases("proj-other"); len(bases) != 0 {
		t.Fatalf("other project leaked bases: %+v", bases)
	}

	if err := s.PutKnowledgeBinding(domain.KnowledgeBinding{ProjectID: "proj-alpha", BaseID: "kb-alpha", Permission: "submit"}); err != nil {
		t.Fatal(err)
	}
	bindings := s.KnowledgeBindings("proj-alpha")
	if len(bindings) != 1 || bindings[0].BaseID != "kb-alpha" || bindings[0].Permission != "submit" {
		t.Fatalf("bindings = %+v", bindings)
	}

	src := domain.KnowledgeSource{
		ID: "src-hosted", BaseID: "kb-alpha", Kind: "hosted", Name: "托管来源",
		HeadRevision: 3, Status: "ready", DocCount: 1,
	}
	if err := s.PutKnowledgeSource(src); err != nil {
		t.Fatal(err)
	}
	gotSrc, ok := s.KnowledgeSource("src-hosted")
	if !ok || gotSrc.BaseID != "kb-alpha" || gotSrc.Kind != "hosted" || gotSrc.HeadRevision != 3 {
		t.Fatalf("source = %+v ok=%v", gotSrc, ok)
	}
	if srcs := s.KnowledgeSources("kb-alpha"); len(srcs) != 1 || srcs[0].ID != "src-hosted" {
		t.Fatalf("sources = %+v", srcs)
	}
}

// TestBuiltinKnowledgeContentPlane 覆盖 S1 内容面：写正文与修订、替换索引块，
// 以及删除来源时没有外键可依赖。
func TestBuiltinKnowledgeContentPlane(t *testing.T) {
	s := builtinStore(t)
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{ID: "src-hosted", BaseID: "kb-alpha", Kind: "hosted"}); err != nil {
		t.Fatal(err)
	}
	doc := domain.KnowledgeDocument{
		SourceID: "src-hosted", Path: "docs/readme.md", Title: "说明",
		Body: "# 说明\n这是中文正文。", LineCount: 2, SHA256: "abc", Commit: "3", Revision: 3,
	}
	if err := s.PutKnowledgeDocument(doc); err != nil {
		t.Fatal(err)
	}
	gotDoc, ok := s.KnowledgeDocument("src-hosted", "docs/readme.md")
	if !ok || gotDoc.Body != doc.Body || gotDoc.Commit != "3" || gotDoc.Revision != 3 {
		t.Fatalf("document = %+v ok=%v", gotDoc, ok)
	}
	if docs := s.KnowledgeDocuments("src-hosted"); len(docs) != 1 {
		t.Fatalf("documents = %+v", docs)
	}

	if err := s.PutKnowledgeRevision(domain.KnowledgeRevision{
		ID: "rev-1", SourceID: "src-hosted", Path: "docs/readme.md", Revision: 1,
		Body: "# 说明\n第一版", Author: "owner",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeRevision(domain.KnowledgeRevision{
		ID: "rev-2", SourceID: "src-hosted", Path: "docs/readme.md", Revision: 2,
		Body: "# 说明\n第二版", Author: "owner",
	}); err != nil {
		t.Fatal(err)
	}
	revs := s.KnowledgeRevisions("src-hosted", "docs/readme.md")
	if len(revs) != 2 || revs[0].Revision != 1 || revs[1].Revision != 2 {
		t.Fatalf("revisions = %+v", revs)
	}

	chunks := []domain.KnowledgeChunk{
		{Idx: 0, Heading: "说明", Title: "说明", StartLine: 1, EndLine: 2, Commit: "3", Revision: 3, Text: "# 说明\n这是中文正文。"},
		{Idx: 1, Heading: "说明", Title: "说明", StartLine: 3, EndLine: 3, Commit: "3", Revision: 3, Text: "更多内容"},
	}
	if err := s.ReplaceKnowledgeChunks("src-hosted", "docs/readme.md", chunks); err != nil {
		t.Fatal(err)
	}
	if got := s.KnowledgeChunks("src-hosted", "docs/readme.md"); len(got) != 2 || got[0].Idx != 0 || got[1].Idx != 1 {
		t.Fatalf("chunks = %+v", got)
	}
	if err := s.ReplaceKnowledgeChunks("src-hosted", "docs/readme.md", chunks[:1]); err != nil {
		t.Fatal(err)
	}
	if got := s.KnowledgeChunks("src-hosted", "docs/readme.md"); len(got) != 1 {
		t.Fatalf("chunks were not replaced: %+v", got)
	}

	// 删除控制面来源不得靠外键联动，内容面仍可读。
	if err := s.DeleteKnowledgeSource("src-hosted"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.KnowledgeSource("src-hosted"); ok {
		t.Fatal("source was not deleted")
	}
	if _, ok := s.KnowledgeDocument("src-hosted", "docs/readme.md"); !ok {
		t.Fatal("content was removed by a foreign-key cascade")
	}
	for _, table := range []string{"knowledge_documents", "knowledge_revisions", "knowledge_chunks"} {
		assertNoForeignKeys(t, s.DB, table)
	}
}

func assertNoForeignKeys(t *testing.T, db *gorm.DB, table string) {
	t.Helper()
	var count int
	if err := db.Raw("SELECT COUNT(*) FROM pragma_foreign_key_list(?)", table).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("%s 不该有外键：内容面只用 source_id 字符串指向控制面", table)
	}
}

// TestKnowledgeContentDBReusesMainPool 覆盖 S1：空 DSN 与相同 DSN 走同一个连接池。
func TestKnowledgeContentDBReusesMainPool(t *testing.T) {
	s := builtinStore(t)
	main := s.DB
	mainDSN := "app:pw@tcp(127.0.0.1:3306)/appdb"
	for _, contentDSN := range []string{
		"", "   ", mainDSN, mainDSN + "?charset=utf8mb4&collation=utf8mb4_unicode_ci",
	} {
		db, separate, err := knowledgeContentDB(main, mainDSN, contentDSN)
		if err != nil {
			t.Fatal(err)
		}
		if separate || db != main {
			t.Fatalf("contentDSN=%q 应复用主库连接池：separate=%v", contentDSN, separate)
		}
	}
}

// TestContentPoolIsSeparate 覆盖 S1：只有不同 DSN 才会另开连接。
func TestContentPoolIsSeparate(t *testing.T) {
	main := "app:pw@tcp(127.0.0.1:3306)/appdb"
	if contentPoolIsSeparate(main, "") {
		t.Fatal("空 DSN 不该另开连接")
	}
	if contentPoolIsSeparate(main, main) {
		t.Fatal("相同 DSN 不该另开连接")
	}
	if !contentPoolIsSeparate(main, "app:pw@tcp(127.0.0.1:3307)/contentdb") {
		t.Fatal("不同 DSN 应另开连接")
	}
}

// TestBuiltinKnowledgeInstallWiring 覆盖 S11：compose 写明 ngram 参数，
// dev.sh 在种子 SQL 之前执行 026。
func TestBuiltinKnowledgeInstallWiring(t *testing.T) {
	compose, err := os.ReadFile("../../compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), "--ngram_token_size=2") {
		t.Fatal("compose.yaml 的 MySQL command 缺少 --ngram_token_size=2")
	}
	dev, err := os.ReadFile("../../scripts/dev.sh")
	if err != nil {
		t.Fatal(err)
	}
	migration := strings.Index(string(dev), "migrations/026_builtin_knowledge.sql")
	if migration < 0 {
		t.Fatal("dev.sh 未执行 026")
	}
	seed := strings.Index(string(dev), "migrations/seed/001_public_catalog.sql")
	if seed < 0 || migration > seed {
		t.Fatal("026 必须在种子 SQL 之前执行")
	}
}

func TestDeleteKnowledgeSourceContentRemovesAllContentRows(t *testing.T) {
	s := builtinStore(t)
	sourceID := "src-delete-content"
	if err := s.PutKnowledgeDocument(domain.KnowledgeDocument{
		SourceID: sourceID, Path: "a.md", Title: "A", Body: "# A\n", LineCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutKnowledgeRevision(domain.KnowledgeRevision{
		ID: "rev-delete", SourceID: sourceID, Path: "a.md", Revision: 1, Body: "# A\n",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceKnowledgeChunks(sourceID, "a.md", []domain.KnowledgeChunk{{
		SourceID: sourceID, Path: "a.md", Idx: 0, Title: "A", StartLine: 1, EndLine: 1, Text: "# A",
	}}); err != nil {
		t.Fatal(err)
	}
	other := domain.KnowledgeDocument{SourceID: "src-keep", Path: "keep.md", Title: "Keep", Body: "# Keep\n", LineCount: 1}
	if err := s.PutKnowledgeDocument(other); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteKnowledgeSourceContent(sourceID); err != nil {
		t.Fatal(err)
	}
	if docs := s.KnowledgeDocuments(sourceID); len(docs) != 0 {
		t.Fatalf("documents = %+v", docs)
	}
	if revs := s.KnowledgeRevisions(sourceID, "a.md"); len(revs) != 0 {
		t.Fatalf("revisions = %+v", revs)
	}
	if chunks := s.KnowledgeChunks(sourceID, "a.md"); len(chunks) != 0 {
		t.Fatalf("chunks = %+v", chunks)
	}
	if docs := s.KnowledgeDocuments("src-keep"); len(docs) != 1 {
		t.Fatalf("其它来源的正文被误删: %+v", docs)
	}
}
