package knowledge

import (
	"encoding/json"
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

const maxKnowledgeReadTargets = 5

// ExtractEvidences parses structured document evidences and target titles from a
// JSON or Markdown tool response. Connectors that already have typed response
// models can populate CallResult.Evidences directly; generic connectors (such as
// MCPConnector) call ExtractEvidences.
func ExtractEvidences(raw, defaultConnID string) ([]string, []domain.KnowledgeEvidence, bool, int) {
	return ExtractEvidencesWithRule(raw, defaultConnID, nil)
}

// ExtractEvidencesWithRule parses structured document evidences and target titles
// using optional provider/tool field mapping rules.
func ExtractEvidencesWithRule(raw, defaultConnID string, rule *KnowledgeToolRule) ([]string, []domain.KnowledgeEvidence, bool, int) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil, false, -1
	}
	var targets []string
	seenTargets := map[string]bool{}
	addTarget := func(s string) {
		s = clipCatalogText(s, 120)
		if s == "" || seenTargets[s] {
			return
		}
		seenTargets[s] = true
		if len(targets) < maxKnowledgeReadTargets {
			targets = append(targets, s)
		}
	}

	var parsed any
	if json.Unmarshal([]byte(raw), &parsed) == nil {
		var evidences []domain.KnowledgeEvidence
		listCount := collectKnowledgeEvidencesFromJSON(parsed, addTarget, &evidences, defaultConnID, knowledgeParentMeta{connID: defaultConnID}, rule)
		return targets, evidences, true, listCount
	}

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "[[knowledge:") {
			continue
		}
		if title := extractMarkdownKnowledgeTarget(line); title != "" {
			addTarget(title)
		}
	}
	return targets, nil, false, -1
}

type knowledgeParentMeta struct {
	connID     string
	sourceID   string
	sourceName string
	docID      string
	title      string
	fileName   string
}

func extractKnowledgeMetaFromMap(m map[string]any, rule *KnowledgeToolRule) knowledgeParentMeta {
	strVal := func(keys ...string) string {
		for _, k := range keys {
			if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
		return ""
	}
	sourceKeys := []string{"source", "source_id", "knowledge_base_id", "kb_id", "dataset_id", "space_id"}
	docIDKeys := []string{"document_id", "doc_id", "file_id", "knowledge_id", "page_id", "path", "uri", "url", "id"}
	titleKeys := []string{"title", "knowledge_title", "document_title", "doc_title", "document_keyword", "name"}
	fileNameKeys := []string{"file_name", "filename", "doc_name", "document_name", "document_keyword"}
	if rule != nil {
		if len(rule.SourceIDKeys) > 0 {
			sourceKeys = appendUniqueStrings(append([]string(nil), rule.SourceIDKeys...), sourceKeys...)
		}
		if len(rule.DocIDKeys) > 0 {
			docIDKeys = appendUniqueStrings(append([]string(nil), rule.DocIDKeys...), docIDKeys...)
		}
		if len(rule.TitleKeys) > 0 {
			titleKeys = appendUniqueStrings(append([]string(nil), rule.TitleKeys...), titleKeys...)
		}
		if len(rule.FileNameKeys) > 0 {
			fileNameKeys = appendUniqueStrings(append([]string(nil), rule.FileNameKeys...), fileNameKeys...)
		}
	}
	sourceID := strVal(sourceKeys...)
	docID := strVal(docIDKeys...)
	connID := strVal("connection_id", "connectionId", "connector_id", "connectorId")
	if connID == "" && strings.Contains(sourceID, "::") {
		connID, _, _ = strings.Cut(sourceID, "::")
	}
	if connID == "" && strings.Contains(docID, "::") {
		connID, _, _ = strings.Cut(docID, "::")
	}
	return knowledgeParentMeta{
		connID:     connID,
		sourceID:   sourceID,
		sourceName: strVal("source_name", "knowledge_base_name", "kb_name", "dataset_name", "space_name"),
		docID:      docID,
		title:      strVal(titleKeys...),
		fileName:   strVal(fileNameKeys...),
	}
}

func mergeKnowledgeMeta(parent, self knowledgeParentMeta) knowledgeParentMeta {
	return knowledgeParentMeta{
		connID:     firstNonEmpty(self.connID, parent.connID),
		sourceID:   firstNonEmpty(self.sourceID, parent.sourceID),
		sourceName: firstNonEmpty(self.sourceName, parent.sourceName),
		docID:      firstNonEmpty(self.docID, parent.docID),
		title:      firstNonEmpty(self.title, parent.title),
		fileName:   firstNonEmpty(self.fileName, parent.fileName),
	}
}

func jsonIntVal(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i, true
		}
	}
	return 0, false
}

func summarizeChunksLocator(chunks []any) string {
	var minIdx, maxIdx int64
	hasIdx := false
	for _, item := range chunks {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		idx, ok := jsonIntVal(m["chunk_index"])
		if !ok {
			continue
		}
		if !hasIdx {
			minIdx, maxIdx, hasIdx = idx, idx, true
			continue
		}
		if idx < minIdx {
			minIdx = idx
		}
		if idx > maxIdx {
			maxIdx = idx
		}
	}
	if !hasIdx {
		return ""
	}
	if minIdx == maxIdx {
		return fmt.Sprintf("#chunk-%d", minIdx)
	}
	return fmt.Sprintf("#chunk-%d..#chunk-%d", minIdx, maxIdx)
}

