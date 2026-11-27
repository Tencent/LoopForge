package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

type memoryLegacyStore struct {
	mu          sync.Mutex
	settings    domain.KnowledgeSettings
	submissions map[string]domain.KnowledgeSubmission
	candidates  []domain.KnowledgeCandidate
	artifacts   map[string]domain.Artifact
	runs        map[string]domain.Run
	runErr      error
	putSubErr   error
}

func newMemoryLegacyStore() *memoryLegacyStore {
	return &memoryLegacyStore{
		submissions: map[string]domain.KnowledgeSubmission{},
		artifacts:   map[string]domain.Artifact{},
		runs:        map[string]domain.Run{},
	}
}

func (m *memoryLegacyStore) KnowledgeSettings() domain.KnowledgeSettings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings
}

func (m *memoryLegacyStore) PutKnowledgeSettings(s domain.KnowledgeSettings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings = s
	return nil
}

func (m *memoryLegacyStore) KnowledgeSubmissions(projectID, status string, limit int) []domain.KnowledgeSubmission {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.KnowledgeSubmission
	for _, sub := range m.submissions {
		if projectID != "" && sub.ProjectID != projectID {
			continue
		}
		if status != "" && sub.Status != status {
			continue
		}
		out = append(out, sub)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (m *memoryLegacyStore) KnowledgeSubmission(id string) (domain.KnowledgeSubmission, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sub, ok := m.submissions[id]
	return sub, ok
}

func (m *memoryLegacyStore) KnowledgeSubmissionIDsByArtifact(artifactIDs []string) map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for _, aid := range artifactIDs {
		for _, sub := range m.submissions {
			if sub.ArtifactID == aid {
				out[aid] = sub.ID
				break
			}
		}
	}
	return out
}

func (m *memoryLegacyStore) PutKnowledgeSubmission(sub domain.KnowledgeSubmission) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.putSubErr != nil {
		return m.putSubErr
	}
	m.submissions[sub.ID] = sub
	return nil
}

func (m *memoryLegacyStore) DeleteKnowledgeSubmission(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.submissions, id)
	return nil
}

func (m *memoryLegacyStore) KnowledgeArtifacts(projectID string, limit int) []domain.KnowledgeCandidate {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.KnowledgeCandidate
	for _, c := range m.candidates {
		if projectID != "" && c.ProjectID != projectID {
			continue
		}
		out = append(out, c)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (m *memoryLegacyStore) Artifact(runID, artifactID string) (domain.Artifact, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	art, ok := m.artifacts[runID+"\x00"+artifactID]
	return art, ok
}

func (m *memoryLegacyStore) RunHeader(id string) (domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runErr != nil {
		return domain.Run{}, m.runErr
	}
	return m.runs[id], nil
}

func TestLegacyUpdateSettingsValidationAndTokenPolicy(t *testing.T) {
	store := newMemoryLegacyStore()
	svc := NewLegacyService(store, func() string { return "1" })

	if got := svc.Settings(); got.Enabled {
		t.Fatalf("expected initially disabled settings, got %+v", got)
	}

	// Reject malformed URLs and SSRF protected IPs.
	for _, badURL := range []string{"ftp://kb.example.com/mcp", "http://", "http://169.254.169.254/mcp", "http://100.100.100.200/mcp"} {
		if _, err := svc.UpdateSettings(LegacySettingsInput{Enabled: true, URL: badURL, Token: "tok"}, "alice"); !isAppErrKind(err, apperr.Invalid) {
			t.Fatalf("expected Invalid for URL %q, got %v", badURL, err)
		}
	}

	// Reject enabling without URL or Token.
	if _, err := svc.UpdateSettings(LegacySettingsInput{Enabled: true, URL: "https://kb.example.com/mcp"}, "alice"); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid when enabling without token, got %v", err)
	}

	// Valid save strips "Bearer " and cleans default/project sources.
	saved, err := svc.UpdateSettings(LegacySettingsInput{
		Enabled:        true,
		URL:            "https://kb.example.com/mcp",
		Token:          "Bearer legacy-secret-1",
		WriteEnabled:   true,
		DefaultSources: []string{" src-a ", "src-a", ""},
		ProjectSources: map[string][]string{" proj-1 ": {"src-b", " src-b "}, "   ": {"ignored"}},
	}, "alice")
	if err != nil {
		t.Fatalf("UpdateSettings failed: %v", err)
	}
	if saved.Token != "legacy-secret-1" || saved.UpdatedBy != "alice" || len(saved.DefaultSources) != 1 || len(saved.ProjectSources) != 1 {
		t.Fatalf("unexpected saved settings: %+v", saved)
	}

	// Omitting Token with ClearToken=false preserves the saved token.
	updated, err := svc.UpdateSettings(LegacySettingsInput{
		Enabled: true,
		URL:     "https://kb.example.com/mcp",
	}, "bob")
	if err != nil || updated.Token != "legacy-secret-1" {
		t.Fatalf("expected token to be preserved, got %+v err=%v", updated, err)
	}

	// ClearToken=true wipes token; re-enabling without token fails, disabling succeeds.
	if _, err := svc.UpdateSettings(LegacySettingsInput{Enabled: true, URL: "https://kb.example.com/mcp", ClearToken: true}, "bob"); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid when enabling with ClearToken=true, got %v", err)
	}
	cleared, err := svc.UpdateSettings(LegacySettingsInput{Enabled: false, URL: "https://kb.example.com/mcp", ClearToken: true}, "bob")
	if err != nil || cleared.Token != "" {
		t.Fatalf("expected token cleared on disabled settings, got %+v err=%v", cleared, err)
	}
}

