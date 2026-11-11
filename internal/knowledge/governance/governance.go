// Package governance 实现知识文档健康检查的纯规则部分：过期、同库重复标题、
// 库内相对链接死链和缺少负责人。它不读数据库、不访问网络，也不修改正文；
// Service 负责取正文、持久化结果和调度。
package governance

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	TypeStale        = "stale"
	TypeDuplicate    = "duplicate"
	TypeDeadLink     = "dead_link"
	TypeMissingOwner = "missing_owner"

	DefaultStaleDays = 180
	// MaxDocuments 与 MaxIssues 防止单次扫描被异常大的知识库拖垮；超出时
	// Report.Truncated 明确标记，面板不把截断结果说成完整结果。
	MaxDocuments = 1000
	MaxIssues    = 500
)

// Document 是健康检查需要的文档视图。
type Document struct {
	SourceID string
	Path     string
	Title    string
	Body     string
	Owner    string
	Updated  *time.Time
}

// Issue 是单项检查结果。Line 只在死链时填写。
type Issue struct {
	SourceID string
	Path     string
	Title    string
	Type     string
	Line     int
	Summary  string
}

// Report 是一次知识库扫描的结果。Documents 是实际扫描的文档数。
type Report struct {
	Documents int
	Issues    []Issue
	Truncated bool
}

// Analyze 扫描一个知识库内的全部文档。now 用于过期判断，staleDays 小于等于
// 0 时使用 DefaultStaleDays。结果按路径、类型、行号排序，便于持久化和展示。
func Analyze(docs []Document, now time.Time, staleDays int) Report {
	if staleDays <= 0 {
		staleDays = DefaultStaleDays
	}
	now = now.UTC()
	report := Report{Issues: make([]Issue, 0)}
	if len(docs) > MaxDocuments {
		docs = docs[:MaxDocuments]
		report.Truncated = true
	}
	report.Documents = len(docs)

	pathIndex := buildPathIndex(docs)
	for i := range docs {
		doc := &docs[i]
		if strings.TrimSpace(doc.Owner) == "" {
			report.Issues = append(report.Issues, newIssue(doc, TypeMissingOwner, 0, "frontmatter 缺少负责人（owner）"))
		}
		report.Issues = append(report.Issues, staleIssues(doc, now, staleDays)...)
		report.Issues = append(report.Issues, deadLinkIssues(doc, pathIndex[doc.SourceID])...)
	}
	report.Issues = append(report.Issues, duplicateIssues(docs)...)

	sort.SliceStable(report.Issues, func(i, j int) bool {
		a, b := report.Issues[i], report.Issues[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.SourceID != b.SourceID {
			return a.SourceID < b.SourceID
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.Line < b.Line
	})
	if len(report.Issues) > MaxIssues {
		report.Issues = report.Issues[:MaxIssues]
		report.Truncated = true
	}
	return report
}

func newIssue(doc *Document, issueType string, line int, summary string) Issue {
	return Issue{
		SourceID: doc.SourceID, Path: doc.Path, Title: titleFor(doc),
		Type: issueType, Line: line, Summary: summary,
	}
}

func staleIssues(doc *Document, now time.Time, staleDays int) []Issue {
	if doc.Updated == nil {
		return []Issue{newIssue(doc, TypeStale, 0, "缺少 updated，无法判断是否过期")}
	}
	updated := doc.Updated.UTC()
	cutoff := now.AddDate(0, 0, -staleDays)
	if updated.Before(cutoff) {
		return []Issue{newIssue(doc, TypeStale, 0, fmt.Sprintf("文档上次更新 %s，已超过 %d 天未更新", updated.Format("2006-01-02"), staleDays))}
	}
	return nil
}

func duplicateIssues(docs []Document) []Issue {
	groups := map[string][]*Document{}
	for i := range docs {
		title := titleFor(&docs[i])
		normalized := normalizeTitle(title)
		if normalized == "" {
			continue
		}
		groups[normalized] = append(groups[normalized], &docs[i])
	}
	var issues []Issue
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		for _, doc := range group {
			issues = append(issues, newIssue(doc, TypeDuplicate, 0, fmt.Sprintf("同库内有 %d 篇文档标题重复：%s", len(group), titleFor(doc))))
		}
	}
	return issues
}

func titleFor(doc *Document) string {
	if title := strings.TrimSpace(doc.Title); title != "" {
		return title
	}
	base := path.Base(strings.TrimSpace(doc.Path))
	return strings.TrimSuffix(base, path.Ext(base))
}

func normalizeTitle(s string) string {
	text := strings.ToLower(strings.TrimSpace(s))
	text = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, text)
	return strings.Join(strings.Fields(text), " ")
}

func buildPathIndex(docs []Document) map[string]map[string]bool {
	index := map[string]map[string]bool{}
	for _, doc := range docs {
		if index[doc.SourceID] == nil {
			index[doc.SourceID] = map[string]bool{}
		}
		index[doc.SourceID][path.Clean(doc.Path)] = true
	}
	return index
}

var markdownLink = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)

func deadLinkIssues(doc *Document, sourcePaths map[string]bool) []Issue {
	if sourcePaths == nil {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(doc.Body, "\r\n", "\n"), "\n")
	start := frontmatterEnd(lines)
	inFence := false
	var issues []Issue
	for i := start; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		for _, match := range markdownLink.FindAllStringSubmatch(line, -1) {
			issue := checkLink(doc, i+1, strings.TrimSpace(match[2]), sourcePaths)
			if issue != nil {
				issues = append(issues, *issue)
			}
		}
	}
	return issues
}

func frontmatterEnd(lines []string) int {
	first := 0
	for first < len(lines) && strings.TrimSpace(lines[first]) == "" {
		first++
	}
	if first >= len(lines) || strings.TrimSpace(lines[first]) != "---" {
		return first
	}
	for i := first + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return i + 1
		}
	}
	return first + 1
}

func checkLink(doc *Document, line int, target string, sourcePaths map[string]bool) *Issue {
	if target == "" || strings.HasPrefix(target, "#") || strings.Contains(target, "://") ||
		strings.HasPrefix(strings.ToLower(target), "mailto:") || strings.HasPrefix(strings.ToLower(target), "data:") {
		return nil
	}
	// Markdown 链接可带 "title"；只取路径部分。
	if i := strings.IndexAny(target, " \t"); i > 0 {
		target = target[:i]
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return nil
	}
	linkPath := parsed.Path
	if linkPath == "" {
		return nil
	}
	// 平台只索引 Markdown，图片、附件等非 .md 相对链接无法用内容面判断，
	// 不做死链检查，避免把仓库里实际存在的资源报成问题。
	if ext := strings.ToLower(path.Ext(linkPath)); ext != ".md" {
		return nil
	}
	resolved := ""
	if strings.HasPrefix(linkPath, "/") {
		resolved = path.Clean(strings.TrimLeft(linkPath, "/"))
	} else {
		resolved = path.Clean(path.Join(path.Dir(doc.Path), linkPath))
	}
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return pointerIssue(doc, line, fmt.Sprintf("链接 %q 路径穿越，超出来源根目录", target))
	}
	if !sourcePaths[resolved] {
		return pointerIssue(doc, line, fmt.Sprintf("链接 %q 指向不存在的文档（解析为 %s）", target, resolved))
	}
	return nil
}

func pointerIssue(doc *Document, line int, summary string) *Issue {
	issue := newIssue(doc, TypeDeadLink, line, summary)
	return &issue
}
