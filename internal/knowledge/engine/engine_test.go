package engine

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
)

// ---- fakes ----

type fakeControl struct {
	sources map[string]domain.KnowledgeSource
	puts    []domain.KnowledgeSource
}

func newFakeControl() *fakeControl {
	return &fakeControl{sources: map[string]domain.KnowledgeSource{}}
}

func (f *fakeControl) KnowledgeSource(id string) (domain.KnowledgeSource, bool) {
	s, ok := f.sources[id]
	return s, ok
}
func (f *fakeControl) KnowledgeSources(string) []domain.KnowledgeSource {
	out := make([]domain.KnowledgeSource, 0, len(f.sources))
	for _, s := range f.sources {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (f *fakeControl) PutKnowledgeSource(s domain.KnowledgeSource) error {
	f.sources[s.ID] = s
	f.puts = append(f.puts, s)
	return nil
}

func docKey(source, path string) string { return source + "\x00" + path }

type fakeContent struct {
	docs   map[string]domain.KnowledgeDocument
	revs   []domain.KnowledgeRevision
	chunks map[string][]domain.KnowledgeChunk
	writes int
}

func newFakeContent() *fakeContent {
	return &fakeContent{
		docs:   map[string]domain.KnowledgeDocument{},
		chunks: map[string][]domain.KnowledgeChunk{},
	}
}

func (f *fakeContent) KnowledgeDocument(source, path string) (domain.KnowledgeDocument, bool) {
	doc, ok := f.docs[docKey(source, path)]
	return doc, ok
}
func (f *fakeContent) KnowledgeDocuments(source string) []domain.KnowledgeDocument {
	var out []domain.KnowledgeDocument
	for key, doc := range f.docs {
		if strings.HasPrefix(key, source+"\x00") {
			out = append(out, doc)
		}
	}
	return out
}
func (f *fakeContent) WriteHostedDocument(doc domain.KnowledgeDocument, rev domain.KnowledgeRevision, chunks []domain.KnowledgeChunk) error {
	key := docKey(doc.SourceID, doc.Path)
	f.docs[key] = doc
	f.chunks[key] = append([]domain.KnowledgeChunk(nil), chunks...)
	f.revs = append(f.revs, rev)
	f.writes++
	return nil
}

type fakeIndex struct {
	hits    []Hit
	err     error
	query   string
	sources []string
	opts    SearchOptions
	calls   int
}

func (f *fakeIndex) Search(_ context.Context, query string, sources []string, opts SearchOptions) ([]Hit, error) {
	f.calls++
	f.query = query
	f.sources = append([]string(nil), sources...)
	f.opts = opts
	return f.hits, f.err
}

// ---- helpers ----

func newTestEngine(control *fakeControl, content *fakeContent, index *fakeIndex) *Engine {
	e := NewEngine(control, content, index)
	e.now = func() time.Time { return time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC) }
	seq := 0
	e.newID = func() string { seq++; return fmt.Sprintf("rev-%d", seq) }
	return e
}

// testMarkdown 含一段显式 frontmatter：keywords 里的「狻猊标记」只应作为
// 元数据存在，不进入索引块的可检索文本；正文里的「麒麟」应可命中。
const testMarkdown = `---
title: "部署手册"
category: "guides"
keywords: "狻猊标记"
---

# 部署手册

麒麟服务器需要配置防火墙规则。
这里是二字符召回词。
`

func assertNoWrite(t *testing.T, c *fakeContent) {
	t.Helper()
	if c.writes != 0 || len(c.revs) != 0 || len(c.docs) != 0 {
		t.Fatalf("本用例不应产生内容面写入: %+v", c)
	}
}

// ---- tests ----

func TestToolsAreReadOnly(t *testing.T) {
	e := newTestEngine(newFakeControl(), newFakeContent(), &fakeIndex{})
	tools, err := e.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != len(knowledge.ReadOnlyTools) {
		t.Fatalf("工具数量 = %d, want %d", len(tools), len(knowledge.ReadOnlyTools))
	}
	for _, tool := range tools {
		if !knowledge.IsReadOnlyTool(tool.Name) {
			t.Fatalf("列出了非只读工具 %q", tool.Name)
		}
		if tool.Name == knowledge.ToolWrite {
			t.Fatal("写工具不应出现在 Tools")
		}
		if len(tool.InputSchema) == 0 {
			t.Fatalf("工具 %q 缺少 inputSchema", tool.Name)
		}
	}
}

func TestListSourcesIsDesensitized(t *testing.T) {
	control := newFakeControl()
	control.sources["git-src"] = domain.KnowledgeSource{
		ID: "git-src", BaseID: "base1", Kind: "git", Name: "公开样例",
		GitURL: "https://example.invalid/repo.git", Branch: "main", PathPrefix: "docs/",
		Status: "ready", IndexStatus: "ready", DocCount: 3,
		LeaseOwner: "worker-1", LastError: "some internal detail", HeadCommit: "abcdef",
	}
	control.sources["hosted-src"] = domain.KnowledgeSource{
		ID: "hosted-src", BaseID: "base1", Kind: "hosted", Name: "项目托管库",
		Status: "ready", IndexStatus: "ready", DocCount: 2, HeadRevision: 5,
	}
	e := newTestEngine(control, newFakeContent(), &fakeIndex{})
	list, err := e.ListSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("来源数 = %d: %+v", len(list), list)
	}
	for _, src := range list {
		// Source 只有这几个字段，结构上无法携带 Git 地址、凭据或本地路径。
		if src.ID == "" || src.Name == "" {
			t.Fatalf("来源字段缺失: %+v", src)
		}
	}
	if list[0].ID != "git-src" || list[0].DocCount != 3 || list[1].ID != "hosted-src" || list[1].DocCount != 2 {
		t.Fatalf("来源映射错误: %+v", list)
	}
}

