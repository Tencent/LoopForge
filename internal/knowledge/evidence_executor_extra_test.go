package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

func TestExtractEvidencesAndCatalogParsing(t *testing.T) {
	// 1. JSON array root + explicit locator + start/end lines + "::" connID extraction.
	arrJSON := `[
		{"source_id":"conn-9::kb-1","document_id":"doc-1","title":"Doc 1","start_line":10,"end_line":25},
		{"document_id":"doc-2","file_name":"b.md","locator":"section 3"}
	]`
	targets, evs, isJSON, count := ExtractEvidences(arrJSON, "default-conn")
	if !isJSON || count != 2 || len(evs) != 2 || len(targets) != 2 {
		t.Fatalf("unexpected array extraction: targets=%v evs=%+v isJSON=%v count=%d", targets, evs, isJSON, count)
	}
	if evs[0].ConnectionID != "conn-9" || evs[0].Locator != "L10-L25" || evs[1].Locator != "section 3" {
		t.Fatalf("unexpected evidence metadata: %+v", evs)
	}

	// 2. Object with chunks + parent metadata (collapsed when chunks have no doc identity, expanded otherwise).
	collapsedJSON := `{"source_id":"kb-1","document_id":"doc-7","title":"Full Doc","chunks":[{"chunk_index":1},{"chunk_index":3}]}`
	_, colEvs, _, colCount := ExtractEvidences(collapsedJSON, "c1")
	if colCount != 2 || len(colEvs) != 1 || colEvs[0].Locator != "#chunk-1..#chunk-3" {
		t.Fatalf("expected collapsed chunk evidence, got count=%d evs=%+v", colCount, colEvs)
	}
	_, emptyChunkEvs, _, emptyChunkCount := ExtractEvidences(`{"document_id":"d","chunks":[]}`, "c1")
	if emptyChunkCount != 0 || len(emptyChunkEvs) != 0 {
		t.Fatalf("expected 0 count for empty chunks, got count=%d evs=%+v", emptyChunkCount, emptyChunkEvs)
	}
	expandedJSON := `{"source_id":"kb-1","chunks":[{"document_id":"d1","chunk_index":0},{"document_id":"d2","chunk_index":2}]}`
	_, expEvs, _, expCount := ExtractEvidences(expandedJSON, "c1")
	if expCount != 2 || len(expEvs) != 2 || expEvs[0].DocumentID != "d1" || expEvs[1].Locator != "#chunk-2" {
		t.Fatalf("expected expanded chunk evidences, got count=%d evs=%+v", expCount, expEvs)
	}

	// 3. Nested data map + custom KnowledgeToolRule.
	rule := &KnowledgeToolRule{
		ListKeys:     []string{"entries"},
		DocIDKeys:    []string{"custom_doc"},
		TitleKeys:    []string{"custom_title"},
		FileNameKeys: []string{"custom_file"},
		SourceIDKeys: []string{"custom_src"},
	}
	nestedJSON := `{"data":{"entries":[{"custom_src":"s1","custom_doc":"d9","custom_title":"T9","custom_file":"f9.md"}]}}`
	_, ruleEvs, _, ruleCount := ExtractEvidencesWithRule(nestedJSON, "c1", rule)
	if ruleCount != 1 || len(ruleEvs) != 1 || ruleEvs[0].SourceID != "s1" || ruleEvs[0].DocumentID != "d9" || ruleEvs[0].FileName != "f9.md" {
		t.Fatalf("expected custom rule extraction, got count=%d evs=%+v", ruleCount, ruleEvs)
	}

	// 4. Markdown extraction (skips [[knowledge:...]] markers).
	mdRaw := "# 1. First Heading\n## 2) Sub Heading\n- **标题：** 架构总览\n- 文件：arch.md\n- [[knowledge:ev_1]] ignored"
	mdTargets, mdEvs, mdIsJSON, _ := ExtractEvidences(mdRaw, "c1")
	if mdIsJSON || len(mdEvs) != 0 || len(mdTargets) != 4 || mdTargets[0] != "First Heading" || mdTargets[3] != "arch.md" {
		t.Fatalf("unexpected markdown target extraction: targets=%v evs=%+v", mdTargets, mdEvs)
	}
	if tEmpty, _, _, cEmpty := ExtractEvidences("   ", "c1"); tEmpty != nil || cEmpty != -1 {
		t.Fatal("expected empty result for blank input")
	}

	// 5. Catalog titles, base IDs, and arg helpers.
	titles := extractCatalogTitles(`{"knowledge_bases":[{"name":"KB1","description":"Core docs"}]}`, 5)
	if len(titles) != 1 || !strings.Contains(titles[0], "KB1 (Core docs)") {
		t.Fatalf("unexpected catalog JSON titles: %v", titles)
	}
	plainTitles := extractCatalogTitles("- Item A\n2. Item B\n{skip}", 5)
	if len(plainTitles) != 2 || plainTitles[0] != "Item A" {
		t.Fatalf("unexpected catalog plain titles: %v", plainTitles)
	}
	baseIDs := extractCatalogBaseIDs(`{"knowledge_bases":[{"id":"kb-a"},{"id":"kb-a"},{"id":"kb-b"}]}`, 5)
	if len(baseIDs) != 2 || baseIDs[1] != "kb-b" {
		t.Fatalf("unexpected baseIDs: %v", baseIDs)
	}
	if len(argStringList("a,b")) != 2 || argStringList("") != nil || argInt(json.Number("42"), 0) != 42 || argInt("bad", 7) != 7 {
		t.Fatal("unexpected arg helper behavior")
	}
}