func hasExplicitDocParentMeta(m map[string]any) bool {
	for _, k := range []string{
		"document_id", "doc_id", "file_id", "knowledge_id", "path", "uri",
		"title", "knowledge_title", "document_title", "doc_title", "file_name", "filename", "doc_name", "document_name", "document_keyword",
	} {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return true
		}
	}
	return false
}

func chunksCarryDocIdentity(chunks []any) bool {
	for _, item := range chunks {
		m, ok := item.(map[string]any)
		if ok && hasExplicitDocParentMeta(m) {
			return true
		}
	}
	return false
}

func collectKnowledgeEvidencesFromJSON(v any, addTarget func(string), evidences *[]domain.KnowledgeEvidence, defaultConnID string, parent knowledgeParentMeta, rule *KnowledgeToolRule) int {
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			collectKnowledgeEvidencesFromJSON(item, addTarget, evidences, defaultConnID, parent, rule)
		}
		return len(x)
	case map[string]any:
		selfMeta := extractKnowledgeMetaFromMap(x, rule)
		mergedMeta := mergeKnowledgeMeta(parent, selfMeta)
		if chunksRaw, hasChunks := x["chunks"]; hasChunks && hasExplicitDocParentMeta(x) {
			if chunks, isArr := chunksRaw.([]any); isArr {
				if len(chunks) == 0 {
					return 0
				}
				if !chunksCarryDocIdentity(chunks) {
					ev := domain.KnowledgeEvidence{
						ConnectionID: firstNonEmpty(mergedMeta.connID, defaultConnID),
						SourceID:     mergedMeta.sourceID,
						SourceName:   clipCatalogText(mergedMeta.sourceName, 120),
						DocumentID:   mergedMeta.docID,
						Title:        clipCatalogText(mergedMeta.title, 120),
						FileName:     clipCatalogText(mergedMeta.fileName, 120),
						Locator:      summarizeChunksLocator(chunks),
					}
					*evidences = append(*evidences, ev)
					if label := firstNonEmpty(ev.FileName, ev.Title, ev.DocumentID, ev.SourceID); label != "" {
						addTarget(label)
					}
					return len(chunks)
				}
			}
		}

		childParent := knowledgeParentMeta{
			connID:     firstNonEmpty(mergedMeta.connID, defaultConnID),
			sourceID:   mergedMeta.sourceID,
			sourceName: mergedMeta.sourceName,
		}
		containerKeys := []string{"results", "documents", "chunks", "items", "knowledge_bases", "sources", "pages", "hits", "file_list", "files", "docs", "list", "records"}
		if rule != nil && len(rule.ListKeys) > 0 {
			containerKeys = appendUniqueStrings(append([]string(nil), rule.ListKeys...), containerKeys...)
		}
		listCount := -1
		for _, key := range containerKeys {
			if sub, ok := x[key]; ok {
				if arr, isArr := sub.([]any); isArr {
					if listCount < 0 || len(arr) > listCount {
						listCount = len(arr)
					}
					for _, item := range arr {
						collectKnowledgeEvidencesFromJSON(item, addTarget, evidences, defaultConnID, childParent, rule)
					}
				}
			}
		}
		if sub, ok := x["data"]; ok {
			switch d := sub.(type) {
			case []any:
				if listCount < 0 || len(d) > listCount {
					listCount = len(d)
				}
				for _, item := range d {
					collectKnowledgeEvidencesFromJSON(item, addTarget, evidences, defaultConnID, childParent, rule)
				}
			case map[string]any:
				subCount := collectKnowledgeEvidencesFromJSON(d, addTarget, evidences, defaultConnID, mergedMeta, rule)
				if listCount < 0 && subCount >= 0 {
					listCount = subCount
				}
			}
		}
		if listCount >= 0 {
			return listCount
		}
		ev, ok := buildKnowledgeEvidenceFromMap(x, defaultConnID, parent, rule)
		if ok {
			*evidences = append(*evidences, ev)
			if label := firstNonEmpty(ev.FileName, ev.Title, ev.DocumentID, ev.SourceID); label != "" {
				addTarget(label)
			}
			return 1
		}
	}
	return -1
}