func TestSearchNarrowsToScope(t *testing.T) {
	index := &fakeIndex{hits: []Hit{
		{Source: "allowed", Commit: "1", Path: "a.md", StartLine: 1, EndLine: 3, Text: "麒麟内容", Score: 3},
		{Source: "other", Commit: "1", Path: "b.md", StartLine: 1, EndLine: 2, Text: "别库内容", Score: 2},
		{Source: "secret", Commit: "1", Path: "c.md", StartLine: 1, EndLine: 2, Text: "受限内容", Score: 1},
	}}
	e := newTestEngine(newFakeControl(), newFakeContent(), index)

	got, err := e.Search(context.Background(), "  麒麟  ", []string{" allowed ", "allowed"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(index.sources, []string{"allowed"}) {
		t.Fatalf("传给适配器的来源应被收窄且去重: %v", index.sources)
	}
	if index.opts.Limit != DefaultSearchLimit || index.query != "麒麟" {
		t.Fatalf("查询参数错误: query=%q opts=%+v", index.query, index.opts)
	}
	if len(got.Results) != 1 || got.Results[0].Source != "allowed" {
		t.Fatalf("结果越过了授权范围: %+v", got.Results)
	}
	if got.Mode != "lexical" || got.Degraded {
		t.Fatalf("mode/degraded 错误: %+v", got)
	}
	wantCitation := "allowed@1:a.md#L1-L3"
	if got.Results[0].Citation != wantCitation || got.Results[0].Score != 3 {
		t.Fatalf("出处/分数错误: %+v", got.Results[0])
	}

	// 空来源集合表示没有可查来源，结果为空且不调用适配器。
	index.calls = 0
	empty, err := e.Search(context.Background(), "麒麟", nil)
	if err != nil || len(empty.Results) != 0 || index.calls != 0 {
		t.Fatalf("空来源应返回空结果且不查适配器: %+v calls=%d err=%v", empty, index.calls, err)
	}
}

func TestSearchCapsAtDefaultLimit(t *testing.T) {
	var hits []Hit
	for i := 0; i < MaxSearchLimit+5; i++ {
		hits = append(hits, Hit{Source: "allowed", Commit: "1", Path: "a.md", Text: "麒麟", Score: float64(60 - i)})
	}
	index := &fakeIndex{hits: hits}
	e := newTestEngine(newFakeControl(), newFakeContent(), index)
	got, err := e.Search(context.Background(), "麒麟", []string{"allowed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Results) != DefaultSearchLimit {
		t.Fatalf("默认条数应为 %d, got %d", DefaultSearchLimit, len(got.Results))
	}
}

func TestFetchReadsLines(t *testing.T) {
	content := newFakeContent()
	body := "line one\nline two\nline three\nline four"
	content.docs[docKey("src", "a.md")] = domain.KnowledgeDocument{
		SourceID: "src", Path: "a.md", Body: body, Commit: "7", LineCount: 4,
	}
	e := newTestEngine(newFakeControl(), content, &fakeIndex{})

	doc, err := e.Fetch(context.Background(), " src ", " a.md ", 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Source != "src" || doc.Path != "a.md" || doc.Commit != "7" || doc.LineCount != 4 {
		t.Fatalf("文档头错误: %+v", doc)
	}
	if doc.StartLine != 2 || doc.EndLine != 3 {
		t.Fatalf("行范围错误: %+v", doc)
	}
	wantLines := []knowledge.Line{{Number: 2, Text: "line two"}, {Number: 3, Text: "line three"}}
	if !reflect.DeepEqual(doc.Lines, wantLines) {
		t.Fatalf("按行读取错误: %+v", doc.Lines)
	}
	if doc.Citation != "src@7:a.md#L2-L3" {
		t.Fatalf("出处错误: %q", doc.Citation)
	}

	full, err := e.Fetch(context.Background(), "src", "a.md", 0, 0)
	if err != nil || len(full.Lines) != 4 || full.StartLine != 1 || full.EndLine != 4 {
		t.Fatalf("默认应读全文: %+v err=%v", full, err)
	}
	if full.Citation != "src@7:a.md#L1-L4" {
		t.Fatalf("全文出处错误: %q", full.Citation)
	}
	if _, err := e.Fetch(context.Background(), "src", "a.md", 5, 9); err == nil {
		t.Fatal("越界行号应报错")
	}
	if _, err := e.Fetch(context.Background(), "src", "missing.md", 1, 2); err == nil {
		t.Fatal("不存在的文档应报错")
	}
}

func TestWriteDocumentHosted(t *testing.T) {
	control := newFakeControl()
	control.sources["src"] = domain.KnowledgeSource{ID: "src", BaseID: "base1", Kind: "hosted", Name: "托管库"}
	content := newFakeContent()
	e := newTestEngine(control, content, &fakeIndex{})

	result, err := e.WriteDocument(context.Background(), " src ", " guides/a.md ", testMarkdown)
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "src" || result.Path != "guides/a.md" || result.Commit != "1" {
		t.Fatalf("写入结果错误: %+v", result)
	}
	key := docKey("src", "guides/a.md")
	doc := content.docs[key]
	if doc.Body != testMarkdown {
		t.Fatalf("用户写的 frontmatter 与正文应原样落库: %q", doc.Body)
	}
	if !strings.HasPrefix(doc.Body, "---\n") || !strings.Contains(doc.Body, "麒麟服务器") {
		t.Fatalf("正文（含 frontmatter）未落库: %q", doc.Body)
	}
	if doc.Commit != "1" || doc.Revision != 1 || doc.Title != "部署手册" || doc.SHA256 == "" {
		t.Fatalf("文档元数据错误: %+v", doc)
	}
	if doc.LineCount != len(splitBody(doc.Body)) {
		t.Fatalf("行数不一致: %+v", doc)
	}
	if len(content.revs) != 1 || content.revs[0].Revision != 1 || content.revs[0].Body != doc.Body {
		t.Fatalf("修订未追加: %+v", content.revs)
	}
	chunks := content.chunks[key]
	if len(chunks) == 0 {
		t.Fatal("未重建索引块")
	}
	var bodyText strings.Builder
	for _, ch := range chunks {
		bodyText.WriteString(ch.Text)
		if strings.Contains(ch.Text, "狻猊标记") {
			t.Fatalf("frontmatter 元数据词进入了可检索文本: %q", ch.Text)
		}
		if ch.Commit != "1" || ch.Revision != 1 || ch.Title != "部署手册" {
			t.Fatalf("索引块出处/元数据错误: %+v", ch)
		}
	}
	if !strings.Contains(bodyText.String(), "麒麟") {
		t.Fatalf("正文词未进入索引块: %q", bodyText.String())
	}
	saved := control.sources["src"]
	if saved.HeadRevision != 1 || saved.DocCount != 1 {
		t.Fatalf("来源修订号/篇数未更新: %+v", saved)
	}

	// 写入后 Fetch 应读到同一份正文，出处版本是修订号。
	fetched, err := e.Fetch(context.Background(), "src", "guides/a.md", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.Commit != "1" || fetched.LineCount != doc.LineCount {
		t.Fatalf("Fetch 与写入不一致: %+v", fetched)
	}
}

func TestWriteDocumentKeepsPlainMarkdownAndCountsRevisionPerDocument(t *testing.T) {
	control := newFakeControl()
	control.sources["src"] = domain.KnowledgeSource{ID: "src", Kind: "hosted"}
	content := newFakeContent()
	e := newTestEngine(control, content, &fakeIndex{})

	a := "# 第一篇\n\n这是用户写的第一行。\n"
	b := "# 第二篇\n\n另一篇文档。\n"
	first, err := e.WriteDocument(context.Background(), "src", "guides/a.md", a)
	if err != nil {
		t.Fatal(err)
	}
	if first.Commit != "1" {
		t.Fatalf("新文档应从修订 1 起: %+v", first)
	}
	second, err := e.WriteDocument(context.Background(), "src", "guides/b.md", b)
	if err != nil {
		t.Fatal(err)
	}
	if second.Commit != "1" {
		t.Fatalf("第二篇新文档也应从修订 1 起，而不是全库递增: %+v", second)
	}

	docA := content.docs[docKey("src", "guides/a.md")]
	if docA.Body != a {
		t.Fatalf("不应自动插入 frontmatter: %q", docA.Body)
	}
	if docA.Revision != 1 || docA.LineCount != 3 {
		t.Fatalf("文档元数据错误: %+v", docA)
	}
	fetched, err := e.Fetch(context.Background(), "src", "guides/a.md", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(fetched.Lines) != 1 || fetched.Lines[0].Text != "# 第一篇" || fetched.Lines[0].Number != 1 {
		t.Fatalf("用户写的第一行应仍是第 1 行: %+v", fetched.Lines)
	}
	if fetched.Citation != "src@1:guides/a.md#L1" {
		t.Fatalf("出处里的版本应为文档修订号: %q", fetched.Citation)
	}

	third, err := e.WriteDocument(context.Background(), "src", "guides/a.md", "# 第一篇\n\n覆盖后的正文。\n")
	if err != nil {
		t.Fatal(err)
	}
	if third.Commit != "2" {
		t.Fatalf("同一文档覆盖后应为修订 2: %+v", third)
	}
	docA = content.docs[docKey("src", "guides/a.md")]
	if docA.Revision != 2 || strings.Contains(docA.Body, "这是用户写的第一行") {
		t.Fatalf("覆盖后正文/修订错误: %+v", docA)
	}
	for _, ch := range content.chunks[docKey("src", "guides/a.md")] {
		if ch.Commit != "2" || ch.Revision != 2 {
			t.Fatalf("覆盖后索引块仍带旧修订: %+v", ch)
		}
	}
	if control.sources["src"].HeadRevision != 2 || control.sources["src"].DocCount != 2 {
		t.Fatalf("来源应记录最近修订与篇数: %+v", control.sources["src"])
	}
}

func TestWriteDocumentOverwrite(t *testing.T) {
	control := newFakeControl()
	control.sources["src"] = domain.KnowledgeSource{ID: "src", Kind: "hosted"}
	content := newFakeContent()
	e := newTestEngine(control, content, &fakeIndex{})

	if _, err := e.WriteDocument(context.Background(), "src", "a.md", testMarkdown); err != nil {
		t.Fatal(err)
	}
	newBody := `---
title: "部署手册"
category: "guides"
keywords: "狻猊标记"
---

# 部署手册

貔貅服务器需要配置网关策略。
`
	if _, err := e.WriteDocument(context.Background(), "src", "a.md", newBody); err != nil {
		t.Fatal(err)
	}
	key := docKey("src", "a.md")
	doc := content.docs[key]
	if doc.Commit != "2" || doc.Revision != 2 || !strings.Contains(doc.Body, "貔貅") || strings.Contains(doc.Body, "麒麟") {
		t.Fatalf("覆盖后正文/版本错误: %+v", doc)
	}
	if len(content.revs) != 2 || content.revs[0].Revision != 1 || content.revs[1].Revision != 2 {
		t.Fatalf("应追加两条修订: %+v", content.revs)
	}
	chunks := content.chunks[key]
	var rebuilt strings.Builder
	for _, ch := range chunks {
		rebuilt.WriteString(ch.Text)
	}
	if strings.Contains(rebuilt.String(), "麒麟") || !strings.Contains(rebuilt.String(), "貔貅") {
		t.Fatalf("覆盖后索引块未重建: %q", rebuilt.String())
	}
	if control.sources["src"].HeadRevision != 2 {
		t.Fatal("来源修订号未递增")
	}
}

func TestWriteDocumentGitUnimplemented(t *testing.T) {
	control := newFakeControl()
	control.sources["remote"] = domain.KnowledgeSource{ID: "remote", Kind: "git", GitURL: "https://example.invalid/r.git"}
	content := newFakeContent()
	e := newTestEngine(control, content, &fakeIndex{})

	_, err := e.WriteDocument(context.Background(), "remote", "a.md", testMarkdown)
	if err == nil || !strings.Contains(err.Error(), "未实现") {
		t.Fatalf("Git 来源应返回未实现: %v", err)
	}
	assertNoWrite(t, content)
}

func TestWriteDocumentRejectsInvalidInput(t *testing.T) {
	control := newFakeControl()
	control.sources["src"] = domain.KnowledgeSource{ID: "src", Kind: "hosted"}

	t.Run("path", func(t *testing.T) {
		content := newFakeContent()
		e := newTestEngine(control, content, &fakeIndex{})
		for _, bad := range []string{"notes.txt", "../secret.md", "guides/../secret.md", "/etc/passwd.md", `guides\a.md`} {
			if _, err := e.WriteDocument(context.Background(), "src", bad, testMarkdown); err == nil {
				t.Fatalf("越界或非 .md 路径应被拒绝: %q", bad)
			}
		}
		assertNoWrite(t, content)
	})
	t.Run("unknown source", func(t *testing.T) {
		content := newFakeContent()
		e := newTestEngine(control, content, &fakeIndex{})
		if _, err := e.WriteDocument(context.Background(), "nope", "a.md", testMarkdown); err == nil {
			t.Fatal("未知来源应被拒绝")
		}
		assertNoWrite(t, content)
	})
	t.Run("bad frontmatter", func(t *testing.T) {
		content := newFakeContent()
		e := newTestEngine(control, content, &fakeIndex{})
		bad := "---\ntitle: \"\"\n---\n正文\n"
		if _, err := e.WriteDocument(context.Background(), "src", "a.md", bad); err == nil {
			t.Fatal("非法 frontmatter 应被拒绝")
		}
		assertNoWrite(t, content)
	})
}
