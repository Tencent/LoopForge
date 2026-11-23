package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	"agentflow/internal/mcpclient"
)

// Resolve credentials and visibility per call. External servers support their
// credential scope only. Only explicitly read-only tools are offered.
func referenceTools(ctx context.Context, d *deps, project string, uid int64, ref domain.KnowledgeRef) ([]mcpclient.Tool, *mcpclient.Client, error) {
	if err := domain.ValidateKnowledgeRefs([]domain.KnowledgeRef{ref}); err != nil {
		return nil, nil, err
	}
	if authRequired() {
		user, ok := d.repo.User(uid)
		p, found := d.repo.Project(project)
		if !ok || !found || !workbenchProjectAllowsUser(p, uid, d.repo.IsPlatformAdmin(user.LoginName)) {
			return nil, nil, fmt.Errorf("知识服务项目授权已失效")
		}
	}
	if ref.ServerID == domain.KnowledgeMCPServerID {
		s := d.repo.KnowledgeSettings()
		if !s.Ready() {
			return nil, nil, knowledge.ErrNotConfigured
		}
		tools, err := knowledge.ReadOnlyToolsForSettings(ctx, s)
		if err != nil {
			return nil, nil, err
		}
		return tools, nil, nil
	}
	server, ok := d.repo.MCPServer(ref.ServerID)
	if !ok || len(assistantOutboundMCPServers([]domain.MCPServerDefinition{server}, project, uid)) != 1 || server.Transport != "http" {
		return nil, nil, fmt.Errorf("知识服务不可用或无权限")
	}
	user, ok := d.repo.User(uid)
	if !ok {
		return nil, nil, fmt.Errorf("知识服务调用身份无效")
	}
	bound, err := bindAssistantMCP(server, user)
	if err != nil {
		return nil, nil, fmt.Errorf("知识服务凭据不可用")
	}
	timeout := knowledge.CallTimeout
	if server.TimeoutSec > 0 && time.Duration(server.TimeoutSec)*time.Second < timeout {
		timeout = time.Duration(server.TimeoutSec) * time.Second
	}
	client := mcpclient.New(bound.URL, bound.Headers).WithTimeout(timeout)
	tools, err := client.ListToolsContext(ctx)
	if err != nil {
		return nil, nil, knowledgeCallError(err)
	}
	out := []mcpclient.Tool{}
	for _, tool := range tools {
		var hints struct {
			ReadOnly bool `json:"readOnlyHint"`
		}
		if json.Unmarshal(tool.Annotations, &hints) == nil && hints.ReadOnly {
			out = append(out, tool)
		}
	}
	return out, client, nil
}
func callKnowledgeReference(ctx context.Context, d *deps, project string, uid int64, ref domain.KnowledgeRef, name string, args map[string]any) (string, int, error) {
	res, err := callKnowledgeReferenceWithOrigin(ctx, d, project, uid, ref, name, args, domain.KnowledgeCallOriginModelTool)
	if err != nil {
		return "", 0, err
	}
	return res.Text, res.Hits, nil
}