func TestLegacyCheckSourcesSearchDocumentAndMCPClient(t *testing.T) {
	const apiToken = "legacy-mcp-token-xyz"
	var lastAuth string
	var forceBadJSON atomic.Bool
	var forceUpstreamErr atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAuth = r.Header.Get("Authorization")
		if lastAuth != "Bearer "+apiToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": "2024-11-05"}})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"tools": []map[string]any{
						{"name": ToolListSources},
						{"name": ToolSearch},
						{"name": ToolFetch},
						{"name": legacyToolWrite},
					},
				},
			})
		case "tools/call":
			if forceUpstreamErr.Load() {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			if forceBadJSON.Load() {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result":  map[string]any{"content": []map[string]any{{"type": "text", "text": "{not-json"}}},
				})
				return
			}
			name, _ := req.Params["name"].(string)
			args, _ := req.Params["arguments"].(map[string]any)
			var payload string
			switch name {
			case ToolListSources:
				payload = `{"sources":[{"id":"src-eng","name":"研发文档","status":"ready","doc_count":10},{"id":"src-sec","name":"安全规范","status":"ready","doc_count":2}]}`
			case ToolSearch:
				payload = `{"query":"` + argString(args["query"]) + `","mode":"hybrid","results":[{"source":"src-eng","path":"guides/api.md","title":"API 规范","start_line":1,"end_line":20,"snippet":"Bearer auth","score":0.9}]}`
			case ToolFetch:
				payload = `{"source":"src-eng","path":"guides/api.md","start_line":1,"end_line":2,"line_count":2,"lines":[{"number":1,"text":"# API"},{"number":2,"text":"Details"}]}`
			case legacyToolWrite:
				payload = `{"source":"` + argString(args["source"]) + `","path":"` + argString(args["path"]) + `","commit":"abc123"}`
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]any{"content": []map[string]any{{"type": "text", "text": payload}}},
			})
		}
	}))
	defer srv.Close()

	store := newMemoryLegacyStore()
	svc := NewLegacyService(store, func() string { return "1" })
	ctx := context.Background()

	// Unconfigured checks/endpoints fail cleanly with expected error codes.
	if chk := svc.Check(ctx, "", ""); chk.OK || chk.Error == "" {
		t.Fatalf("expected unconfigured Check to fail, got %+v", chk)
	}
	if _, err := svc.Sources(ctx, "proj-1"); !isAppErrKind(err, apperr.Unavailable) {
		t.Fatalf("expected Unavailable from Sources when unconfigured, got %v", err)
	}
	if _, err := svc.Search(ctx, "proj-1", "api", nil); !isAppErrKind(err, apperr.Unavailable) {
		t.Fatalf("expected Unavailable from Search when unconfigured, got %v", err)
	}
	if _, err := svc.Document(ctx, "proj-1", "src-eng", "guides/api.md", 1, 10); !isAppErrKind(err, apperr.Unavailable) {
		t.Fatalf("expected Unavailable from Document when unconfigured, got %v", err)
	}

	// Check with unsaved override URL/Token succeeds and lists tools & sources.
	chk := svc.Check(ctx, srv.URL, "Bearer "+apiToken)
	if !chk.OK || len(chk.Sources) != 2 || len(chk.Tools) != 4 || lastAuth != "Bearer "+apiToken {
		t.Fatalf("unexpected override Check result: %+v auth=%q", chk, lastAuth)
	}

	// Save enabled settings with proj-1 restricted to src-eng.
	if _, err := svc.UpdateSettings(LegacySettingsInput{
		Enabled:        true,
		URL:            srv.URL,
		Token:          apiToken,
		DefaultSources: []string{"src-eng", "src-sec"},
		ProjectSources: map[string][]string{"proj-1": {"src-eng"}},
	}, "admin"); err != nil {
		t.Fatal(err)
	}

	// Sources marks Allowed per project scope.
	views, err := svc.Sources(ctx, "proj-1")
	if err != nil || len(views) != 2 {
		t.Fatalf("Sources failed: views=%+v err=%v", views, err)
	}
	if !views[0].Allowed || views[1].Allowed {
		t.Fatalf("unexpected Allowed flags for proj-1: %+v", views)
	}

	// Search validation & scoping.
	if _, err := svc.Search(ctx, "proj-1", "   ", nil); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for empty search query, got %v", err)
	}
	if _, err := svc.Search(ctx, "proj-1", "api", []string{"src-sec"}); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for out-of-scope source in Search, got %v", err)
	}
	searchOut, err := svc.Search(ctx, "proj-1", "api", nil)
	if err != nil || len(searchOut.Result.Results) != 1 || len(searchOut.Sources) != 1 || searchOut.Sources[0] != "src-eng" {
		t.Fatalf("unexpected Search output: %+v err=%v", searchOut, err)
	}

	// Document validation, cross-project forbidden, and fetch.
	if _, err := svc.Document(ctx, "proj-1", "", "a.md", 0, 0); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for empty source, got %v", err)
	}
	if _, err := svc.Document(ctx, "proj-1", "src-sec", "secret.md", 0, 0); !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden for out-of-scope source in Document, got %v", err)
	}
	doc, err := svc.Document(ctx, "proj-1", "src-eng", "guides/api.md", 1, 2)
	if err != nil || len(doc.Lines) != 2 || doc.Lines[0].Text != "# API" {
		t.Fatalf("unexpected Document output: %+v err=%v", doc, err)
	}

	// Malformed JSON and upstream HTTP errors map to Upstream / failed Check.
	forceBadJSON.Store(true)
	if _, err := svc.Document(ctx, "proj-1", "src-eng", "guides/api.md", 0, 0); !isAppErrKind(err, apperr.Upstream) || !strings.Contains(err.Error(), "无法解析") {
		t.Fatalf("expected Upstream parse error, got %v", err)
	}
	forceBadJSON.Store(false)
	forceUpstreamErr.Store(true)
	if chkErr := svc.Check(ctx, "", ""); chkErr.OK || chkErr.Error == "" {
		t.Fatalf("expected Check failure on upstream error, got %+v", chkErr)
	}
	if _, err := svc.Sources(ctx, "proj-1"); !isAppErrKind(err, apperr.Upstream) {
		t.Fatalf("expected Upstream error on Sources, got %v", err)
	}
	if _, err := svc.Search(ctx, "proj-1", "api", nil); !isAppErrKind(err, apperr.Upstream) {
		t.Fatalf("expected Upstream error on Search, got %v", err)
	}
}