type catalogVariantConnector struct {
	typ     string
	tools   []mcpclient.Tool
	sources []Source
	text    string
	err     error
}

func (c *catalogVariantConnector) Type() string                 { return c.typ }
func (c *catalogVariantConnector) Check(context.Context) CheckResult { return CheckResult{OK: true} }
func (c *catalogVariantConnector) ListSources(context.Context) ([]Source, error) {
	return c.sources, c.err
}
func (c *catalogVariantConnector) Tools(context.Context) ([]mcpclient.Tool, error) {
	return c.tools, c.err
}
func (c *catalogVariantConnector) CallTool(_ context.Context, tool string, _ map[string]any, _ SourceScope) (CallResult, error) {
	if c.err != nil {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, c.err
	}
	return CallResult{Status: domain.KnowledgeStatusHit, Text: c.text, Citeable: !domain.IsKnowledgeCatalogTool(tool)}, nil
}

type catalogVariantFactory struct {
	typ  string
	conn Connector
}

func (f catalogVariantFactory) Meta() domain.ConnectorTypeMeta {
	return domain.ConnectorTypeMeta{Type: f.typ, Name: f.typ}
}
func (f catalogVariantFactory) ValidateAndNormalize(ep string, cfg domain.KnowledgeConnectorConfig, sec domain.KnowledgeConnectorSecret, _ bool) (string, domain.KnowledgeConnectorConfig, domain.KnowledgeConnectorSecret, error) {
	return ep, cfg, sec, nil
}
func (f catalogVariantFactory) New(ConnectorSpec) (Connector, error) { return f.conn, nil }

