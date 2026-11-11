package knowledge

import (
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
)

const (
	FreshnessCurrent = "current"
	FreshnessChanged = "changed"
	FreshnessDeleted = "deleted"
)

// CitationStore 是引用记录的持久化。Service 用类型断言取出，不扩大 Store。
type CitationStore interface {
	PutKnowledgeCitation(domain.KnowledgeCitation) error
	KnowledgeCitations(projectID string, limit int) []domain.KnowledgeCitation
	KnowledgeCitationCounts(projectID string, since time.Time) []domain.KnowledgeCitationCount
}

// Freshness 是一条出处相对来源当前正文的时效。
type Freshness struct {
	Citation string `json:"citation,omitempty"`
	Source   string `json:"source,omitempty"`
	Path     string `json:"path,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Status   string `json:"status"`
	Current  string `json:"current,omitempty"`
}

// CitedDocument 是统计窗口内被引用过的文档及次数。
type CitedDocument struct {
	SourceID string `json:"sourceId"`
	Path     string `json:"path"`
	BaseID   string `json:"baseId,omitempty"`
	Layer    string `json:"layer,omitempty"`
	Title    string `json:"title,omitempty"`
	Count    int    `json:"count"`
}

// UncitedDocument 是有正文、但统计窗口内没有引用的文档。
type UncitedDocument struct {
	SourceID string `json:"sourceId"`
	Path     string `json:"path"`
	BaseID   string `json:"baseId,omitempty"`
	Layer    string `json:"layer,omitempty"`
	Title    string `json:"title,omitempty"`
}

// CitationUsage 是一个项目的引用统计。Available 为 false 表示存储还没有引用表。
type CitationUsage struct {
	Available bool              `json:"available"`
	Cited     []CitedDocument   `json:"cited"`
	Uncited   []UncitedDocument `json:"uncited"`
}

// RecordCitations 尽力写入引用。存储不支持或项目为空时直接返回，不当成工具失败。
func (s *Service) RecordCitations(items []domain.KnowledgeCitation) error {
	if s == nil || s.store == nil || len(items) == 0 {
		return nil
	}
	store, ok := s.store.(CitationStore)
	if !ok {
		return nil
	}
	var first error
	for _, item := range items {
		if strings.TrimSpace(item.ProjectID) == "" || strings.TrimSpace(item.SourceID) == "" || strings.TrimSpace(item.Path) == "" {
			continue
		}
		if item.At.IsZero() {
			item.At = s.citationNow()
		}
		if err := store.PutKnowledgeCitation(item); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Citations 返回一个项目最近的引用。空项目返回空。
func (s *Service) Citations(projectID string, limit int) []domain.KnowledgeCitation {
	out := make([]domain.KnowledgeCitation, 0)
	if s == nil || s.store == nil || strings.TrimSpace(projectID) == "" {
		return out
	}
	store, ok := s.store.(CitationStore)
	if !ok {
		return out
	}
	return store.KnowledgeCitations(projectID, limit)
}

// CitationUsage 统计项目在时间窗内的引用次数，并列出有正文但没有引用的文档。
// 第二个返回值表示存储是否支持引用表；不支持时不要把空列表当成「从未命中」。
func (s *Service) CitationUsage(projectID string) (CitationUsage, bool) {
	out := CitationUsage{Cited: []CitedDocument{}, Uncited: []UncitedDocument{}}
	if s == nil || s.store == nil {
		return out, false
	}
	store, ok := s.store.(CitationStore)
	if !ok {
		return out, false
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return out, true
	}
	ms, err := s.managed()
	if err != nil {
		return out, true
	}
	since := s.citationNow().Add(-domain.KnowledgeCitationWindow)
	counts := store.KnowledgeCitationCounts(projectID, since)
	byDoc := map[string]int{}
	for _, item := range counts {
		byDoc[item.SourceID+"\n"+item.Path] = item.Count
	}
	seen := map[string]bool{}
	for _, base := range visibleBases(ms, projectID) {
		for _, src := range ms.KnowledgeSources(base.ID) {
			for _, doc := range ms.KnowledgeDocuments(src.ID) {
				if strings.TrimSpace(doc.Body) == "" || !strings.HasSuffix(strings.ToLower(doc.Path), ".md") {
					continue
				}
				key := src.ID + "\n" + doc.Path
				seen[key] = true
				if n := byDoc[key]; n > 0 {
					out.Cited = append(out.Cited, CitedDocument{
						SourceID: src.ID, Path: doc.Path, BaseID: base.ID, Layer: base.Layer, Title: doc.Title, Count: n,
					})
				} else {
					out.Uncited = append(out.Uncited, UncitedDocument{
						SourceID: src.ID, Path: doc.Path, BaseID: base.ID, Layer: base.Layer, Title: doc.Title,
					})
				}
			}
		}
	}
	for _, item := range counts {
		key := item.SourceID + "\n" + item.Path
		if seen[key] || item.Count <= 0 {
			continue
		}
		out.Cited = append(out.Cited, CitedDocument{SourceID: item.SourceID, Path: item.Path, Count: item.Count})
	}
	sort.SliceStable(out.Cited, func(i, j int) bool {
		if out.Cited[i].Count != out.Cited[j].Count {
			return out.Cited[i].Count > out.Cited[j].Count
		}
		if out.Cited[i].Path != out.Cited[j].Path {
			return out.Cited[i].Path < out.Cited[j].Path
		}
		return out.Cited[i].SourceID < out.Cited[j].SourceID
	})
	sort.SliceStable(out.Uncited, func(i, j int) bool {
		if out.Uncited[i].Path != out.Uncited[j].Path {
			return out.Uncited[i].Path < out.Uncited[j].Path
		}
		return out.Uncited[i].SourceID < out.Uncited[j].SourceID
	})
	out.Available = true
	return out, true
}

// JudgeFreshness 比对该出处的版本和来源里当前正文的版本。
// 来源不在项目授权内、或正文已不存在时记为 deleted，并且不返回当前版本。
func (s *Service) JudgeFreshness(projectID, source, path, cited string) Freshness {
	source = strings.TrimSpace(source)
	path = strings.TrimSpace(path)
	cited = strings.TrimSpace(cited)
	out := Freshness{Source: source, Path: path, Commit: cited, Status: FreshnessDeleted}
	if s == nil || source == "" || path == "" || !SourceAllowed(s.AllowedSources(projectID), source) {
		return out
	}
	ms, err := s.managed()
	if err != nil {
		return out
	}
	doc, ok := findDocument(ms, source, path)
	if !ok {
		return out
	}
	current := strings.TrimSpace(doc.Commit)
	out.Current = current
	if cited == "" || cited == current {
		out.Status = FreshnessCurrent
		return out
	}
	out.Status = FreshnessChanged
	return out
}

// JudgeCitations 批量判断出处时效，顺序与输入一致。无法解析的字符串记为 deleted。
func (s *Service) JudgeCitations(projectID string, citations []string) []Freshness {
	out := make([]Freshness, 0, len(citations))
	for _, raw := range citations {
		source, version, path, _, _, ok := ParseCitation(raw)
		if !ok {
			out = append(out, Freshness{Citation: strings.TrimSpace(raw), Status: FreshnessDeleted})
			continue
		}
		item := s.JudgeFreshness(projectID, source, path, version)
		item.Citation = strings.TrimSpace(raw)
		out = append(out, item)
	}
	return out
}

func (s *Service) citationNow() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

func findDocument(ms ManagedStore, source, path string) (domain.KnowledgeDocument, bool) {
	for _, doc := range ms.KnowledgeDocuments(source) {
		if doc.Path == path {
			return doc, true
		}
	}
	return domain.KnowledgeDocument{}, false
}

func citationFromHit(caller Caller, at time.Time, tool, source, path, commit, base, layer string, start, end int, chunk int64) domain.KnowledgeCitation {
	if end < start {
		end = start
	}
	return domain.KnowledgeCitation{
		At: at, ProjectID: caller.ProjectID, AgentID: caller.AgentID, UserID: caller.UserID,
		RunID: caller.RunID, SessionID: caller.SessionID, Tool: tool,
		SourceID: source, Path: path, Commit: commit, StartLine: start, EndLine: end,
		ChunkID: chunk, BaseID: base, Layer: layer,
	}
}

// CitationsForSearch 把一次检索的命中记成引用。
func CitationsForSearch(caller Caller, at time.Time, result SearchResult) []domain.KnowledgeCitation {
	out := make([]domain.KnowledgeCitation, 0, len(result.Results))
	for _, hit := range result.Results {
		if hit.Source == "" || hit.Path == "" {
			continue
		}
		out = append(out, citationFromHit(caller, at, ToolSearch, hit.Source, hit.Path, hit.Commit, hit.BaseID, hit.Layer, hit.StartLine, hit.EndLine, 0))
	}
	return out
}

// CitationsForDocument 把一次读原文记成一条引用。
func CitationsForDocument(caller Caller, at time.Time, doc Document) []domain.KnowledgeCitation {
	if doc.Source == "" || doc.Path == "" {
		return nil
	}
	return []domain.KnowledgeCitation{citationFromHit(caller, at, ToolFetch, doc.Source, doc.Path, doc.Commit, doc.BaseID, doc.Layer, doc.StartLine, doc.EndLine, 0)}
}

// CitationsForGrep 把精确匹配的每一行记成引用。
func CitationsForGrep(caller Caller, at time.Time, result GrepResult) []domain.KnowledgeCitation {
	out := make([]domain.KnowledgeCitation, 0, len(result.Hits))
	for _, hit := range result.Hits {
		if hit.Source == "" || hit.Path == "" {
			continue
		}
		out = append(out, citationFromHit(caller, at, ToolGrep, hit.Source, hit.Path, hit.Commit, hit.BaseID, hit.Layer, hit.Line, hit.Line, 0))
	}
	return out
}