func TestLegacySubmissionsLifecycleAndSecurityBoundaries(t *testing.T) {
	store := newMemoryLegacyStore()
	fixedNow := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	seq := 0
	svc := NewLegacyService(store, func() string {
		seq++
		return string(rune('a' + seq - 1))
	})
	svc.now = func() time.Time { return fixedNow }

	// Seed runs & artifacts for Candidates / Submit testing.
	store.runs["run-personal"] = domain.Run{
		ID:   "run-personal",
		Kind: domain.RunKindWorkspace,
		ExecutionContext: domain.TelemetryContext{
			ProjectID: "",
		},
	}
	store.runs["run-proj"] = domain.Run{
		ID:         "run-proj",
		WorkflowID: "wf-1",
		ExecutionContext: domain.TelemetryContext{
			ProjectID: "proj-1",
			IssueID:   "iss-1",
		},
	}
	store.artifacts["run-proj\x00art-1"] = domain.Artifact{
		ID:     "art-1",
		RunID:  "run-proj",
		NodeID: "node-doc",
		Name:   domain.KnowledgeArtifactBasename,
		Content: "---\n" +
			`title: "Gateway Design"` + "\n" +
			"source: src-eng\n" +
			"path: guides/gateway.md\n" +
			"---\n\n" +
			"Use mTLS for service calls.",
	}
	store.candidates = []domain.KnowledgeCandidate{
		{Artifact: store.artifacts["run-proj\x00art-1"], ProjectID: "proj-1"},
	}

	// 1. Personal workspace artifact MUST be blocked with Forbidden.
	if _, err := svc.Submit(LegacySubmissionInput{RunID: "run-personal", ArtifactID: "art-p"}, "alice"); !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden for personal workspace artifact, got %v", err)
	}
	// Missing run / artifact -> NotFound.
	if _, err := svc.Submit(LegacySubmissionInput{RunID: "run-missing", ArtifactID: "art-x"}, "alice"); !isAppErrKind(err, apperr.NotFound) {
		t.Fatalf("expected NotFound for missing run, got %v", err)
	}
	if _, err := svc.Submit(LegacySubmissionInput{RunID: "run-proj", ArtifactID: "art-missing"}, "alice"); !isAppErrKind(err, apperr.NotFound) {
		t.Fatalf("expected NotFound for missing artifact, got %v", err)
	}
	// RunHeader error propagates.
	store.runErr = errors.New("db down")
	if _, err := svc.Submit(LegacySubmissionInput{RunID: "run-proj", ArtifactID: "art-1"}, "alice"); err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("expected RunHeader error propagation, got %v", err)
	}
	store.runErr = nil

	// Empty body -> Invalid.
	if _, err := svc.Submit(LegacySubmissionInput{Content: "   "}, "alice"); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for empty body, got %v", err)
	}

	// 2. Valid artifact submission parses front-matter, inherits run metadata, and updates Candidates().
	sub, err := svc.Submit(LegacySubmissionInput{RunID: "run-proj", ArtifactID: "art-1"}, "alice")
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}
	if sub.Title != "Gateway Design" || sub.Source != "src-eng" || sub.Path != "guides/gateway.md" ||
		sub.ProjectID != "proj-1" || sub.IssueID != "iss-1" || sub.WorkflowID != "wf-1" || sub.NodeID != "node-doc" {
		t.Fatalf("unexpected submitted item: %+v", sub)
	}
	cands := svc.Candidates("proj-1")
	if len(cands) != 1 || cands[0].SubmissionID != sub.ID {
		t.Fatalf("expected candidate annotated with submission ID %q, got %+v", sub.ID, cands)
	}

	// Duplicate artifact submission -> Conflict with existing id detail.
	if _, dupErr := svc.Submit(LegacySubmissionInput{RunID: "run-proj", ArtifactID: "art-1"}, "alice"); !isAppErrKind(dupErr, apperr.Conflict) {
		t.Fatalf("expected Conflict on duplicate artifact submit, got %v", dupErr)
	}

	// 3. Reject -> Edit (resets to pending) -> manual submission Delete.
	rejected, err := svc.Reject(sub.ID, "  needs detail  ", "reviewer")
	if err != nil || rejected.Status != domain.KnowledgeSubmissionRejected || rejected.ReviewNote != "needs detail" || rejected.ReviewedBy != "reviewer" {
		t.Fatalf("Reject failed: sub=%+v err=%v", rejected, err)
	}
	edited, err := svc.Edit(sub.ID, LegacySubmissionEdit{
		Title:   "Gateway Design v2",
		Source:  "src-eng",
		Path:    "guides\\gateway-v2.md",
		Content: "Updated mTLS notes.",
	})
	if err != nil || edited.Status != domain.KnowledgeSubmissionPending || edited.Title != "Gateway Design v2" || edited.Error != "" {
		t.Fatalf("Edit failed: sub=%+v err=%v", edited, err)
	}
	manual, err := svc.Submit(LegacySubmissionInput{Content: "# Quick Note\n\nBody"}, "alice")
	if err != nil || manual.Title != "Quick Note" {
		t.Fatalf("manual Submit failed: %+v err=%v", manual, err)
	}
	if list := svc.Submissions("proj-1", ""); len(list) != 1 {
		t.Fatalf("expected 1 project submission, got %+v", list)
	}
	if err := svc.Delete(manual.ID); err != nil {
		t.Fatalf("Delete manual failed: %v", err)
	}
	if _, err := svc.Submission(manual.ID); !isAppErrKind(err, apperr.NotFound) {
		t.Fatalf("expected NotFound after Delete, got %v", err)
	}

	// 4. Publish guards and upstream write success/failure.
	ctx := context.Background()
	if _, err := svc.Publish(ctx, sub.ID, "reviewer"); !isAppErrKind(err, apperr.Unavailable) {
		t.Fatalf("expected Unavailable when unconfigured, got %v", err)
	}
	store.settings = domain.KnowledgeSettings{
		Enabled:        true,
		URL:            "https://kb.example.com/mcp",
		Token:          "tok",
		WriteEnabled:   false,
		ProjectSources: map[string][]string{"proj-1": {"src-eng"}},
	}
	if _, err := svc.Publish(ctx, sub.ID, "reviewer"); !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden when WriteEnabled=false, got %v", err)
	}

	store.settings.WriteEnabled = true
	var writeErr error
	var wroteContent string
	var wrotePath string
	svc.dial = func(domain.KnowledgeSettings) (legacyUpstream, error) {
		return stubLegacyUpstream{writeErr: writeErr, onWrite: func(p, c string) {
			wrotePath = p
			wroteContent = c
		}}, nil
	}

	// Empty source, out-of-scope source, and invalid path checks.
	_, _ = svc.Edit(sub.ID, LegacySubmissionEdit{Source: "", Path: "guides/a.md"})
	if _, err := svc.Publish(ctx, sub.ID, "reviewer"); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for empty source, got %v", err)
	}
	_, _ = svc.Edit(sub.ID, LegacySubmissionEdit{Source: "src-other", Path: "guides/a.md"})
	if _, err := svc.Publish(ctx, sub.ID, "reviewer"); !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden for out-of-scope source, got %v", err)
	}
	_, _ = svc.Edit(sub.ID, LegacySubmissionEdit{Source: "src-eng", Path: "guides/bad.txt"})
	if _, err := svc.Publish(ctx, sub.ID, "reviewer"); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for non-.md path, got %v", err)
	}

	// Upstream write failure records item.Error and returns Upstream.
	_, _ = svc.Edit(sub.ID, LegacySubmissionEdit{Source: "src-eng", Path: "guides\\gateway-v2.md"})
	writeErr = errors.New("upstream disk full")
	if _, err := svc.Publish(ctx, sub.ID, "reviewer"); !isAppErrKind(err, apperr.Upstream) {
		t.Fatalf("expected Upstream on write failure, got %v", err)
	}
	failedItem, _ := svc.Submission(sub.ID)
	if failedItem.Error != "upstream disk full" || failedItem.Status != domain.KnowledgeSubmissionPending {
		t.Fatalf("expected error persisted on failed publish, got %+v", failedItem)
	}

	// Upstream write success prepends heading, normalizes path, marks published, and blocks subsequent mutations.
	writeErr = nil
	pub, err := svc.Publish(ctx, sub.ID, "reviewer")
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}
	if pub.Status != domain.KnowledgeSubmissionPublished || pub.Commit != "commit-1" || pub.Error != "" || !pub.PublishedAt.Equal(fixedNow) {
		t.Fatalf("unexpected published submission: %+v", pub)
	}
	if wrotePath != "guides/gateway-v2.md" || !strings.HasPrefix(wroteContent, "# Gateway Design v2\n\n") {
		t.Fatalf("unexpected write payload path=%q content=%q", wrotePath, wroteContent)
	}
	if _, err := svc.Edit(sub.ID, LegacySubmissionEdit{Title: "x"}); !isAppErrKind(err, apperr.Conflict) {
		t.Fatalf("expected Conflict editing published submission, got %v", err)
	}
	if _, err := svc.Reject(sub.ID, "no", "reviewer"); !isAppErrKind(err, apperr.Conflict) {
		t.Fatalf("expected Conflict rejecting published submission, got %v", err)
	}
	if err := svc.Delete(sub.ID); !isAppErrKind(err, apperr.Conflict) {
		t.Fatalf("expected Conflict deleting published submission, got %v", err)
	}
	if _, err := svc.Publish(ctx, sub.ID, "reviewer"); !isAppErrKind(err, apperr.Conflict) {
		t.Fatalf("expected Conflict re-publishing published submission, got %v", err)
	}
}

