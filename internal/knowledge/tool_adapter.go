package knowledge

import (
	"encoding/json"
	"net/url"
	"path"
	"strings"

	"agentflow/internal/domain"
)

// KnowledgeToolRole classifies an upstream knowledge MCP tool for metadata
// extraction and citation attribution.
type KnowledgeToolRole string

const (
	ToolRoleCatalog KnowledgeToolRole = "catalog"
	ToolRoleSearch  KnowledgeToolRole = "search"
	ToolRoleRead    KnowledgeToolRole = "read"
)

// KnowledgeToolRule declaratively maps a tool's arguments and response fields
// to LoopForge's unified KnowledgeEvidence model. Adding support for a new MCP
// provider requires only adding a rule entry here or setting Extra["tool_rules"].
type KnowledgeToolRule struct {
	Tool         string            `json:"tool"`
	Role         KnowledgeToolRole `json:"role,omitempty"`
	DocIDArgs    []string          `json:"docIdArgs,omitempty"`
	TitleArgs    []string          `json:"titleArgs,omitempty"`
	ListKeys     []string          `json:"listKeys,omitempty"`
	DocIDKeys    []string          `json:"docIdKeys,omitempty"`
	TitleKeys    []string          `json:"titleKeys,omitempty"`
	FileNameKeys []string          `json:"fileNameKeys,omitempty"`
	SourceIDKeys []string          `json:"sourceIdKeys,omitempty"`
}

var defaultDocIDArgKeys = []string{
	"file_id", "doc_id", "document_id", "knowledge_id", "page_id", "path", "uri", "url", "id",
}

var defaultTitleArgKeys = []string{
	"title", "file_name", "filename", "doc_name", "document_name", "name",
}

var builtinToolRules = []KnowledgeToolRule{
	{
		Tool:      "kb_fetch",
		Role:      ToolRoleRead,
		DocIDArgs: []string{"document_id", "doc_id", "knowledge_id", "path", "id"},
	},
	{
		Tool:      "read_document",
		Role:      ToolRoleRead,
		DocIDArgs: []string{"knowledge_id", "document_id", "doc_id", "path", "id"},
	},
	{
		// 腾讯文档 MCP: get_content / smartcanvas.read / sheet.get_cell_data
		Tool:      "get_content",
		Role:      ToolRoleRead,
		DocIDArgs: []string{"file_id", "doc_id", "document_id", "url", "path", "id"},
	},
	{
		Tool:      "smartcanvas.read",
		Role:      ToolRoleRead,
		DocIDArgs: []string{"file_id", "doc_id", "document_id", "url", "id"},
	},
	{
		Tool:      "sheet.get_cell_data",
		Role:      ToolRoleRead,
		DocIDArgs: []string{"file_id", "doc_id", "document_id", "url", "id"},
	},
	{
		// 腾讯文档 MCP: manage.search_file
		Tool:         "manage.search_file",
		Role:         ToolRoleSearch,
		ListKeys:     []string{"file_list", "files", "docs", "list"},
		DocIDKeys:    []string{"file_id", "doc_id", "url", "id"},
		TitleKeys:    []string{"file_name", "title", "name"},
		FileNameKeys: []string{"file_name", "name"},
	},
	{
		// 飞书 / Notion / 通用正文读取工具
		Tool:      "fetch-doc",
		Role:      ToolRoleRead,
		DocIDArgs: []string{"doc_id", "document_id", "file_id", "url", "id"},
	},
	{
		Tool:      "fetch_doc",
		Role:      ToolRoleRead,
		DocIDArgs: []string{"doc_id", "document_id", "file_id", "url", "id"},
	},
	{
		Tool:      "get_document",
		Role:      ToolRoleRead,
		DocIDArgs: []string{"document_id", "doc_id", "file_id", "url", "id"},
	},
	{
		Tool:      "read_page",
		Role:      ToolRoleRead,
		DocIDArgs: []string{"page_id", "doc_id", "document_id", "url", "id"},
	},
	{
		Tool:     "search-doc",
		Role:     ToolRoleSearch,
		ListKeys: []string{"docs", "files", "pages", "list"},
	},
	{
		Tool:     "search_doc",
		Role:     ToolRoleSearch,
		ListKeys: []string{"docs", "files", "pages", "list"},
	},
}

