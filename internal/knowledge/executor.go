package knowledge

import (
	"context"
	"crypto/rand"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

const (
	catalogCacheTTL  = 60 * time.Second
	catalogProbeWait = 2 * time.Second
	catalogMaxRunes  = 1600
	catalogMaxDocs   = 20
	catalogCacheMax  = 256
)

type catalogCacheEntry struct {
	xml       string
	expiresAt time.Time
}

type catalogFlight struct {
	done chan struct{}
	xml  string
}

// Executor coordinates runtime read-only execution across a user's bound
// KnowledgeConnectors, builds structured citations/evidence suffixes, and probes
// L0 <bound_knowledge_bases> catalogs with caching and singleflight deduplication.
type Executor struct {
	svc *Service

	mu       sync.Mutex
	catalog  map[string]catalogCacheEntry
	inflight map[string]*catalogFlight
}

// NewExecutor creates an Executor backed by Service.
func NewExecutor(svc *Service) *Executor {
	return &Executor{
		svc:      svc,
		catalog:  map[string]catalogCacheEntry{},
		inflight: map[string]*catalogFlight{},
	}
}

// Service returns the underlying Connector Service.
func (e *Executor) Service() *Service {
	if e == nil {
		return nil
	}
	return e.svc
}

// ResetCatalogCacheForTest clears cached L0 catalog XML entries in tests.
func (e *Executor) ResetCatalogCacheForTest() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.catalog = map[string]catalogCacheEntry{}
	e.inflight = map[string]*catalogFlight{}
	e.mu.Unlock()
}

// ReferenceTools validates ref and discovers the read-only tools exposed by the
// connector belonging to ownerUserID.
func (e *Executor) ReferenceTools(ctx context.Context, ownerUserID int64, ref domain.KnowledgeRef) (domain.KnowledgeConnector, []mcpclient.Tool, error) {
	if err := domain.ValidateKnowledgeRefs([]domain.KnowledgeRef{ref}); err != nil {
		return domain.KnowledgeConnector{}, nil, err
	}
	if e == nil || e.svc == nil {
		return domain.KnowledgeConnector{}, nil, ErrConnectorNotFound
	}
	meta, conn, err := e.svc.ResolveEnabledConnector(ownerUserID, ref.ConnectorID)
	if err != nil {
		return domain.KnowledgeConnector{}, nil, err
	}
	tools, err := conn.Tools(ctx)
	if err != nil {
		return domain.KnowledgeConnector{}, nil, err
	}
	return meta, tools, nil
}

// ToolMayWrite reports whether the named tool on ref may mutate external data.
func (e *Executor) ToolMayWrite(ctx context.Context, ownerUserID int64, ref domain.KnowledgeRef, toolName string) bool {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return false
	}
	_, tools, err := e.ReferenceTools(ctx, ownerUserID, ref)
	if err == nil {
		for _, t := range tools {
			if t.Name == toolName {
				return IsKnowledgeToolMayWrite(t)
			}
		}
	}
	return IsKnowledgeToolMayWrite(mcpclient.Tool{Name: toolName})
}

// ExecuteReferenceTool executes a single read-only tool call on a user-owned
// connector, builds a KnowledgeRead with evidence IDs, and appends the
// citation marker suffix for the LLM when the result is citable.
func (e *Executor) ExecuteReferenceTool(
	ctx context.Context,
	ownerUserID int64,
	ref domain.KnowledgeRef,
	tool string,
	args map[string]any,
	origin string,
) (domain.KnowledgeRead, string, error) {
	return e.ExecuteReferenceToolWithHistory(ctx, ownerUserID, ref, tool, args, origin, nil)
}