func callKnowledgeReferenceWithOrigin(ctx context.Context, d *deps, project string, uid int64, ref domain.KnowledgeRef, name string, args map[string]any, origin string) (knowledge.ExecutionResult, error) {
	if origin == "" {
		origin = domain.KnowledgeCallOriginModelTool
	}
	started := time.Now()
	tools, client, err := referenceTools(ctx, d, project, uid, ref)
	if err != nil {
		if origin == domain.KnowledgeCallOriginCatalogProbe {
			log.Printf("knowledge-call origin=%s project=%s server=%s tool=%s ok=false latency=%dms", origin, project, ref.ServerID, name, time.Since(started).Milliseconds())
		}
		return knowledge.ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, err
	}
	for _, tool := range tools {
		if tool.Name != name {
			continue
		}
		if client != nil {
			text, callErr := client.CallToolContext(ctx, name, args)
			if origin == domain.KnowledgeCallOriginCatalogProbe {
				log.Printf("knowledge-call origin=%s project=%s server=%s tool=%s ok=%t latency=%dms", origin, project, ref.ServerID, name, callErr == nil, time.Since(started).Milliseconds())
			}
			if callErr != nil {
				return knowledge.ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, knowledgeCallError(callErr)
			}
			trimmed := strings.TrimSpace(text)
			if origin == domain.KnowledgeCallOriginCatalogProbe {
				trimmed = truncateRunes(trimmed, 32000)
			}
			_, hits := extractKnowledgeResultMeta(trimmed)
			return knowledge.ExecutionResult{Text: trimmed, Hits: hits}, nil
		}
		settings := d.repo.KnowledgeSettings()
		if !settings.Ready() {
			return knowledge.ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, knowledge.ErrNotConfigured
		}
		svc := d.knowledgeSvc
		if svc == nil {
			svc = knowledge.NewService(d.repo, nil)
		}
		res, execErr := svc.ExecuteTool(ctx, project, name, args, knowledge.ExecuteOptions{ScopeOverride: ref.Sources})
		if origin == domain.KnowledgeCallOriginCatalogProbe {
			log.Printf("knowledge-call origin=%s project=%s server=%s tool=%s status=%s ok=%t latency=%dms", origin, project, ref.ServerID, name, res.Status, execErr == nil, time.Since(started).Milliseconds())
		}
		if execErr != nil {
			return res, execErr
		}
		if res.Hits == 0 {
			if _, parsedHits := extractKnowledgeResultMeta(res.Text); parsedHits > 0 {
				res.Hits = parsedHits
			}
		}
		return res, nil
	}
	return knowledge.ExecutionResult{Status: domain.KnowledgeStatusUnavailable}, fmt.Errorf("知识服务未开放该只读工具")
}

func knowledgeReadResult(serverID, tool, text string, hits int, err error) (domain.KnowledgeRead, string) {
	return knowledgeReadResultWithMeta(serverID, tool, text, hits, "", "", domain.KnowledgeCallOriginModelTool, err)
}

func knowledgeReadResultWithMeta(serverID, tool, text string, hits int, execStatus, execWarning, origin string, err error) (domain.KnowledgeRead, string) {
	if origin == "" {
		origin = domain.KnowledgeCallOriginModelTool
	}
	read := domain.KnowledgeRead{
		ID:       rand.Text(),
		Origin:   origin,
		ServerID: serverID,
		Tool:     tool,
		Warning:  strings.TrimSpace(execWarning),
	}
	if err != nil {
		read.Status = domain.KnowledgeStatusUnavailable
		if read.Warning == "" {
			read.Warning = truncateRunes(strings.TrimSpace(err.Error()), 200)
		}
		return read, text
	}
	if strings.TrimSpace(text) == "" {
		if execStatus == domain.KnowledgeStatusUnavailable {
			read.Status = domain.KnowledgeStatusUnavailable
		} else {
			read.Status = domain.KnowledgeStatusEmpty
		}
		return read, text
	}

	evPrefix := knowledgeEvidenceIDPrefix(read.ID)
	targets, evidences, isJSON, jsonListCount := extractKnowledgeEvidences(text, evPrefix)
	read.Targets = targets
	read.Evidences = evidences

	switch {
	case execStatus == domain.KnowledgeStatusUnavailable:
		read.Status = domain.KnowledgeStatusUnavailable
		read.Evidences = nil
	case execStatus == domain.KnowledgeStatusPartial:
		read.Status = domain.KnowledgeStatusPartial
	case execStatus == domain.KnowledgeStatusEmpty:
		read.Status = domain.KnowledgeStatusEmpty
		read.Evidences = nil
	case isJSON && jsonListCount == 0:
		read.Status = domain.KnowledgeStatusEmpty
		read.Evidences = nil
	case serverID == domain.KnowledgeMCPServerID && (tool == knowledge.ToolSearch || tool == knowledge.WeKnoraToolSearchKnowledge) && hits == 0 && len(evidences) == 0:
		read.Status = domain.KnowledgeStatusEmpty
	case serverID != domain.KnowledgeMCPServerID && len(evidences) == 0:
		read.Status = domain.KnowledgeStatusUnstructured
	default:
		read.Status = domain.KnowledgeStatusHit
	}

	if serverID != domain.KnowledgeMCPServerID {
		text = truncateRunes(strings.TrimSpace(text), 32000)
	}

	if domain.IsKnowledgeCatalogTool(tool) {
		read.Evidences = nil
		return read, text
	}

	if domain.IsKnowledgeCitableStatus(read.Status) {
		if len(read.Evidences) > 0 {
			var b strings.Builder
			b.WriteString(text)
			b.WriteString("\n证据引用编号（若回答采用对应内容，请在引用处紧跟输出 [[knowledge:<id>]] 标记，勿放入反引号）：")
			for _, ev := range read.Evidences {
				label := firstNonEmpty(ev.FileName, ev.Title, ev.DocumentID, ev.SourceID)
				if ev.Title != "" && ev.FileName != "" && !strings.EqualFold(ev.Title, ev.FileName) {
					base := strings.TrimSuffix(ev.FileName, filepath.Ext(ev.FileName))
					if !strings.EqualFold(ev.Title, base) {
						label = ev.Title + " (" + ev.FileName + ")"
					}
				}
				if ev.SourceName != "" && ev.SourceName != label {
					label = ev.SourceName + " / " + label
				}
				if ev.Locator != "" {
					fmt.Fprintf(&b, "\n- [[knowledge:%s]] %s (%s)", ev.ID, label, ev.Locator)
				} else {
					fmt.Fprintf(&b, "\n- [[knowledge:%s]] %s", ev.ID, label)
				}
			}
			text = b.String()
		} else {
			text += "\n若输出引用本次结果，请紧跟附上 [[knowledge:" + read.ID + "]] 标记（勿放入反引号）。"
		}
	}
	return read, text
}

