package knowledge

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

const (
	// GrepPatternMaxRunes 限制模式长度，避免超长正则占用匹配时间。
	GrepPatternMaxRunes = 128
	// GrepHitLimit 是一次精确匹配返回的命中上限。
	GrepHitLimit = 50
	// GrepTimeout 是一次精确匹配的执行时间上限。
	GrepTimeout = 2 * time.Second
	// GrepScanByteLimit 限制一次精确匹配加载和扫描的正文总字节数，避免
	// 大知识库把请求拖到超时之后仍占用内存。
	GrepScanByteLimit = 8 << 20
)

// grepNow 供测试把时钟往前拨，生产使用真实时间。
var grepNow = time.Now

// GrepQuery 是一次 kb_grep。Regex 为 false 时 Pattern 按字面量匹配。
type GrepQuery struct {
	Pattern string
	Regex   bool
	Sources []string
}

// GrepHit 是一行命中。行号与出处的起止行相同，方便再交给 kb_fetch。
type GrepHit struct {
	BaseOrigin
	Source    string `json:"source"`
	Commit    string `json:"commit"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Text      string `json:"text"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Citation  string `json:"citation"`
	URL       string `json:"url,omitempty"`
	Revision  int64  `json:"revision,omitempty"`
	Version   string `json:"version,omitempty"`
}

// GrepResult 是精确匹配的工具结果。Truncated 表示因为条数上限提前停止。
type GrepResult struct {
	Pattern   string    `json:"pattern"`
	Regex     bool      `json:"regex"`
	Truncated bool      `json:"truncated,omitempty"`
	Hits      []GrepHit `json:"hits"`
}

type grepDoc struct {
	Source string
	Commit string
	Path   string
	Body   string
}