// ExecuteReferenceToolWithHistory behaves like ExecuteReferenceTool while also
// correlating document metadata from prior reads in the same session or node.
func (e *Executor) ExecuteReferenceToolWithHistory(
	ctx context.Context,
	ownerUserID int64,
	ref domain.KnowledgeRef,
	tool string,
	args map[string]any,
	origin string,
	priorReads []domain.KnowledgeRead,
) (domain.KnowledgeRead, string, error) {
	if origin == "" {
		origin = domain.KnowledgeCallOriginModelTool
	}
	read := domain.KnowledgeRead{
		ID:       rand.Text(),
		Origin:   origin,
		ServerID: ref.ConnectorID,
		Tool:     strings.TrimSpace(tool),
		Query:    KnowledgeCallQuerySummary(args),
		Status:   domain.KnowledgeStatusUnavailable,
	}
	if err := domain.ValidateKnowledgeRefs([]domain.KnowledgeRef{ref}); err != nil {
		read.Warning = clipCatalogText(err.Error(), 200)
		return read, "", err
	}
	if e == nil || e.svc == nil {
		read.Warning = ErrConnectorNotFound.Error()
		return read, "", ErrConnectorNotFound
	}
	meta, conn, err := e.svc.ResolveEnabledConnector(ownerUserID, ref.ConnectorID)
	if err != nil {
		read.Warning = clipCatalogText(err.Error(), 200)
		return read, "", err
	}
	read.ServerID = meta.ID
	rule := resolveKnowledgeToolRule(meta.Config.Extra, read.Tool)
	read.ReadFull = rule.Role == ToolRoleRead || domain.IsKnowledgeReadTool(read.Tool)

	res, callErr := conn.CallTool(ctx, read.Tool, args, SourceScope{
		Mode:    ref.Mode,
		Sources: ref.Sources,
	})
	if callErr != nil {
		read.Status = domain.KnowledgeStatusUnavailable
		read.Warning = clipCatalogText(firstNonEmpty(res.Warning, callErr.Error()), 200)
		if usage := e.svc.Usage(); usage != nil {
			_ = usage.RecordExecution(ctx, ownerUserID, read, callErr)
		}
		return read, "", callErr
	}

	read.Warning = clipCatalogText(res.Warning, 200)
	read.Targets = append([]string(nil), res.Targets...)
	if res.Status != "" {
		read.Status = res.Status
	} else {
		read.Status = domain.KnowledgeStatusHit
	}

	text := strings.TrimSpace(res.Text)
	if origin == domain.KnowledgeCallOriginCatalogProbe || !domain.IsNativeRESTConnectorType(meta.Type) {
		text = truncateRunes(text, 32000)
	}
	if rule.Role == ToolRoleCatalog || domain.IsKnowledgeCatalogTool(read.Tool) || !res.Citeable {
		read.Evidences = nil
		if usage := e.svc.Usage(); usage != nil {
			_ = usage.RecordExecution(ctx, ownerUserID, read, nil)
		}
		return read, text, nil
	}

	if len(res.Evidences) > 0 {
		read.Evidences = append([]domain.KnowledgeEvidence(nil), res.Evidences...)
	}
	enrichReadEvidences(&read, text, args, rule, priorReads, meta.ID)

	if len(read.Evidences) > 0 {
		prefix := knowledgeEvidenceIDPrefix(read.ID)
		evidences := make([]domain.KnowledgeEvidence, len(read.Evidences))
		for i, ev := range read.Evidences {
			if strings.TrimSpace(ev.ID) == "" {
				ev.ID = fmt.Sprintf("%s_%d", prefix, i+1)
			}
			if strings.TrimSpace(ev.ConnectionID) == "" {
				ev.ConnectionID = meta.ID
			}
			evidences[i] = ev
		}
		read.Evidences = evidences
	}

	if domain.IsKnowledgeCitableStatus(read.Status) {
		text = appendCitationSuffix(text, read, meta.Name)
	}
	if usage := e.svc.Usage(); usage != nil {
		_ = usage.RecordExecution(ctx, ownerUserID, read, nil)
	}
	return read, text, nil
}