func knowledgeEvidenceIDPrefix(readID string) string {
	readID = strings.ToLower(strings.TrimSpace(readID))
	if readID == "" {
		return "ev"
	}
	if len(readID) > 6 {
		readID = readID[:6]
	}
	return "ev_" + readID
}

type knowledgeParentMeta struct {
	connID     string
	sourceID   string
	sourceName string
	docID      string
	title      string
	fileName   string
}

func extractKnowledgeMetaFromMap(m map[string]any) knowledgeParentMeta {
	strVal := func(keys ...string) string {
		for _, k := range keys {
			if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
		return ""
	}
	sourceID := strVal("source", "source_id", "knowledge_base_id", "kb_id")
	docID := strVal("document_id", "doc_id", "knowledge_id", "path", "uri", "id")
	connID := strVal("connection_id", "connectionId")
	if connID == "" && strings.Contains(sourceID, "::") {
		connID, _, _ = strings.Cut(sourceID, "::")
	}
	if connID == "" && strings.Contains(docID, "::") {
		connID, _, _ = strings.Cut(docID, "::")
	}
	return knowledgeParentMeta{
		connID:     connID,
		sourceID:   sourceID,
		sourceName: strVal("source_name", "knowledge_base_name", "kb_name"),
		docID:      docID,
		title:      strVal("title", "knowledge_title", "document_title", "doc_title", "name"),
		fileName:   strVal("file_name", "filename", "doc_name"),
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

func extractKnowledgeEvidences(raw, evPrefix string) ([]string, []domain.KnowledgeEvidence, bool, int) {
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
		listCount := collectKnowledgeEvidencesFromJSON(parsed, addTarget, &evidences, evPrefix, knowledgeParentMeta{})
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
		"document_id", "doc_id", "knowledge_id", "path", "uri",
		"title", "knowledge_title", "document_title", "doc_title", "file_name", "filename", "doc_name",
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

func collectKnowledgeEvidencesFromJSON(v any, addTarget func(string), evidences *[]domain.KnowledgeEvidence, evPrefix string, parent knowledgeParentMeta) int {
	if strings.TrimSpace(evPrefix) == "" {
		evPrefix = "ev"
	}
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			collectKnowledgeEvidencesFromJSON(item, addTarget, evidences, evPrefix, parent)
		}
		return len(x)
	case map[string]any:
		selfMeta := extractKnowledgeMetaFromMap(x)
		mergedMeta := mergeKnowledgeMeta(parent, selfMeta)
		if chunksRaw, hasChunks := x["chunks"]; hasChunks && hasExplicitDocParentMeta(x) {
			if chunks, isArr := chunksRaw.([]any); isArr {
				if len(chunks) == 0 {
					return 0
				}
				if !chunksCarryDocIdentity(chunks) {
					ev := domain.KnowledgeEvidence{
						ID:           fmt.Sprintf("%s_%d", evPrefix, len(*evidences)+1),
						ConnectionID: mergedMeta.connID,
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
			connID:     mergedMeta.connID,
			sourceID:   mergedMeta.sourceID,
			sourceName: mergedMeta.sourceName,
		}
		containerKeys := []string{"results", "documents", "chunks", "items", "knowledge_bases", "sources", "pages", "hits"}
		listCount := -1
		for _, key := range containerKeys {
			if sub, ok := x[key]; ok {
				if arr, isArr := sub.([]any); isArr {
					if listCount < 0 || len(arr) > listCount {
						listCount = len(arr)
					}
					for _, item := range arr {
						collectKnowledgeEvidencesFromJSON(item, addTarget, evidences, evPrefix, childParent)
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
					collectKnowledgeEvidencesFromJSON(item, addTarget, evidences, evPrefix, childParent)
				}
			case map[string]any:
				subCount := collectKnowledgeEvidencesFromJSON(d, addTarget, evidences, evPrefix, mergedMeta)
				if listCount < 0 && subCount >= 0 {
					listCount = subCount
				}
			}
		}
		if listCount >= 0 {
			return listCount
		}
		ev, ok := buildKnowledgeEvidenceFromMap(x, len(*evidences)+1, evPrefix, parent)
		if ok {
			*evidences = append(*evidences, ev)
			if label := firstNonEmpty(ev.Title, ev.DocumentID, ev.SourceID); label != "" {
				addTarget(label)
			}
			return 1
		}
	}
	return -1
}

func buildKnowledgeEvidenceFromMap(m map[string]any, idx int, evPrefix string, parent knowledgeParentMeta) (domain.KnowledgeEvidence, bool) {
	meta := mergeKnowledgeMeta(parent, extractKnowledgeMetaFromMap(m))
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
	if strings.TrimSpace(evPrefix) == "" {
		evPrefix = "ev"
	}
	return domain.KnowledgeEvidence{
		ID:           fmt.Sprintf("%s_%d", evPrefix, idx),
		ConnectionID: meta.connID,
		SourceID:     meta.sourceID,
		SourceName:   clipCatalogText(meta.sourceName, 120),
		DocumentID:   meta.docID,
		Title:        clipCatalogText(meta.title, 120),
		FileName:     clipCatalogText(meta.fileName, 120),
		Locator:      locator,
	}, true
}

const maxKnowledgeReadTargets = 5

func knowledgeCallQuerySummary(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	for _, key := range []string{"query", "knowledge_id", "document_id", "path", "keyword"} {
		v, ok := args[key]
		if !ok || v == nil {
			continue
		}
		s := strings.Join(strings.Fields(strings.TrimSpace(fmt.Sprint(v))), " ")
		if s != "" && s != "<nil>" {
			runes := []rune(s)
			if len(runes) > 120 {
				return string(runes[:119]) + "…"
			}
			return s
		}
	}
	return ""
}

func extractKnowledgeResultMeta(raw string) ([]string, int) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, 0
	}
	var targets []string
	seen := map[string]bool{}
	addTarget := func(s string) {
		s = clipCatalogText(s, 120)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		if len(targets) < maxKnowledgeReadTargets {
			targets = append(targets, s)
		}
	}

	var parsed any
	if json.Unmarshal([]byte(raw), &parsed) == nil {
		hits := collectKnowledgeTargetsFromJSON(parsed, addTarget)
		return targets, hits
	}

	hits := 0
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "[[knowledge:") {
			continue
		}
		if title := extractMarkdownKnowledgeTarget(line); title != "" {
			hits++
			addTarget(title)
		}
	}
	return targets, hits
}

func collectKnowledgeTargetsFromJSON(v any, add func(string)) int {
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			collectKnowledgeTargetsFromJSON(item, add)
		}
		return len(x)
	case map[string]any:
		containerKeys := []string{"results", "documents", "chunks", "items", "knowledge_bases", "sources", "pages", "hits"}
		listHits := -1
		for _, key := range containerKeys {
			if sub, ok := x[key]; ok {
				if arr, isArr := sub.([]any); isArr {
					if len(arr) > listHits {
						listHits = len(arr)
					}
					for _, item := range arr {
						collectKnowledgeTargetsFromJSON(item, add)
					}
				}
			}
		}
		if sub, ok := x["data"]; ok {
			switch d := sub.(type) {
			case []any:
				if len(d) > listHits {
					listHits = len(d)
				}
				for _, item := range d {
					collectKnowledgeTargetsFromJSON(item, add)
				}
			case map[string]any:
				subHits := collectKnowledgeTargetsFromJSON(d, add)
				if listHits < 0 && subHits > 0 {
					listHits = subHits
				}
			}
		}
		if listHits >= 0 {
			return listHits
		}
		for _, key := range []string{"title", "knowledge_title", "document_title", "doc_title", "file_name", "filename", "doc_name", "name", "path", "knowledge_id", "document_id", "id"} {
			if s, ok := x[key].(string); ok && strings.TrimSpace(s) != "" {
				add(s)
				return 1
			}
		}
	}
	return 0
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

// Keep upstream HTTP response bodies, hosts and credentials out of diagnostics,
// while preserving structured MCP tool-level validation hints (e.g. invalid knowledge_base_id).
func knowledgeCallError(err error) error {
	var timed net.Error
	msg := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timed) && timed.Timeout():
		return fmt.Errorf("知识服务超时")
	case strings.Contains(msg, "gateway HTTP 403:") || strings.Contains(msg, "gateway HTTP 401:"):
		return fmt.Errorf("知识服务拒绝访问")
	case strings.HasPrefix(msg, "mcp tool ") && !strings.Contains(msg, "http://") && !strings.Contains(msg, "https://") && !strings.Contains(msg, "Bearer "):
		return fmt.Errorf("%s", truncateRunes(strings.TrimSpace(msg), 200))
	default:
		return fmt.Errorf("知识服务不可用或调用失败")
	}
}