func TestExecutorEdgeCasesAndCatalogBranches(t *testing.T) {
	var nilExec *Executor
	if nilExec.Service() != nil {
		t.Fatal("expected nil Service() on nil Executor")
	}
	nilExec.ResetCatalogCacheForTest()
	if _, _, err := nilExec.ReferenceTools(context.Background(), 1, domain.KnowledgeRef{ConnectorID: "c", Mode: "all"}); err == nil {
		t.Fatal("expected error from nil Executor ReferenceTools")
	}
	if _, _, err := nilExec.ExecuteReferenceTool(context.Background(), 1, domain.KnowledgeRef{ConnectorID: "c", Mode: "all"}, "t", nil, ""); err == nil {
		t.Fatal("expected error from nil Executor ExecuteReferenceTool")
	}

	reg := NewRegistry()
	docsConn := &catalogVariantConnector{
		typ:   "docs_only",
		tools: []mcpclient.Tool{{Name: WeKnoraToolListDocuments}, {Name: "read_document"}, {Name: "write_doc", Annotations: json.RawMessage(`{"destructiveHint":true}`)}},
		text:  `{"documents":[{"title":"Guide A"}]}`,
	}
	srcConn := &catalogVariantConnector{
		typ:   "sources_only",
		tools: []mcpclient.Tool{{Name: ToolListSources}},
		text:  `{"sources":[{"name":"Space X"}]}`,
	}
	defConn := &catalogVariantConnector{
		typ:     "default_only",
		tools:   []mcpclient.Tool{{Name: "custom_search"}},
		sources: []Source{{ID: "s-1", Name: "Default Source"}},
		text:    "Unstructured plain answer without heading",
	}
	reg.Register(catalogVariantFactory{typ: "docs_only", conn: docsConn})
	reg.Register(catalogVariantFactory{typ: "sources_only", conn: srcConn})
	reg.Register(catalogVariantFactory{typ: "default_only", conn: defConn})

	svc := NewServiceWithRegistry(newMemoryConnectorStore(), reg, nil)
	exec := NewExecutor(svc)
	ctx := context.Background()

	cDocs, _ := svc.CreateConnector(ctx, 1, ConnectorInput{ID: "c-docs", Name: "Docs", Type: "docs_only", Endpoint: "https://a.example"})
	cSrc, _ := svc.CreateConnector(ctx, 1, ConnectorInput{ID: "c-src", Name: "Src", Type: "sources_only", Endpoint: "https://b.example"})
	cDef, _ := svc.CreateConnector(ctx, 1, ConnectorInput{ID: "c-def", Name: "Def", Type: "default_only", Endpoint: "https://c.example"})
	disabled := false
	cDis, _ := svc.CreateConnector(ctx, 1, ConnectorInput{ID: "c-dis", Name: "Dis", Type: "default_only", Endpoint: "https://d.example", Enabled: &disabled})

	// ToolMayWrite checks.
	if exec.ToolMayWrite(ctx, 1, domain.KnowledgeRef{ConnectorID: cDocs.ID, Mode: "all"}, "") {
		t.Fatal("empty tool name must not be marked writable")
	}
	if !exec.ToolMayWrite(ctx, 1, domain.KnowledgeRef{ConnectorID: cDocs.ID, Mode: "all"}, "write_doc") {
		t.Fatal("annotated destructive tool should be writable")
	}
	if !exec.ToolMayWrite(ctx, 1, domain.KnowledgeRef{ConnectorID: "missing", Mode: "all"}, "delete_file") {
		t.Fatal("fallback name check should catch delete_file")
	}

	// ExecuteReferenceTool: disabled connector -> Forbidden; catalog tool -> non-citable; unstructured fallback -> [[knowledge:<id>]].
	if _, _, err := exec.ExecuteReferenceTool(ctx, 1, domain.KnowledgeRef{ConnectorID: cDis.ID, Mode: "all"}, "custom_search", nil, ""); !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden for disabled connector, got %v", err)
	}
	catRead, catText, err := exec.ExecuteReferenceTool(ctx, 1, domain.KnowledgeRef{ConnectorID: cDocs.ID, Mode: "all"}, WeKnoraToolListDocuments, nil, "")
	if err != nil || strings.Contains(catText, "[[knowledge:") || len(catRead.Evidences) != 0 {
		t.Fatalf("catalog tool should not attach evidences or citation markers: read=%+v text=%s err=%v", catRead, catText, err)
	}
	unRead, unText, err := exec.ExecuteReferenceTool(ctx, 1, domain.KnowledgeRef{ConnectorID: cDef.ID, Mode: "all"}, "custom_search", map[string]any{"query": strings.Repeat("x", 130)}, "")
	if err != nil || !strings.Contains(unText, "[[knowledge:"+unRead.ID+"]]") {
		t.Fatalf("expected fallback citation marker [[knowledge:%s]], got %s err=%v", unRead.ID, unText, err)
	}
	if !strings.HasSuffix(unRead.Query, "…") {
		t.Fatalf("expected truncated Query summary, got %q", unRead.Query)
	}
	if KnowledgeCallQuerySummary(nil) != "" {
		t.Fatal("expected empty query summary for nil args")
	}

	// appendCitationSuffix formatting when Title != FileName.
	suffixed := appendCitationSuffix("body", domain.KnowledgeRead{
		ID: "r1",
		Evidences: []domain.KnowledgeEvidence{{
			ID:         "ev_1",
			SourceName: "TeamKB",
			Title:      "Architecture",
			FileName:   "arch-spec.md",
			Locator:    "L1-L5",
		}},
	}, "Conn")
	if !strings.Contains(suffixed, "Architecture (arch-spec.md)") || !strings.Contains(suffixed, "知识库「TeamKB」") {
		t.Fatalf("unexpected formatted citation suffix: %s", suffixed)
	}

	// BuildCatalogXML covering docs_only, sources_only, default_only, unavailable connector, cache hit & reset.
	refs := []domain.KnowledgeRef{
		{ConnectorID: cDocs.ID, Mode: "all"},
		{ConnectorID: cSrc.ID, Mode: "all"},
		{ConnectorID: cDef.ID, Mode: "allowlist", Sources: []string{"s-1"}},
		{ConnectorID: cDis.ID, Mode: "all"},
	}
	xml1 := exec.BuildCatalogXML(ctx, 1, refs, "chat", nil)
	for _, want := range []string{"Guide A", "Space X", "Default Source", "unavailable="} {
		if !strings.Contains(xml1, want) {
			t.Fatalf("BuildCatalogXML missing %q:\n%s", want, xml1)
		}
	}
	// Cache hit + reset.
	if xml2 := exec.BuildCatalogXML(ctx, 1, refs, "chat", nil); xml2 != xml1 {
		t.Fatal("expected cached XML to match")
	}
	exec.ResetCatalogCacheForTest()
}