type stubLegacyUpstream struct {
	writeErr error
	onWrite  func(path, content string)
}

func (s stubLegacyUpstream) Tools(context.Context) ([]mcpclient.Tool, error) { return nil, nil }
func (s stubLegacyUpstream) ListSources(context.Context) ([]LegacySource, error) {
	return nil, nil
}
func (s stubLegacyUpstream) Search(context.Context, string, []string) (LegacySearchResult, error) {
	return LegacySearchResult{}, nil
}
func (s stubLegacyUpstream) Fetch(context.Context, string, string, int, int) (LegacyDocument, error) {
	return LegacyDocument{}, nil
}
func (s stubLegacyUpstream) WriteDocument(_ context.Context, source, docPath, content string) (LegacyWriteResult, error) {
	if s.onWrite != nil {
		s.onWrite(docPath, content)
	}
	if s.writeErr != nil {
		return LegacyWriteResult{}, s.writeErr
	}
	return LegacyWriteResult{Source: source, Path: docPath, Commit: "commit-1"}, nil
}

func TestLegacyParseDistilledNormalizeDocPathAndScopeHelpers(t *testing.T) {
	// ParseDistilled cases.
	p1 := ParseDistilled("---\r\ntitle: 'My Title'\r\nignored-line\r\nsource: kb1\r\npath: a/b.md\r\n---\r\n\r\nBody")
	if p1.Title != "My Title" || p1.Source != "kb1" || p1.Path != "a/b.md" || p1.Body != "Body" {
		t.Fatalf("unexpected ParseDistilled front-matter result: %+v", p1)
	}
	p2 := ParseDistilled("---\ntitle: Unclosed\n# Fallback Heading\nText")
	if p2.Title != "Fallback Heading" {
		t.Fatalf("expected markdown heading fallback on unclosed front-matter, got %+v", p2)
	}

	// NormalizeDocPath cases.
	pathCases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"guides\\deploy guide.md", "guides/deploy-guide.md", true},
		{"../../etc/passwd.md", "etc/passwd.md", true}, // path.Clean("/" + p) clamps traversal to repo root
		{"", "", false},
		{".", "", false},
		{"guides/readme.txt", "", false},
	}
	for _, tc := range pathCases {
		got, ok := NormalizeDocPath(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("NormalizeDocPath(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}

	// LegacyScopeSources & LegacySourceAllowed & clipLegacy.
	if got, err := LegacyScopeSources(nil, []string{"a", " a "}); err != nil || len(got) != 1 || got[0] != "a" {
		t.Fatalf("unexpected open scope result: %v %v", got, err)
	}
	if got, err := LegacyScopeSources([]string{"a", "b"}, nil); err != nil || len(got) != 2 {
		t.Fatalf("unexpected empty request expansion: %v %v", got, err)
	}
	if _, err := LegacyScopeSources([]string{"a"}, []string{"b"}); err == nil {
		t.Fatal("expected error for disallowed source")
	}
	if !LegacySourceAllowed(nil, "anything") || LegacySourceAllowed([]string{"a"}, "b") {
		t.Fatal("unexpected LegacySourceAllowed result")
	}
	if got := clipLegacy("  hello world  ", 5); got != "hello…" {
		t.Fatalf("unexpected clipLegacy: %q", got)
	}
}