const (
	knowledgeCatalogCacheTTL  = 60 * time.Second
	knowledgeCatalogProbeWait = 2 * time.Second
	knowledgeCatalogMaxRunes  = 1600
	knowledgeCatalogMaxDocs   = 20
	knowledgeCatalogCacheMax  = 256
)

type knowledgeCatalogCacheEntry struct {
	xml       string
	expiresAt time.Time
}

type knowledgeCatalogFlight struct {
	done chan struct{}
	xml  string
}

var knowledgeCatalogCache = struct {
	mu       sync.Mutex
	items    map[string]knowledgeCatalogCacheEntry
	inflight map[string]*knowledgeCatalogFlight
}{
	items:    map[string]knowledgeCatalogCacheEntry{},
	inflight: map[string]*knowledgeCatalogFlight{},
}

func resetKnowledgeCatalogCacheForTest() {
	knowledgeCatalogCache.mu.Lock()
	knowledgeCatalogCache.items = map[string]knowledgeCatalogCacheEntry{}
	knowledgeCatalogCache.inflight = map[string]*knowledgeCatalogFlight{}
	knowledgeCatalogCache.mu.Unlock()
}

func knowledgeToolNamespace(serverID string) string {
	sum := sha256.Sum256([]byte(serverID))
	return fmt.Sprintf("kb_%x", sum[:6])
}