func TestServiceCRUDNilUsageAndCheckOverrides(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "svc-crud-secret")
	store := newMemoryConnectorStore()
	svc := NewService(store, nil)
	ctx := context.Background()

	// Usage methods are safe no-ops when store does not implement UsageStore.
	if svc.Usage() != nil {
		t.Fatal("expected nil UsageCounter for plain store")
	}
	if sum := svc.UsageSummary(1, "", 10); len(sum.Connectors) != 0 {
		t.Fatalf("expected empty summary, got %+v", sum)
	}
	if evs := svc.UsageEvents(1, domain.KnowledgeUsageEventFilter{}); len(evs.Items) != 0 {
		t.Fatalf("expected empty events, got %+v", evs)
	}
	if err := svc.MarkUsageCited(ctx, 1, nil); err != nil || svc.ResetUsage(1, "") != nil {
		t.Fatal("expected nil errors from nil-usage MarkUsageCited/ResetUsage")
	}

	// Create errors.
	if _, err := svc.CreateConnector(ctx, 1, ConnectorInput{Name: "X", Type: "unknown"}); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for unknown type, got %v", err)
	}
	if _, err := svc.CreateConnector(ctx, 1, ConnectorInput{Name: " ", Type: domain.ConnectorTypeMCP, Endpoint: "https://mcp.example.com"}); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for empty name, got %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	created, err := svc.CreateConnector(ctx, 1, ConnectorInput{
		ID:       "kc-fixed",
		Name:     "MCP One",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srv.URL,
		Secret:   domain.KnowledgeConnectorSecret{Token: "tok-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateConnector(ctx, 1, ConnectorInput{
		ID:       "kc-fixed",
		Name:     "Dup",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srv.URL,
	}); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for duplicate ID, got %v", err)
	}

	// UpdateConnector: not found, clear secret, disable, re-enable.
	if _, err := svc.UpdateConnector(ctx, 1, "missing", ConnectorInput{Name: "N"}); !isAppErrKind(err, apperr.NotFound) {
		t.Fatalf("expected NotFound, got %v", err)
	}
	disabled := false
	upd, err := svc.UpdateConnector(ctx, 1, created.ID, ConnectorInput{
		Name:        "MCP Updated",
		ClearSecret: true,
		Enabled:     &disabled,
	})
	if err != nil || upd.SecretConfigured || upd.Enabled || upd.Name != "MCP Updated" {
		t.Fatalf("unexpected UpdateConnector result: %+v err=%v", upd, err)
	}
	if _, _, err := svc.ResolveEnabledConnector(1, created.ID); !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden for disabled connector, got %v", err)
	}

	// CheckConnector error branches.
	if chk := svc.CheckConnector(ctx, 1, "", nil); chk.OK || chk.Error == "" {
		t.Fatalf("expected error for empty ID and nil override, got %+v", chk)
	}
	if chk := svc.CheckConnector(ctx, 1, "missing", nil); chk.OK || !strings.Contains(chk.Error, "不存在") {
		t.Fatalf("expected not found error, got %+v", chk)
	}
	if chk := svc.CheckConnector(ctx, 1, "", &ConnectorInput{Type: "nope"}); chk.OK {
		t.Fatal("expected unsupported type check failure")
	}
	if chk := svc.CheckConnector(ctx, 1, created.ID, &ConnectorInput{ClearSecret: true, Secret: domain.KnowledgeConnectorSecret{Token: "tok-2"}}); chk.OK {
		// srv returns plain 200 without JSON-RPC result -> Check returns OK=false safely
	}

	// DeleteConnector: missing -> NotFound, existing -> nil.
	if err := svc.DeleteConnector(1, "missing"); !isAppErrKind(err, apperr.NotFound) {
		t.Fatalf("expected NotFound on DeleteConnector, got %v", err)
	}
	if err := svc.DeleteConnector(1, created.ID); err != nil {
		t.Fatalf("DeleteConnector failed: %v", err)
	}
}