func buildKnowledgeEvidenceFromMap(m map[string]any, defaultConnID string, parent knowledgeParentMeta, rule *KnowledgeToolRule) (domain.KnowledgeEvidence, bool) {
	meta := mergeKnowledgeMeta(parent, extractKnowledgeMetaFromMap(m, rule))
	if meta.title == "" && meta.fileName == "" && meta.docID == "" && meta.sourceID == "" {
		return domain.KnowledgeEvidence{}, false
	}
	locator := ""
	if s, ok := m["locator"].(string); ok && strings.TrimSpace(s) != "" {
		locator = strings.TrimSpace(s)
	}
	if locator == "" {
		if chunkIdx, okC := jsonIntVal(m["chunk_index"]); okC {
			locator = fmt.Sprintf("#chunk-%d", chunkIdx)
		} else if start, okS := jsonIntVal(m["start_line"]); okS {
			if end, okE := jsonIntVal(m["end_line"]); okE && (start > 0 || end > 0) {
				locator = fmt.Sprintf("L%d-L%d", start, end)
			}
		}
	}
	return domain.KnowledgeEvidence{
		ConnectionID: firstNonEmpty(meta.connID, defaultConnID),
		SourceID:     meta.sourceID,
		SourceName:   clipCatalogText(meta.sourceName, 120),
		DocumentID:   meta.docID,
		Title:        clipCatalogText(meta.title, 120),
		FileName:     clipCatalogText(meta.fileName, 120),
		Locator:      locator,
	}, true
}

func extractMarkdownKnowledgeTarget(line string) string {
	if strings.HasPrefix(line, "#") {
		heading := strings.TrimSpace(strings.TrimLeft(line, "#"))
		heading = trimOrderedListPrefix(heading)
		if heading != "" {
			return heading
		}
	}
	cleaned := strings.TrimSpace(strings.TrimLeft(line, "-*• "))
	cleaned = strings.ReplaceAll(cleaned, "**", "")
	lower := strings.ToLower(cleaned)
	for _, prefix := range []string{
		"title:", "title：", "标题:", "标题：",
		"knowledge_title:", "knowledge_title：",
		"document_title:", "document_title：",
		"document:", "document：", "文档:", "文档：",
		"file:", "file：", "文件:", "文件：",
		"path:", "path：", "路径:", "路径：",
		"knowledge_id:", "knowledge_id：",
		"document_id:", "document_id：",
	} {
		if strings.HasPrefix(lower, prefix) {
			val := strings.TrimSpace(cleaned[len(prefix):])
			if val != "" {
				return val
			}
		}
	}
	return ""
}

func trimOrderedListPrefix(s string) string {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i > 0 && i < len(s) {
		rest := s[i:]
		for _, sep := range []string{".", ")", "、"} {
			if strings.HasPrefix(rest, sep) {
				return strings.TrimSpace(rest[len(sep):])
			}
		}
	}
	return s
}

func extractCatalogTitles(raw string, limit int) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || limit <= 0 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] || len(out) >= limit {
			return
		}
		seen[s] = true
		out = append(out, clipCatalogText(s, 80))
	}

	var parsed any
	if json.Unmarshal([]byte(raw), &parsed) == nil {
		collectCatalogTitlesFromJSON(parsed, add)
		if len(out) > 0 {
			return out
		}
	}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "-*•0123456789. "))
		if line != "" && !strings.HasPrefix(line, "{") && !strings.HasPrefix(line, "[") {
			add(line)
		}
		if len(out) >= limit {
			break
		}
	}
	return out
}

func collectCatalogTitlesFromJSON(v any, add func(string)) {
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			collectCatalogTitlesFromJSON(item, add)
		}
	case map[string]any:
		for _, key := range []string{"file_name", "filename", "doc_name", "title", "name", "id"} {
			if s, ok := x[key].(string); ok && strings.TrimSpace(s) != "" {
				if desc, ok := x["description"].(string); ok && strings.TrimSpace(desc) != "" && key != "description" {
					add(s + " (" + clipCatalogText(strings.TrimSpace(desc), 40) + ")")
				} else {
					add(s)
				}
				break
			}
		}
		for _, key := range []string{"documents", "items", "knowledge_bases", "sources", "data", "results"} {
			if sub, ok := x[key]; ok {
				collectCatalogTitlesFromJSON(sub, add)
			}
		}
	}
}

func extractCatalogBaseIDs(raw string, limit int) []string {
	var parsed any
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &parsed) != nil || limit <= 0 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		if len(out) >= limit {
			return
		}
		switch x := v.(type) {
		case []any:
			for _, item := range x {
				walk(item)
			}
		case map[string]any:
			if id, ok := x["id"].(string); ok {
				id = strings.TrimSpace(id)
				if id != "" && !seen[id] {
					seen[id] = true
					out = append(out, id)
				}
			}
			for _, key := range []string{"knowledge_bases", "items", "data", "results"} {
				if sub, ok := x[key]; ok {
					walk(sub)
				}
			}
		}
	}
	walk(parsed)
	return out
}

func clipCatalogText(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
}

func escapeXMLText(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func appendUniqueCatalogItems(dst, src []string, limit int) []string {
	seen := make(map[string]bool, len(dst))
	for _, s := range dst {
		seen[s] = true
	}
	for _, s := range src {
		if len(dst) >= limit {
			break
		}
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		dst = append(dst, s)
	}
	return dst
}

func argString(v any) string {
	if v == nil {
		return ""
	}
	s := fmt.Sprint(v)
	if s == "<nil>" {
		return ""
	}
	return s
}

func argStringList(v any) []string {
	switch list := v.(type) {
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		if strings.TrimSpace(list) == "" {
			return nil
		}
		return strings.Split(list, ",")
	}
	return nil
}

func argInt(v any, fallback int) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return int(n)
		}
	}
	return fallback
}