func knowledgeServerMeta(d *deps, serverID string) (name, desc string) {
	if serverID == domain.KnowledgeMCPServerID {
		return "团队知识库", "团队内部知识库检索服务"
	}
	if d != nil && d.repo != nil {
		if srv, ok := d.repo.MCPServer(serverID); ok {
			name = strings.TrimSpace(srv.Name)
			desc = strings.TrimSpace(srv.Description)
		}
	}
	if name == "" {
		name = serverID
	}
	return name, desc
}

func isKnowledgeSearchOrReadTool(upstreamName string) bool {
	switch upstreamName {
	case knowledge.ToolSearch, knowledge.ToolFetch, knowledge.ToolListSources,
		"search_knowledge", "grep_chunks", "read_document", "list_documents", "list_knowledge_bases":
		return true
	default:
		return strings.Contains(upstreamName, "search") || strings.Contains(upstreamName, "read") || strings.Contains(upstreamName, "fetch")
	}
}

func knowledgeToolDescription(serverName, serverDesc, serverID, upstreamName, rawDesc string) string {
	label := strings.TrimSpace(serverName)
	if label == "" {
		label = serverID
	}
	header := "[用户已绑定知识库：" + label
	if d := strings.TrimSpace(serverDesc); d != "" {
		header += "（" + clipCatalogText(d, 80) + "）"
	}
	header += "]"
	desc := strings.TrimSpace(rawDesc)
	if desc != "" {
		desc = header + " · " + desc
	} else {
		desc = header + " · " + upstreamName
	}
	if isKnowledgeSearchOrReadTool(upstreamName) {
		desc += "；涉及个人档案、团队规范、项目架构、排障或内部事实时优先调用；寒暄或纯通用知识无需调用。"
	}
	return desc
}