func resolveKnowledgeToolRule(extra map[string]string, toolName string) KnowledgeToolRule {
	toolName = strings.TrimSpace(toolName)
	var out KnowledgeToolRule
	for _, r := range builtinToolRules {
		if matchToolRuleName(r.Tool, toolName) {
			out = r
			break
		}
	}
	if raw := strings.TrimSpace(extra["tool_rules"]); raw != "" {
		var custom []KnowledgeToolRule
		if json.Unmarshal([]byte(raw), &custom) == nil {
			for _, r := range custom {
				if matchToolRuleName(r.Tool, toolName) {
					out = mergeToolRules(out, r)
					break
				}
			}
		}
	}
	if out.Role == "" {
		switch {
		case domain.IsKnowledgeCatalogTool(toolName):
			out.Role = ToolRoleCatalog
		case domain.IsKnowledgeReadTool(toolName):
			out.Role = ToolRoleRead
		}
	}
	if out.Tool == "" {
		out.Tool = toolName
	}
	return out
}

func matchToolRuleName(pattern, toolName string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || toolName == "" {
		return false
	}
	if pattern == "*" || strings.EqualFold(pattern, toolName) {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(strings.ToLower(toolName), strings.ToLower(strings.TrimSuffix(pattern, "*")))
	}
	return false
}

func mergeToolRules(base, override KnowledgeToolRule) KnowledgeToolRule {
	if override.Tool != "" {
		base.Tool = override.Tool
	}
	if override.Role != "" {
		base.Role = override.Role
	}
	if len(override.DocIDArgs) > 0 {
		base.DocIDArgs = override.DocIDArgs
	}
	if len(override.TitleArgs) > 0 {
		base.TitleArgs = override.TitleArgs
	}
	if len(override.ListKeys) > 0 {
		base.ListKeys = appendUniqueStrings(base.ListKeys, override.ListKeys...)
	}
	if len(override.DocIDKeys) > 0 {
		base.DocIDKeys = appendUniqueStrings(base.DocIDKeys, override.DocIDKeys...)
	}
	if len(override.TitleKeys) > 0 {
		base.TitleKeys = appendUniqueStrings(base.TitleKeys, override.TitleKeys...)
	}
	if len(override.FileNameKeys) > 0 {
		base.FileNameKeys = appendUniqueStrings(base.FileNameKeys, override.FileNameKeys...)
	}
	if len(override.SourceIDKeys) > 0 {
		base.SourceIDKeys = appendUniqueStrings(base.SourceIDKeys, override.SourceIDKeys...)
	}
	return base
}

func appendUniqueStrings(dst []string, items ...string) []string {
	seen := make(map[string]bool, len(dst))
	for _, s := range dst {
		if s = strings.TrimSpace(s); s != "" {
			seen[s] = true
		}
	}
	for _, s := range items {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			dst = append(dst, s)
		}
	}
	return dst
}