func appendCitationSuffix(text string, read domain.KnowledgeRead, connectorName string) string {
	if len(read.Evidences) == 0 {
		return text + "\n若输出引用本次结果，请紧跟附上 [[knowledge:" + read.ID + "]] 标记（勿放入反引号）。"
	}
	var b strings.Builder
	b.WriteString(text)
	b.WriteString("\n证据引用编号（仅在直接采用对应条目事实时，于该句末尾紧跟输出对应的 [[knowledge:<id>]]，勿放入反引号；每句只标直接支撑本句的 ID，不多标、不漏标）：")
	for _, ev := range read.Evidences {
		label := firstNonEmpty(ev.FileName, ev.Title, ev.DocumentID, ev.SourceID)
		if ev.Title != "" && ev.FileName != "" && !strings.EqualFold(ev.Title, ev.FileName) {
			base := strings.TrimSuffix(ev.FileName, filepath.Ext(ev.FileName))
			if !strings.EqualFold(ev.Title, base) {
				label = ev.Title + " (" + ev.FileName + ")"
			}
		}
		kbLabel := firstNonEmpty(ev.SourceName, ev.SourceID, connectorName)
		if kbLabel != "" && kbLabel != label {
			label = "知识库「" + kbLabel + "」 / " + label
		}
		if ev.Locator != "" {
			fmt.Fprintf(&b, "\n- [[knowledge:%s]] %s (%s)", ev.ID, label, ev.Locator)
		} else {
			fmt.Fprintf(&b, "\n- [[knowledge:%s]] %s", ev.ID, label)
		}
	}
	return b.String()
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

// KnowledgeCallQuerySummary extracts a human-readable summary of a tool call's
// primary query parameter for telemetry and run detail views.
func KnowledgeCallQuerySummary(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	for _, key := range []string{"query", "keyword", "knowledge_id", "document_id", "doc_id", "file_id", "page_id", "path", "url", "id"} {
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

// BuildCatalogXML builds a cached L0 <bound_knowledge_bases> summary block for
// Chat or SOP prompts, strictly scoped to connectors owned by ownerUserID.
func (e *Executor) BuildCatalogXML(
	ctx context.Context,
	ownerUserID int64,
	refs []domain.KnowledgeRef,
	mode string,
	formatTool func(conn domain.KnowledgeConnector, toolName string) string,
) string {
	if e == nil || e.svc == nil || len(refs) == 0 {
		return ""
	}
	key := e.catalogCacheKey(ownerUserID, mode, refs)
	now := time.Now()

	e.mu.Lock()
	if entry, ok := e.catalog[key]; ok && now.Before(entry.expiresAt) {
		e.mu.Unlock()
		return entry.xml
	}
	if flight, ok := e.inflight[key]; ok {
		e.mu.Unlock()
		select {
		case <-flight.done:
			return flight.xml
		case <-ctx.Done():
			return ""
		}
	}
	flight := &catalogFlight{done: make(chan struct{})}
	e.inflight[key] = flight
	e.mu.Unlock()

	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), catalogProbeWait)
	defer cancel()

	var b strings.Builder
	b.WriteString("<bound_knowledge_bases>\n")
	for _, ref := range refs {
		if b.Len() >= catalogMaxRunes {
			break
		}
		b.WriteString(e.describeBoundRef(probeCtx, ownerUserID, ref, formatTool))
	}
	b.WriteString("</bound_knowledge_bases>")
	out := clipCatalogTextKeepingNewlines(b.String(), catalogMaxRunes+len("</bound_knowledge_bases>")+32)
	if !strings.HasSuffix(out, "</bound_knowledge_bases>") {
		out += "\n</bound_knowledge_bases>"
	}

	writeNow := time.Now()
	e.mu.Lock()
	delete(e.inflight, key)
	e.evictCatalogLocked(writeNow)
	e.catalog[key] = catalogCacheEntry{xml: out, expiresAt: writeNow.Add(catalogCacheTTL)}
	flight.xml = out
	close(flight.done)
	e.mu.Unlock()
	return out
}

func (e *Executor) catalogCacheKey(ownerUserID int64, mode string, refs []domain.KnowledgeRef) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%d", mode, ownerUserID)
	for _, ref := range refs {
		sources := append([]string(nil), ref.Sources...)
		sort.Strings(sources)
		fmt.Fprintf(&b, "|%s:%s:%s", ref.ConnectorID, ref.Mode, strings.Join(sources, ","))
		if meta, ok := e.svc.GetConnector(ownerUserID, ref.ConnectorID); ok {
			fmt.Fprintf(&b, "@%t#%s#%d", meta.Enabled, meta.Endpoint, meta.UpdatedAt.UTC().UnixNano())
		}
	}
	return b.String()
}

func (e *Executor) evictCatalogLocked(now time.Time) {
	for k, entry := range e.catalog {
		if !now.Before(entry.expiresAt) {
			delete(e.catalog, k)
		}
	}
	for len(e.catalog) >= catalogCacheMax {
		var oldestKey string
		var oldestTime time.Time
		first := true
		for k, entry := range e.catalog {
			if first || entry.expiresAt.Before(oldestTime) {
				oldestKey = k
				oldestTime = entry.expiresAt
				first = false
			}
		}
		if oldestKey == "" {
			break
		}
		delete(e.catalog, oldestKey)
	}
}

