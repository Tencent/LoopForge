package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	"agentflow/internal/knowledge/engine/chunk"
	"agentflow/internal/knowledge/engine/frontmatter"
	"agentflow/internal/knowledge/engine/gitsync"
	"agentflow/internal/knowledge/hosting"
	"agentflow/internal/mcpclient"
)

// ErrExternalWriteNotImplemented 是对直接写入 Git 来源的明确拒绝：Git 来源
// 必须经审核队列发布，由 Engine.WriteGitDocument 提交、推送并创建合并请求。
var ErrExternalWriteNotImplemented = errors.New("Git 来源写回需经审核队列发布")

// ControlStore 是引擎需要的控制面（主库）能力，由 *repo.KnowledgeRepo 提供。
type ControlStore interface {
	KnowledgeSource(id string) (domain.KnowledgeSource, bool)
	KnowledgeSources(baseID string) []domain.KnowledgeSource
	PutKnowledgeSource(source domain.KnowledgeSource) error
}

// ContentStore 是引擎需要的内容面能力，由 *repo.KnowledgeContentRepo 提供。
type ContentStore interface {
	KnowledgeDocument(sourceID, path string) (domain.KnowledgeDocument, bool)
	KnowledgeDocuments(sourceID string) []domain.KnowledgeDocument
	WriteHostedDocument(doc domain.KnowledgeDocument, rev domain.KnowledgeRevision, chunks []domain.KnowledgeChunk) error
}

var revisionIDSeq atomic.Uint64

// Engine 实现 knowledge.Upstream 五个方法，是内置知识引擎的唯一适配器。
// 写回相关字段由 initWriteback 填默认值，测试可在同包内替换 Git/托管平台
// 调用，不改变 Upstream 契约。
type Engine struct {
	control ControlStore
	content ContentStore
	index   KeywordIndex
	now     func() time.Time
	newID   func() string

	writeCacheRoot   string
	writeLeaseTTL    time.Duration
	gitWrite         func(context.Context, string, string, string, gitsync.WriteOptions) (gitsync.WriteResult, error)
	gitMergeProbe    func(context.Context, string, string, string, string) (bool, error)
	hostingClientFor func(hosting.Repo, string) *hosting.Client
	// vectorDeindexer 是可选向量索引的去索引钩子。向量分支合并后由
	// VectorRuntime 装上；本分支没有向量运行时，保持 nil 即不执行。
	vectorDeindexer func(context.Context, string, string) error
}

var _ knowledge.Upstream = (*Engine)(nil)

// NewEngine 用控制面、内容面与关键词适配器装配引擎。关键词适配器是内部
// 替换点，可换成内存倒排等实现而不改 Upstream 和表。
func NewEngine(control ControlStore, content ContentStore, index KeywordIndex) *Engine {
	e := &Engine{
		control: control, content: content, index: index,
		now:   time.Now,
		newID: func() string { return fmt.Sprintf("kr-%d-%d", time.Now().UTC().UnixNano(), revisionIDSeq.Add(1)) },
	}
	e.initWriteback()
	return e
}

// Tools 返回固定的只读工具说明，不向 Agent 列出写工具。
func (e *Engine) Tools(context.Context) ([]mcpclient.Tool, error) {
	return readOnlyTools(), nil
}

// ListSources 读控制面来源，只输出工具 JSON 契约里的脱敏字段，不含
// Git 地址、凭据和本地缓存路径。
func (e *Engine) ListSources(context.Context) ([]knowledge.Source, error) {
	rows := e.control.KnowledgeSources("")
	out := make([]knowledge.Source, 0, len(rows))
	for _, src := range rows {
		out = append(out, knowledge.Source{
			ID: src.ID, Name: src.Name, Status: src.Status,
			IndexStatus: src.IndexStatus, DocCount: src.DocCount,
		})
	}
	return out, nil
}

// Search 在调用方已经收窄的来源集合里做关键词检索。空集合表示没有可查的
// 来源，直接返回空结果，不扩展到全部来源。命中还会再按来源过滤一次，保证
// 结果绝不越过调用方的授权范围。
func (e *Engine) Search(ctx context.Context, query string, sources []string) (knowledge.SearchResult, error) {
	query = strings.TrimSpace(query)
	result := knowledge.SearchResult{
		Query: query, Mode: "lexical", Degraded: false,
		Results: []knowledge.SearchHit{},
	}
	if query == "" {
		return result, nil
	}
	scope := cleanSources(sources)
	if len(scope) == 0 {
		return result, nil
	}
	hits, err := e.index.Search(ctx, query, scope, SearchOptions{Limit: DefaultSearchLimit})
	if err != nil {
		return result, err
	}
	allowed := make(map[string]bool, len(scope))
	for _, id := range scope {
		allowed[id] = true
	}
	for _, h := range hits {
		if !allowed[h.Source] {
			continue
		}
		result.Results = append(result.Results, knowledge.SearchHit{
			Source: h.Source, Commit: h.Commit, Path: h.Path,
			Title: h.Title, Heading: h.Heading,
			StartLine: h.StartLine, EndLine: h.EndLine,
			Snippet:  makeSnippet(h.Text, query),
			Score:    h.Score,
			Citation: citation(h.Source, h.Commit, h.Path, h.StartLine, h.EndLine),
		})
		if len(result.Results) >= DefaultSearchLimit {
			break
		}
	}
	return result, nil
}