// enrichReadEvidences applies the 4-level fallback chain for document reads:
// 1) response metadata -> 2) prior session/run search evidences matched by arg doc ID
// -> 3) markdown heading / extracted target -> 4) arg doc ID / URL fallback.
func enrichReadEvidences(
	read *domain.KnowledgeRead,
	text string,
	args map[string]any,
	rule KnowledgeToolRule,
	priorReads []domain.KnowledgeRead,
	defaultConnID string,
) {
	if read == nil || rule.Role == ToolRoleCatalog || domain.IsKnowledgeCatalogTool(read.Tool) {
		return
	}
	if read.Status == domain.KnowledgeStatusUnavailable || read.Status == domain.KnowledgeStatusEmpty {
		return
	}

	// Level 1: enrich any existing evidences that have DocumentID but lack Title/FileName.
	if len(read.Evidences) > 0 {
		for i := range read.Evidences {
			if read.Evidences[i].Title == "" && read.Evidences[i].FileName == "" && read.Evidences[i].DocumentID != "" {
				if matched, ok := findPriorEvidenceByDocID(priorReads, read.ServerID, read.Evidences[i].DocumentID); ok {
					mergeEvidenceMeta(&read.Evidences[i], matched)
				}
			}
		}
		return
	}

	// Only synthesize fallback evidence for read tools, or when args explicitly identify a document.
	docID := firstArgString(args, appendUniqueStrings(rule.DocIDArgs, defaultDocIDArgKeys...)...)
	argTitle := firstArgString(args, appendUniqueStrings(rule.TitleArgs, defaultTitleArgKeys...)...)
	if rule.Role != ToolRoleRead && docID == "" && argTitle == "" {
		return
	}

	ev := domain.KnowledgeEvidence{
		ConnectionID: firstNonEmpty(read.ServerID, defaultConnID),
		DocumentID:   docID,
		Title:        clipCatalogText(argTitle, 120),
	}

	// Level 2: match against prior search/list evidences in the same session or node.
	if docID != "" {
		if matched, ok := findPriorEvidenceByDocID(priorReads, read.ServerID, docID); ok {
			mergeEvidenceMeta(&ev, matched)
		}
	}

	// Level 3: fallback to first Markdown heading or extracted target.
	if ev.Title == "" && ev.FileName == "" {
		if len(read.Targets) > 0 {
			ev.Title = clipCatalogText(read.Targets[0], 120)
		} else if heading := extractFirstMarkdownHeading(text); heading != "" {
			ev.Title = clipCatalogText(heading, 120)
		}
	}

	// Level 4: fallback to readable label derived from docID / URL.
	if ev.Title == "" && ev.FileName == "" && docID != "" {
		ev.Title = clipCatalogText(readableDocLabelFromID(docID), 120)
	}

	if ev.Title == "" && ev.FileName == "" && ev.DocumentID == "" {
		return
	}
	read.Evidences = []domain.KnowledgeEvidence{ev}
	if read.Status == domain.KnowledgeStatusUnstructured {
		read.Status = domain.KnowledgeStatusHit
	}
}

func firstArgString(args map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := args[k]; ok {
			if s := strings.TrimSpace(argString(v)); s != "" {
				return s
			}
		}
	}
	return ""
}

func findPriorEvidenceByDocID(priorReads []domain.KnowledgeRead, serverID, docID string) (domain.KnowledgeEvidence, bool) {
	target := normalizeDocIDForMatch(docID)
	if target == "" {
		return domain.KnowledgeEvidence{}, false
	}
	for i := len(priorReads) - 1; i >= 0; i-- {
		r := priorReads[i]
		if serverID != "" && r.ServerID != "" && r.ServerID != serverID {
			continue
		}
		for _, ev := range r.Evidences {
			if normalizeDocIDForMatch(ev.DocumentID) == target {
				return ev, true
			}
			if ev.DocumentID != "" && (strings.Contains(ev.DocumentID, target) || strings.Contains(target, normalizeDocIDForMatch(ev.DocumentID))) {
				if len(target) >= 6 && len(normalizeDocIDForMatch(ev.DocumentID)) >= 6 {
					return ev, true
				}
			}
		}
	}
	return domain.KnowledgeEvidence{}, false
}

func normalizeDocIDForMatch(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		if u, err := url.Parse(raw); err == nil {
			p := strings.Trim(u.Path, "/")
			if p != "" {
				return strings.ToLower(path.Base(p))
			}
		}
	}
	return strings.ToLower(raw)
}

func mergeEvidenceMeta(dst *domain.KnowledgeEvidence, src domain.KnowledgeEvidence) {
	if dst.SourceID == "" {
		dst.SourceID = src.SourceID
	}
	if dst.SourceName == "" {
		dst.SourceName = src.SourceName
	}
	if dst.DocumentID == "" {
		dst.DocumentID = src.DocumentID
	}
	if dst.Title == "" {
		dst.Title = src.Title
	}
	if dst.FileName == "" {
		dst.FileName = src.FileName
	}
}

func extractFirstMarkdownHeading(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			h := trimOrderedListPrefix(strings.TrimSpace(strings.TrimLeft(line, "#")))
			if h != "" {
				return h
			}
		}
	}
	return ""
}

func readableDocLabelFromID(docID string) string {
	docID = strings.TrimSpace(docID)
	if strings.HasPrefix(docID, "http://") || strings.HasPrefix(docID, "https://") {
		if u, err := url.Parse(docID); err == nil {
			base := strings.Trim(path.Base(u.Path), "/")
			if base != "" && base != "." {
				return base
			}
		}
	}
	return docID
}