// knowledgeReferencePromptContext builds the SystemPrompt section for bound
// knowledge references: policy instructions + a lightweight, cached L0 catalog.
func knowledgeReferencePromptContext(ctx context.Context, d *deps, project string, uid int64, refs []domain.KnowledgeRef) string {
	if len(refs) == 0 {
		return ""
	}
	catalog := buildBoundKnowledgeCatalogXML(ctx, d, project, uid, refs)
	if catalog == "" {
		return domain.KnowledgeLookupInstruction
	}
	return domain.KnowledgeLookupInstruction + "\n" + catalog
}

func buildSOPKnowledgeCatalogXML(ctx context.Context, d *deps, project string, uid int64, refs []domain.KnowledgeRef) string {
	if len(refs) == 0 {
		return ""
	}
	return buildBoundKnowledgeCatalogXMLForMode(ctx, d, "sop", project, uid, refs, func(serverID, toolName string) string {
		mcpID := serverID
		if mcpID != domain.KnowledgeMCPServerID {
			mcpID = domain.KnowledgeMCPServerID + "-" + mcpID
		}
		return "mcp__" + mcpID + "__" + toolName
	})
}

func knowledgeCatalogCacheKey(d *deps, mode, project string, uid int64, refs []domain.KnowledgeRef) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%d", mode, project, uid)
	hasBuiltin := false
	for _, ref := range refs {
		if ref.ServerID == domain.KnowledgeMCPServerID {
			hasBuiltin = true
		}
		sources := append([]string(nil), ref.Sources...)
		sort.Strings(sources)
		fmt.Fprintf(&b, "|%s:%s:%s", ref.ServerID, ref.Mode, strings.Join(sources, ","))
		if ref.ServerID != domain.KnowledgeMCPServerID && d != nil && d.repo != nil {
			if srv, ok := d.repo.MCPServer(ref.ServerID); ok {
				fmt.Fprintf(&b, "@%s#%d", strings.TrimSpace(srv.URL), srv.UpdatedAt.UTC().UnixNano())
			}
		}
	}
	if hasBuiltin && d != nil && d.repo != nil {
		settings := d.repo.KnowledgeSettings()
		rawAllowed := settings.SourcesForProject(project)
		allowed := make([]string, 0, len(rawAllowed))
		seenAllowed := map[string]bool{}
		for _, src := range rawAllowed {
			src = strings.TrimSpace(src)
			if src == "" || seenAllowed[src] {
				continue
			}
			seenAllowed[src] = true
			allowed = append(allowed, src)
		}
		sort.Strings(allowed)
		fmt.Fprintf(&b, "|scope=%s|fp=%s", strings.Join(allowed, ","), settings.ActiveConnectionsFingerprint())
	}
	return b.String()
}