// Grep 在当前项目可见来源的 MySQL 正文上做精确匹配，不读 Git。
// 空的授权列表得到空结果；点名未授权来源则拒绝。
func (s *Service) Grep(ctx context.Context, projectID string, q GrepQuery) (GrepResult, error) {
	pattern := strings.TrimSpace(q.Pattern)
	out := GrepResult{Pattern: pattern, Regex: q.Regex, Hits: []GrepHit{}}
	if pattern == "" {
		return out, apperr.New(apperr.Invalid, "请输入要匹配的字面量或正则")
	}
	if utf8.RuneCountInString(pattern) > GrepPatternMaxRunes {
		return out, apperr.New(apperr.Invalid, fmt.Sprintf("匹配模式最长 %d 个字符", GrepPatternMaxRunes))
	}
	scoped, err := ScopeSources(s.AllowedSources(projectID), q.Sources)
	if err != nil {
		return out, apperr.New(apperr.Invalid, err.Error())
	}
	if len(scoped) == 0 || s == nil || s.store == nil {
		return out, nil
	}
	match, err := grepMatcher(pattern, q.Regex)
	if err != nil {
		return out, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	started := grepNow()
	expired := func() bool {
		return grepNow().Sub(started) > GrepTimeout
	}
	const loadBatch = 100
	scanned := 0
	hits := make([]GrepHit, 0)
	truncated := false
	for _, source := range scoped {
		after := ""
		for {
			if err := grepStopped(ctx, expired); err != nil {
				out.Hits = hits
				return out, err
			}
			list := s.grepDocumentPage(source, after, loadBatch)
			if len(list) == 0 {
				break
			}
			docs := make([]grepDoc, 0, len(list))
			for _, doc := range list {
				if err := grepStopped(ctx, expired); err != nil {
					out.Hits = hits
					return out, err
				}
				if scanned+len(doc.Body) > GrepScanByteLimit {
					out.Hits = hits
					return out, apperr.New(apperr.Unavailable, "精确匹配扫描内容超过上限，请缩小来源范围")
				}
				scanned += len(doc.Body)
				docs = append(docs, grepDoc{Source: source, Commit: doc.Commit, Path: doc.Path, Body: doc.Body})
			}
			remaining := GrepHitLimit - len(hits)
			if remaining <= 0 {
				truncated = true
				break
			}
			batchHits, batchTruncated, err := grepBodies(ctx, docs, match, remaining, expired)
			hits = append(hits, batchHits...)
			if err != nil {
				out.Hits = hits
				out.Truncated = true
				return out, err
			}
			if batchTruncated {
				truncated = true
				break
			}
			after = list[len(list)-1].Path
			if len(list) < loadBatch {
				break
			}
		}
		if truncated {
			break
		}
	}
	for i := range hits {
		hits[i].BaseOrigin = s.sourceOrigin(hits[i].Source)
		hits[i].URL = s.SourceFileURL(hits[i].Source, hits[i].Commit, hits[i].Path, hits[i].Line, hits[i].Line)
	}
	out.Hits = hits
	s.annotateGrepVersions(&out)
	out.Truncated = truncated
	return out, nil
}

// grepDocumentPage prefers the content-plane pager so large sources are read in
// bounded pages. Test doubles and narrow stores keep the whole-list fallback.
func (s *Service) grepDocumentPage(source, after string, limit int) []domain.KnowledgeDocument {
	if pager, ok := s.store.(interface {
		KnowledgeDocumentPage(string, string, int) []domain.KnowledgeDocument
	}); ok {
		return pager.KnowledgeDocumentPage(source, after, limit)
	}
	list := append([]domain.KnowledgeDocument(nil), s.store.KnowledgeDocuments(source)...)
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
	start := sort.Search(len(list), func(i int) bool { return list[i].Path > after })
	if start >= len(list) {
		return nil
	}
	end := start + limit
	if end > len(list) {
		end = len(list)
	}
	return list[start:end]
}

func grepMatcher(pattern string, regex bool) (func(string) bool, error) {
	if !regex {
		return func(line string) bool { return strings.Contains(line, pattern) }, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, apperr.New(apperr.Invalid, "正则无效："+err.Error())
	}
	return re.MatchString, nil
}

// grepStopped 在加载和逐行匹配两个阶段共用同一套取消与超时判断。
func grepStopped(ctx context.Context, expired func() bool) error {
	if (ctx != nil && ctx.Err() != nil) || (expired != nil && expired()) {
		return apperr.New(apperr.Unavailable, "精确匹配超过时间上限，请缩短模式或缩小来源范围")
	}
	return nil
}

// grepBodies 逐行匹配。expired 或 ctx 结束时返回已扫到的命中和超时错误，
// 不再继续后面的行。条数达到 limit 时 Truncated 为 true，不算超时。
func grepBodies(ctx context.Context, docs []grepDoc, match func(string) bool, limit int, expired func() bool) ([]GrepHit, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = GrepHitLimit
	}
	hits := make([]GrepHit, 0)
	for _, doc := range docs {
		if err := grepStopped(ctx, expired); err != nil {
			return hits, true, err
		}
		if !strings.HasSuffix(strings.ToLower(doc.Path), ".md") {
			continue
		}
		for i, line := range grepLines(doc.Body) {
			if err := grepStopped(ctx, expired); err != nil {
				return hits, true, err
			}
			if !match(line) {
				continue
			}
			number := i + 1
			text := line
			if utf8.RuneCountInString(text) > 500 {
				text = string([]rune(text)[:500]) + "…"
			}
			hits = append(hits, GrepHit{
				Source: doc.Source, Commit: doc.Commit, Path: doc.Path,
				Line: number, Text: text, StartLine: number, EndLine: number,
				Citation: FormatCitation(doc.Source, doc.Commit, doc.Path, number, number),
			})
			if len(hits) >= limit {
				return hits, true, nil
			}
		}
	}
	return hits, false, nil
}