// Fetch 读内容面正文并按行截取。Upstream 没有 commit 入参，因此这里只提供
// 当前已索引版本；切片不读 Git、不读历史版本，历史版本的核对在工具边界
// 由调用方用返回的出处版本处理。
func (e *Engine) Fetch(_ context.Context, source, path string, start, end int) (knowledge.Document, error) {
	source, path = strings.TrimSpace(source), strings.TrimSpace(path)
	if source == "" || path == "" {
		return knowledge.Document{}, errors.New("source 和 path 必填")
	}
	doc, ok := e.content.KnowledgeDocument(source, path)
	if !ok {
		return knowledge.Document{}, fmt.Errorf("文档不存在：%s:%s", source, path)
	}
	lines := splitBody(doc.Body)
	first, last, err := lineRange(len(lines), start, end)
	if err != nil {
		return knowledge.Document{}, err
	}
	out := knowledge.Document{
		Source: source, Commit: doc.Commit, Path: path,
		StartLine: first, EndLine: last, LineCount: len(lines),
		Lines:    make([]knowledge.Line, 0, last-first+1),
		Citation: citation(source, doc.Commit, path, first, last),
	}
	for number := first; number <= last; number++ {
		out.Lines = append(out.Lines, knowledge.Line{Number: number, Text: lines[number-1]})
	}
	return out, nil
}

// WriteDocument 仅对托管来源生效：校验路径与 frontmatter，在内容库事务里
// 写正文、追加修订、替换该文档的索引块，再更新来源的修订号与篇数。
// Git 来源返回明确的未实现错误，不推送、不建提交。
func (e *Engine) WriteDocument(_ context.Context, source, path, content string) (knowledge.WriteResult, error) {
	source = strings.TrimSpace(source)
	path = strings.TrimSpace(path)
	docPath, ok := knowledge.NormalizeDocPath(path)
	if !ok {
		return knowledge.WriteResult{}, errors.New("写入路径必须是仓库内的 .md 相对路径")
	}
	src, found := e.control.KnowledgeSource(source)
	if !found {
		return knowledge.WriteResult{}, fmt.Errorf("来源 %s 不存在", source)
	}
	if src.Kind != "hosted" {
		return knowledge.WriteResult{}, ErrExternalWriteNotImplemented
	}

	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if err := frontmatter.Validate([]byte(normalized)); err != nil {
		return knowledge.WriteResult{}, fmt.Errorf("文档校验失败: %w", err)
	}
	bodyText := normalized
	lines := splitBody(bodyText)
	title := chunk.Title(bodyText)

	// 修订号按文档计数：新文档从 1 起，同一路径覆盖时只递增这篇文档的号。
	revision := int64(1)
	if existing, ok := e.content.KnowledgeDocument(source, docPath); ok && existing.Revision > 0 {
		revision = existing.Revision + 1
	}
	version := strconv.FormatInt(revision, 10)
	now := e.now().UTC()

	doc := domain.KnowledgeDocument{
		SourceID: source, Path: docPath, Title: title, Body: bodyText,
		LineCount: len(lines), SHA256: sha256Hex(bodyText),
		Commit: version, Revision: revision, UpdatedAt: now,
	}
	rev := domain.KnowledgeRevision{
		ID: e.newID(), SourceID: source, Path: docPath,
		Revision: revision, Body: bodyText, CreatedAt: now,
	}
	chunks := buildChunks(source, docPath, title, version, revision, lines)

	if err := e.content.WriteHostedDocument(doc, rev, chunks); err != nil {
		return knowledge.WriteResult{}, err
	}
	src.HeadRevision = revision
	src.DocCount = len(e.content.KnowledgeDocuments(source))
	if err := e.control.PutKnowledgeSource(src); err != nil {
		return knowledge.WriteResult{}, err
	}
	return knowledge.WriteResult{Source: source, Path: docPath, Commit: version}, nil
}

// SetVectorDeindexer installs the optional vector-index hook used by
// retraction. It is kept as a narrow callback so retraction never imports or
// changes the retrieval/fusion path.
func (e *Engine) SetVectorDeindexer(hook func(context.Context, string, string) error) {
	e.vectorDeindexer = hook
}