func buildBoundKnowledgeCatalogXML(ctx context.Context, d *deps, project string, uid int64, refs []domain.KnowledgeRef) string {
	return buildBoundKnowledgeCatalogXMLForMode(ctx, d, "chat", project, uid, refs, func(serverID, toolName string) string {
		return knowledgeToolNamespace(serverID) + "__" + toolName
	})
}

func evictKnowledgeCatalogCacheLocked(now time.Time) {
	for k, entry := range knowledgeCatalogCache.items {
		if !now.Before(entry.expiresAt) {
			delete(knowledgeCatalogCache.items, k)
		}
	}
	for len(knowledgeCatalogCache.items) >= knowledgeCatalogCacheMax {
		var oldestKey string
		var oldestTime time.Time
		first := true
		for k, entry := range knowledgeCatalogCache.items {
			if first || entry.expiresAt.Before(oldestTime) {
				oldestKey = k
				oldestTime = entry.expiresAt
				first = false
			}
		}
		if oldestKey == "" {
			break
		}
		delete(knowledgeCatalogCache.items, oldestKey)
	}
}

func buildBoundKnowledgeCatalogXMLForMode(ctx context.Context, d *deps, mode, project string, uid int64, refs []domain.KnowledgeRef, formatTool func(serverID, toolName string) string) string {
	key := knowledgeCatalogCacheKey(d, mode, project, uid, refs)
	now := time.Now()
	knowledgeCatalogCache.mu.Lock()
	if entry, ok := knowledgeCatalogCache.items[key]; ok && now.Before(entry.expiresAt) {
		knowledgeCatalogCache.mu.Unlock()
		return entry.xml
	}
	if flight, ok := knowledgeCatalogCache.inflight[key]; ok {
		knowledgeCatalogCache.mu.Unlock()
		select {
		case <-flight.done:
			return flight.xml
		case <-ctx.Done():
			return ""
		}
	}
	flight := &knowledgeCatalogFlight{done: make(chan struct{})}
	knowledgeCatalogCache.inflight[key] = flight
	knowledgeCatalogCache.mu.Unlock()

	probeCtx, cancel := context.WithTimeout(ctx, knowledgeCatalogProbeWait)
	defer cancel()

	var b strings.Builder
	b.WriteString("<bound_knowledge_bases>\n")
	for _, ref := range refs {
		if b.Len() >= knowledgeCatalogMaxRunes {
			break
		}
		b.WriteString(describeBoundKnowledgeRef(probeCtx, d, project, uid, ref, formatTool))
	}
	b.WriteString("</bound_knowledge_bases>")
	out := clipCatalogText(b.String(), knowledgeCatalogMaxRunes+len("</bound_knowledge_bases>")+32)
	if !strings.HasSuffix(out, "</bound_knowledge_bases>") {
		out += "\n</bound_knowledge_bases>"
	}

	writeNow := time.Now()
	knowledgeCatalogCache.mu.Lock()
	delete(knowledgeCatalogCache.inflight, key)
	evictKnowledgeCatalogCacheLocked(writeNow)
	knowledgeCatalogCache.items[key] = knowledgeCatalogCacheEntry{xml: out, expiresAt: writeNow.Add(knowledgeCatalogCacheTTL)}
	flight.xml = out
	close(flight.done)
	knowledgeCatalogCache.mu.Unlock()
	return out
}