func grepLines(body string) []string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(body, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// FormatCitation 拼出 source@版本:path#Lx-Ly。托管库的版本是修订号，Git 来源是 commit。
func FormatCitation(source, version, path string, start, end int) string {
	if start <= 0 {
		start = 1
	}
	if end < start {
		end = start
	}
	ref := fmt.Sprintf("#L%d", start)
	if end > start {
		ref += fmt.Sprintf("-L%d", end)
	}
	return source + "@" + version + ":" + path + ref
}

// ParseCitation 解析出处字符串。解析失败时 ok 为 false。
func ParseCitation(raw string) (source, version, path string, start, end int, ok bool) {
	raw = strings.TrimSpace(raw)
	at := strings.IndexByte(raw, '@')
	if at <= 0 {
		return "", "", "", 0, 0, false
	}
	source = raw[:at]
	rest := raw[at+1:]
	colon := strings.IndexByte(rest, ':')
	if colon <= 0 {
		return "", "", "", 0, 0, false
	}
	version = rest[:colon]
	path = rest[colon+1:]
	if hash := strings.IndexByte(path, '#'); hash >= 0 {
		ref := path[hash:]
		path = path[:hash]
		var lo, hi int
		if _, err := fmt.Sscanf(ref, "#L%d-L%d", &lo, &hi); err != nil {
			if _, err = fmt.Sscanf(ref, "#L%d", &lo); err != nil {
				return "", "", "", 0, 0, false
			}
			hi = lo
		}
		start, end = lo, hi
	}
	if source == "" || version == "" || path == "" || strings.ContainsAny(source, " \t") || strings.ContainsAny(path, " \t") {
		return "", "", "", 0, 0, false
	}
	return source, version, path, start, end, true
}

// SourceFileURL 按来源的 Git 地址拼出仓库网页链接。规则与知识中心
// kbGitFileURL 一致：只接受 https 的 Git 来源和 40 位 commit，拼不出时返回空。
func (s *Service) SourceFileURL(source, version, path string, start, end int) string {
	if s == nil {
		return ""
	}
	ms, err := s.managed()
	if err != nil {
		return ""
	}
	src, ok := ms.KnowledgeSource(strings.TrimSpace(source))
	if !ok || src.Kind != "git" {
		return ""
	}
	return GitFileURL(src.GitURL, version, path, start, end)
}

// GitFileURL 把 Git 地址、commit、路径和行号拼成常见托管平台的网页链接。
func GitFileURL(gitURL, version, path string, start, end int) string {
	gitURL = strings.TrimSpace(gitURL)
	version = strings.TrimSpace(version)
	path = strings.TrimSpace(path)
	if gitURL == "" || path == "" || !commitLike(version) {
		return ""
	}
	u, err := url.Parse(gitURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	repo := strings.Trim(u.Path, "/")
	repo = strings.TrimSuffix(repo, ".git")
	if repo == "" {
		return ""
	}
	rel := escapePath(path)
	host := strings.ToLower(u.Hostname())
	origin := u.Scheme + "://" + u.Host
	ref := lineFragment(start, end)
	switch {
	case host == "github.com" || host == "gitee.com":
		return origin + "/" + repo + "/blob/" + version + "/" + rel + ref
	case host == "codeberg.org":
		return origin + "/" + repo + "/src/commit/" + version + "/" + rel + ref
	case host == "gitlab.com" || strings.Contains(host, "gitlab"):
		return origin + "/" + repo + "/-/blob/" + version + "/" + rel + ref
	case host == "bitbucket.org":
		anchor := ""
		lo, hi := linePair(start, end)
		if lo > 0 {
			anchor = fmt.Sprintf("#lines-%d:%d", lo, hi)
		}
		return origin + "/" + repo + "/src/" + version + "/" + rel + anchor
	default:
		return ""
	}
}

func commitLike(version string) bool {
	if len(version) != 40 {
		return false
	}
	for _, r := range version {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func linePair(start, end int) (int, int) {
	if start <= 0 {
		return 0, 0
	}
	if end < start {
		end = start
	}
	return start, end
}

func lineFragment(start, end int) string {
	lo, hi := linePair(start, end)
	if lo == 0 {
		return ""
	}
	if hi > lo {
		return fmt.Sprintf("#L%d-L%d", lo, hi)
	}
	return fmt.Sprintf("#L%d", lo)
}

func escapePath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