// RetractDocument removes one published hosted document from the content
// plane: body, revisions and chunks. The keyword index disappears with the
// chunks; an optional vector runtime is asked to remove its points through the
// same hook used for incremental indexing. Git sources are rejected because
// deleting the platform copy would not delete the repository fact source.
func (e *Engine) RetractDocument(ctx context.Context, source, path string) error {
	source = strings.TrimSpace(source)
	docPath, ok := knowledge.NormalizeDocPath(path)
	if !ok {
		return errors.New("撤回路径必须是仓库内的 .md 相对路径")
	}
	src, found := e.control.KnowledgeSource(source)
	if !found {
		return fmt.Errorf("来源 %s 不存在", source)
	}
	if src.Kind != "hosted" {
		return errors.New("Git 来源的知识请通过删除合并请求撤回")
	}
	deleter, ok := e.content.(interface {
		DeleteKnowledgeDocument(sourceID, path string) error
	})
	if !ok {
		return errors.New("知识库内容存储不支持撤回")
	}
	if err := deleter.DeleteKnowledgeDocument(source, docPath); err != nil {
		return err
	}
	src.DocCount = len(e.content.KnowledgeDocuments(source))
	if err := e.control.PutKnowledgeSource(src); err != nil {
		return err
	}
	if e.vectorDeindexer != nil {
		if err := e.vectorDeindexer(ctx, source, docPath); err != nil {
			// Vectors are a cache. The rows are already gone; report the failure
			// so the caller can retry the deindex without reviving the document.
			return fmt.Errorf("删除向量索引失败：%w", err)
		}
	}
	return nil
}

// buildChunks 从正文行切出索引块。frontmatter 行被置空但保留行数，使块的
// 行号与 Fetch 读到的正文行号一致，同时 frontmatter 里的元数据词不进入
// 可检索文本（只用于标题/元数据）。
func buildChunks(sourceID, path, title, version string, revision int64, lines []string) []domain.KnowledgeChunk {
	searchLines := blankFrontmatter(lines)
	blocks, _ := chunk.Split(strings.Join(searchLines, "\n"))
	out := make([]domain.KnowledgeChunk, 0, len(blocks))
	for i, b := range blocks {
		out = append(out, domain.KnowledgeChunk{
			SourceID: sourceID, Path: path, Idx: i,
			Heading: b.Heading, Title: title,
			StartLine: b.StartLine, EndLine: b.EndLine,
			Commit: version, Revision: revision, Text: b.Text,
		})
	}
	return out
}

// blankFrontmatter 把开头的 frontmatter 块（含两侧 ---）置空，行数不变。
// 没有 frontmatter 时原样返回。
func blankFrontmatter(lines []string) []string {
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return lines
	}
	out := append([]string(nil), lines...)
	for i := 1; i < len(out); i++ {
		if strings.TrimSpace(out[i]) == "---" {
			for j := 0; j <= i; j++ {
				out[j] = ""
			}
			return out
		}
	}
	return lines
}

// splitBody 把正文切成行，去掉末尾单个空行，使最后一行是真实内容行。
// Fetch、行号、块行号、line_count 全部用这一份行数组，保持一致。
func splitBody(body string) []string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(body, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func lineRange(lineCount, requestedStart, requestedEnd int) (int, int, error) {
	if lineCount == 0 {
		return 0, 0, errors.New("文档为空")
	}
	start := requestedStart
	if start == 0 {
		start = 1
	}
	end := requestedEnd
	if end == 0 || end > lineCount {
		end = lineCount
	}
	if start < 1 || start > lineCount || end < start {
		return 0, 0, fmt.Errorf("行号范围无效：文档共 %d 行", lineCount)
	}
	return start, end, nil
}

func cleanSources(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, id := range in {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func citation(source, version, path string, start, end int) string {
	ref := fmt.Sprintf("#L%d", start)
	if end > start {
		ref += fmt.Sprintf("-L%d", end)
	}
	return source + "@" + version + ":" + path + ref
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// makeSnippet 取含首个查询词的 rune 窗口（前 40 后 80），首尾补省略号。
func makeSnippet(text, query string) string {
	runes := []rune(strings.TrimSpace(text))
	lowerAll := []rune(strings.ToLower(string(runes)))
	for _, term := range strings.Fields(strings.ToLower(query)) {
		tr := []rune(term)
		for i := 0; i+len(tr) <= len(lowerAll); i++ {
			if string(lowerAll[i:i+len(tr)]) != term {
				continue
			}
			start := i - 40
			if start < 0 {
				start = 0
			}
			end := i + 80
			if end > len(runes) {
				end = len(runes)
			}
			snip := string(runes[start:end])
			if start > 0 {
				snip = "…" + snip
			}
			if end < len(runes) {
				snip += "…"
			}
			return snip
		}
	}
	if len(runes) > 120 {
		return string(runes[:120]) + "…"
	}
	return string(runes)
}