func TestMCPConnectorValidationScopeErrorsAndRetry(t *testing.T) {
	f := newMCPFactory()
	if meta := f.Meta(); meta.Type != domain.ConnectorTypeMCP || !meta.SupportsSubSources {
		t.Fatalf("unexpected mcp meta: %+v", meta)
	}
	for _, bad := range []string{"", "https://a\n.com", "ftp://a.com", "https://u:p@a.com", "http://169.254.169.254/mcp"} {
		if _, _, _, err := f.ValidateAndNormalize(bad, domain.KnowledgeConnectorConfig{}, domain.KnowledgeConnectorSecret{}, false); !isAppErrKind(err, apperr.Invalid) {
			t.Fatalf("expected Invalid for %q, got %v", bad, err)
		}
	}
	if _, _, _, err := f.ValidateAndNormalize("https://a.com/mcp", domain.KnowledgeConnectorConfig{TimeoutSec: 301}, domain.KnowledgeConnectorSecret{}, false); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for timeout >300, got %v", err)
	}
	if _, _, _, err := f.ValidateAndNormalize("https://a.com/mcp", domain.KnowledgeConnectorConfig{Extra: map[string]string{"oauthMode": "bogus"}}, domain.KnowledgeConnectorSecret{}, false); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for bad oauthMode, got %v", err)
	}
	if _, _, _, err := f.ValidateAndNormalize("https://a.com/mcp", domain.KnowledgeConnectorConfig{Extra: map[string]string{"oauthMode": OAuthModeCustom, "oauthTokenUrl": "javascript:1"}}, domain.KnowledgeConnectorSecret{}, false); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for bad oauthTokenUrl, got %v", err)
	}
	_, _, sec, err := f.ValidateAndNormalize("https://a.com/mcp", domain.KnowledgeConnectorConfig{}, domain.KnowledgeConnectorSecret{AuthType: domain.ConnectorAuthOAuth, Headers: map[string]string{"X-Api": "1"}}, false)
	if err != nil || sec.AuthType != domain.ConnectorAuthHeaders {
		t.Fatalf("expected oauthMode=none to reset AuthType to headers, got %+v err=%v", sec, err)
	}

	// parseSourcesFromMCPText & filterMCPSourcesJSON.
	if got := parseSourcesFromMCPText("not-json"); len(got) != 0 {
		t.Fatalf("expected empty sources for non-json, got %+v", got)
	}
	srcs := parseSourcesFromMCPText(`{"spaces":[{"space_id":"sp1","space_name":"Space 1","knowledge_count":4}]}`)
	if len(srcs) != 1 || srcs[0].ID != "sp1" || srcs[0].DocCount != 4 {
		t.Fatalf("unexpected parsed spaces: %+v", srcs)
	}
	filtered := filterMCPSourcesJSON(`{"sources":[{"id":"s1"},{"id":"s2"}]}`, SourceScope{Mode: "allowlist", Sources: []string{"s1"}})
	if !strings.Contains(filtered, "s1") || strings.Contains(filtered, "s2") {
		t.Fatalf("unexpected filtered sources JSON: %s", filtered)
	}
	if filterMCPSourcesJSON("plain-text", SourceScope{Mode: "allowlist", Sources: []string{"s1"}}) != "plain-text" {
		t.Fatal("non-JSON should pass through unchanged")
	}

	// applyScopeToMCPArgs branches.
	if _, err := applyScopeToMCPArgs(mcpclient.Tool{Name: "kb_search"}, nil, SourceScope{Mode: "allowlist"}); err == nil {
		t.Fatal("expected error for empty allowlist")
	}
	if _, err := applyScopeToMCPArgs(mcpclient.Tool{Name: "kb_search"}, map[string]any{"space_id": "s2"}, SourceScope{Mode: "allowlist", Sources: []string{"s1"}}); err == nil {
		t.Fatal("expected error for out-of-scope single source key")
	}
	injected, err := applyScopeToMCPArgs(mcpclient.Tool{
		Name:        "search_spaces",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"space_ids":{"type":"array"}}}`),
	}, nil, SourceScope{Mode: "allowlist", Sources: []string{"s1"}})
	if err != nil || len(injected["space_ids"].([]string)) != 1 {
		t.Fatalf("expected space_ids injected, got %+v err=%v", injected, err)
	}

	// safeError & retry helpers.
	mc := &mcpConnector{spec: ConnectorSpec{Secret: domain.KnowledgeConnectorSecret{Token: "secret-abc"}}}
	if !isAppErrKind(mc.safeError(context.DeadlineExceeded), apperr.Upstream) {
		t.Fatal("expected Upstream on DeadlineExceeded")
	}
	if !isAppErrKind(mc.safeError(&mcpclient.HTTPError{StatusCode: 429}), apperr.Upstream) {
		t.Fatal("expected Upstream on 429")
	}
	if !isAppErrKind(mc.safeError(&mcpclient.HTTPError{StatusCode: 400}), apperr.Invalid) {
		t.Fatal("expected Invalid on 400")
	}
	if !isAppErrKind(mc.safeError(&mcpclient.RPCError{Code: -32602, Message: "bad param"}), apperr.Invalid) {
		t.Fatal("expected Invalid on RPC -32602")
	}
	if got := mc.safeError(errors.New("mcp tool failed: bad arg")); !isAppErrKind(got, apperr.Upstream) || !strings.Contains(got.Error(), "mcp tool failed") {
		t.Fatalf("expected sanitized mcp tool error, got %v", got)
	}
	if got := mc.safeError(errors.New("gateway HTTP 401: secret-abc")); !isAppErrKind(got, apperr.Upstream) || strings.Contains(got.Error(), "secret-abc") {
		t.Fatalf("expected redacted 401 error, got %v", got)
	}

	if isRetryableMCPError(nil) || isRetryableMCPError(context.Canceled) || isRetryableMCPError(&mcpclient.HTTPError{StatusCode: 400}) {
		t.Fatal("non-retryable error reported as retryable")
	}
	if !isRetryableMCPError(&mcpclient.HTTPError{StatusCode: 503}) || !isRetryableMCPError(errors.New("broken pipe")) {
		t.Fatal("expected 503 and broken pipe to be retryable")
	}
	if d := mcpRetryDelay(0, &mcpclient.HTTPError{StatusCode: 429, RetryAfter: "9"}); d != 5*time.Second {
		t.Fatalf("expected capped 5s retry delay, got %v", d)
	}
	if connectorMaxConcurrency(domain.KnowledgeConnectorConfig{Extra: map[string]string{"max_concurrency": "50"}}) != 16 {
		t.Fatal("expected max_concurrency capped at 16")
	}
}

func TestSiYuanAndObsidianRemainingBranches(t *testing.T) {
	// SiYuan remaining validation and error paths.
	sf := newSiYuanFactory()
	if _, _, _, err := sf.ValidateAndNormalize("http://127.0.0.1:6806", domain.KnowledgeConnectorConfig{}, domain.KnowledgeConnectorSecret{}, false); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid when SiYuan lacks token, got %v", err)
	}
	var syMode atomic.Int32
	sySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch syMode.Load() {
		case 1:
			w.WriteHeader(http.StatusUnauthorized)
		case 2:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("internal err"))
		case 3:
			_, _ = w.Write([]byte("{bad-json"))
		case 4:
			_, _ = w.Write([]byte(`{"code":42,"msg":"biz err"}`))
		default:
			switch r.URL.Path {
			case "/api/notebook/lsNotebooks":
				_, _ = w.Write([]byte(`{"code":0,"data":{"notebooks":[{"id":"nb-1","name":"NB1"}]}}`))
			case "/api/filetree/listDocsByPath":
				_, _ = w.Write([]byte(`{"code":0,"data":{"files":[{"id":"d1","name":"Alpha.sy","path":"/d1.sy"}]}}`))
			case "/api/search/fullTextSearchBlock":
				_, _ = w.Write([]byte(`{"code":0,"data":{"blocks":[]}}`))
			}
		}
	}))
	defer sySrv.Close()

	syConn, err := sf.New(ConnectorSpec{ID: "sy-1", Endpoint: sySrv.URL, Secret: domain.KnowledgeConnectorSecret{Token: "sy-tok"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	scopeNB1 := SourceScope{Mode: "allowlist", Sources: []string{"nb-1"}}
	if _, err := syConn.CallTool(ctx, WeKnoraToolListDocuments, nil, scopeNB1); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for empty kbID, got %v", err)
	}
	if _, err := syConn.CallTool(ctx, WeKnoraToolListDocuments, map[string]any{"knowledge_base_id": "nb-2"}, scopeNB1); !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden for out-of-scope notebook, got %v", err)
	}
	listRes, err := syConn.CallTool(ctx, WeKnoraToolListDocuments, map[string]any{"knowledge_base_id": "nb-1", "keyword": "alpha"}, scopeNB1)
	if err != nil || listRes.Hits != 1 || !strings.Contains(listRes.Text, "Alpha") {
		t.Fatalf("unexpected SiYuan list_documents: %+v err=%v", listRes, err)
	}
	emptySearch, err := syConn.CallTool(ctx, WeKnoraToolSearchKnowledge, map[string]any{"query": "missing"}, scopeNB1)
	if err != nil || emptySearch.Status != domain.KnowledgeStatusEmpty {
		t.Fatalf("expected Empty SiYuan search, got %+v err=%v", emptySearch, err)
	}
	for _, m := range []int32{1, 2, 3, 4} {
		syMode.Store(m)
		if chk := syConn.Check(ctx); chk.OK {
			t.Fatalf("expected SiYuan Check failure in mode %d", m)
		}
	}

	// Obsidian remaining path traversal and read/list paths.
	of := newObsidianFactory()
	if _, _, _, err := of.ValidateAndNormalize("http://127.0.0.1:27123", domain.KnowledgeConnectorConfig{}, domain.KnowledgeConnectorSecret{}, false); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid when Obsidian lacks key, got %v", err)
	}
	for _, badPath := range []string{"", "a\\b.md", "a/../b.md", ".hidden.md", "notes/file.txt"} {
		if _, _, err := cleanVaultRelPath(badPath); !isAppErrKind(err, apperr.Invalid) {
			t.Fatalf("expected Invalid for vault path %q, got %v", badPath, err)
		}
	}
	rel, top, err := cleanVaultRelPath("README.md")
	if err != nil || rel != "README.md" || top != obsidianRootSourceID {
		t.Fatalf("unexpected root markdown path result: rel=%q top=%q err=%v", rel, top, err)
	}

	obSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`{"authenticated":true,"status":"OK"}`))
		case "/vault/":
			_, _ = w.Write([]byte(`{"files":["README.md","Sub/"]}`))
		case "/vault/README.md":
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte("# Root Readme\n\nPlain markdown response."))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer obSrv.Close()

	obConn, err := of.New(ConnectorSpec{ID: "ob-1", Endpoint: obSrv.URL, Secret: domain.KnowledgeConnectorSecret{Token: "ob-tok"}})
	if err != nil {
		t.Fatal(err)
	}
	rootDocs, err := obConn.CallTool(ctx, WeKnoraToolListDocuments, map[string]any{"knowledge_base_id": obsidianRootSourceID}, SourceScope{Mode: "all"})
	if err != nil || rootDocs.Hits != 1 || !strings.Contains(rootDocs.Text, "README.md") {
		t.Fatalf("expected root README.md in list_documents, got %+v err=%v", rootDocs, err)
	}
	plainRead, err := obConn.CallTool(ctx, WeKnoraToolReadDocument, map[string]any{"knowledge_id": "README.md"}, SourceScope{Mode: "all"})
	if err != nil || !strings.Contains(plainRead.Text, "Plain markdown response.") {
		t.Fatalf("expected plain markdown read_document, got %+v err=%v", plainRead, err)
	}
}