func (e *Executor) describeBoundRef(
	ctx context.Context,
	ownerUserID int64,
	ref domain.KnowledgeRef,
	formatTool func(conn domain.KnowledgeConnector, toolName string) string,
) string {
	meta, tools, err := e.ReferenceTools(ctx, ownerUserID, ref)
	if err != nil {
		var b strings.Builder
		fmt.Fprintf(&b, "  <kb id=%q unavailable=%q />\n", ref.ConnectorID, err.Error())
		return b.String()
	}

	var toolNames []string
	var hasListBases, hasListDocs, hasListSources bool
	for _, t := range tools {
		if formatTool != nil {
			toolNames = append(toolNames, formatTool(meta, t.Name))
		} else {
			toolNames = append(toolNames, t.Name)
		}
		switch t.Name {
		case WeKnoraToolListKnowledgeBases:
			hasListBases = true
		case WeKnoraToolListDocuments:
			hasListDocs = true
		case ToolListSources:
			hasListSources = true
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "  <kb id=%q name=%q type=%q", meta.ID, meta.Name, meta.Type)
	if len(toolNames) > 0 {
		fmt.Fprintf(&b, " tools=%q", strings.Join(toolNames, ", "))
	}
	b.WriteString(">\n")
	if len(ref.Sources) > 0 {
		fmt.Fprintf(&b, "    <scoped_sources>%s</scoped_sources>\n", escapeXMLText(strings.Join(ref.Sources, ", ")))
	}

	var baseIDs []string
	var samples []string
	if ctx.Err() == nil {
		switch {
		case hasListBases:
			if _, text, callErr := e.ExecuteReferenceTool(ctx, ownerUserID, ref, WeKnoraToolListKnowledgeBases, map[string]any{}, domain.KnowledgeCallOriginCatalogProbe); callErr == nil {
				baseIDs = extractCatalogBaseIDs(text, 5)
				if hasListDocs && len(baseIDs) > 0 {
					for _, baseID := range baseIDs {
						if ctx.Err() != nil || len(samples) >= catalogMaxDocs {
							break
						}
						if _, docText, docErr := e.ExecuteReferenceTool(ctx, ownerUserID, ref, WeKnoraToolListDocuments, map[string]any{
							"knowledge_base_id": baseID,
							"page_size":         10,
						}, domain.KnowledgeCallOriginCatalogProbe); docErr == nil {
							samples = appendUniqueCatalogItems(samples, extractCatalogTitles(docText, catalogMaxDocs-len(samples)), catalogMaxDocs)
						}
					}
				}
				if len(samples) == 0 {
					samples = extractCatalogTitles(text, catalogMaxDocs)
				}
			}
		case hasListDocs:
			if _, docText, callErr := e.ExecuteReferenceTool(ctx, ownerUserID, ref, WeKnoraToolListDocuments, map[string]any{"page_size": catalogMaxDocs}, domain.KnowledgeCallOriginCatalogProbe); callErr == nil {
				samples = extractCatalogTitles(docText, catalogMaxDocs)
			}
		case hasListSources:
			if _, srcText, callErr := e.ExecuteReferenceTool(ctx, ownerUserID, ref, ToolListSources, map[string]any{}, domain.KnowledgeCallOriginCatalogProbe); callErr == nil {
				samples = extractCatalogTitles(srcText, catalogMaxDocs)
			}
		default:
			if sources, srcErr := e.svc.ListConnectorSources(ctx, ownerUserID, ref.ConnectorID); srcErr == nil {
				scope := SourceScope{Mode: ref.Mode, Sources: ref.Sources}
				for _, src := range sources {
					if !scope.Allows(src.ID) {
						continue
					}
					if len(baseIDs) < 5 && strings.TrimSpace(src.ID) != "" {
						baseIDs = append(baseIDs, strings.TrimSpace(src.ID))
					}
					if len(samples) < catalogMaxDocs {
						label := clipCatalogText(firstNonEmpty(src.Name, src.ID), 80)
						if label != "" {
							samples = appendUniqueCatalogItems(samples, []string{label}, catalogMaxDocs)
						}
					}
				}
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

func clipCatalogTextKeepingNewlines(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}