func describeBoundKnowledgeRef(ctx context.Context, d *deps, project string, uid int64, ref domain.KnowledgeRef, formatTool func(serverID, toolName string) string) string {
	name, desc := knowledgeServerMeta(d, ref.ServerID)

	var toolNames []string
	var hasListBases, hasListDocs, hasListSources bool
	if tools, _, err := referenceTools(ctx, d, project, uid, ref); err == nil {
		for _, t := range tools {
			toolNames = append(toolNames, formatTool(ref.ServerID, t.Name))
			switch t.Name {
			case "list_knowledge_bases":
				hasListBases = true
			case "list_documents":
				hasListDocs = true
			case knowledge.ToolListSources:
				hasListSources = true
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "  <kb id=%q name=%q", ref.ServerID, name)
	if len(toolNames) > 0 {
		fmt.Fprintf(&b, " tools=%q", strings.Join(toolNames, ", "))
	}
	b.WriteString(">\n")
	if desc != "" {
		fmt.Fprintf(&b, "    <description>%s</description>\n", escapeXMLText(clipCatalogText(desc, 160)))
	}
	if len(ref.Sources) > 0 {
		fmt.Fprintf(&b, "    <scoped_sources>%s</scoped_sources>\n", escapeXMLText(strings.Join(ref.Sources, ", ")))
	}

	var baseIDs []string
	var samples []string
	if ctx.Err() == nil {
		switch {
		case hasListBases:
			if res, err := callKnowledgeReferenceWithOrigin(ctx, d, project, uid, ref, "list_knowledge_bases", map[string]any{}, domain.KnowledgeCallOriginCatalogProbe); err == nil {
				baseIDs = extractCatalogBaseIDs(res.Text, 5)
				if hasListDocs && len(baseIDs) > 0 {
					for _, baseID := range baseIDs {
						if ctx.Err() != nil || len(samples) >= knowledgeCatalogMaxDocs {
							break
						}
						if docRes, docErr := callKnowledgeReferenceWithOrigin(ctx, d, project, uid, ref, "list_documents", map[string]any{
							"knowledge_base_id": baseID,
							"page_size":         10,
						}, domain.KnowledgeCallOriginCatalogProbe); docErr == nil {
							samples = appendUniqueCatalogItems(samples, extractCatalogTitles(docRes.Text, knowledgeCatalogMaxDocs-len(samples)), knowledgeCatalogMaxDocs)
						}
					}
				}
				if len(samples) == 0 {
					samples = extractCatalogTitles(res.Text, knowledgeCatalogMaxDocs)
				}
			}
		case hasListDocs:
			if res, err := callKnowledgeReferenceWithOrigin(ctx, d, project, uid, ref, "list_documents", map[string]any{"page_size": knowledgeCatalogMaxDocs}, domain.KnowledgeCallOriginCatalogProbe); err == nil {
				samples = extractCatalogTitles(res.Text, knowledgeCatalogMaxDocs)
			}
		case hasListSources:
			if res, err := callKnowledgeReferenceWithOrigin(ctx, d, project, uid, ref, knowledge.ToolListSources, map[string]any{}, domain.KnowledgeCallOriginCatalogProbe); err == nil {
				samples = extractCatalogTitles(res.Text, knowledgeCatalogMaxDocs)
			}
		}
	}
	if len(baseIDs) > 0 {
		fmt.Fprintf(&b, "    <knowledge_base_ids>%s</knowledge_base_ids>\n", escapeXMLText(strings.Join(baseIDs, ", ")))
	}
	if len(samples) > 0 {
		b.WriteString("    <sample_documents>\n")
		for _, item := range samples {
			fmt.Fprintf(&b, "      - %s\n", escapeXMLText(item))
		}
		b.WriteString("    </sample_documents>\n")
	}
	b.WriteString("  </kb>\n")
	return b.String()
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
		for _, key := range []string{"title", "name", "file_name", "filename", "doc_name", "id"} {
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
