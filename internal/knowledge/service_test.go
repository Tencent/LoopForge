package knowledge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

type fakeStore struct {
	settings    domain.KnowledgeSettings
	submissions map[string]domain.KnowledgeSubmission
	artifacts   map[string]domain.Artifact
	runs        map[string]domain.Run
	agents      []domain.AgentDefinition
	workflows   []domain.WorkflowDefinition
	candidates  []domain.KnowledgeCandidate
}

func newFakeStore() *fakeStore {
	return &fakeStore{submissions: map[string]domain.KnowledgeSubmission{}, artifacts: map[string]domain.Artifact{}, runs: map[string]domain.Run{}}
}

func (f *fakeStore) KnowledgeSettings() domain.KnowledgeSettings { return f.settings }
func (f *fakeStore) PutKnowledgeSettings(s domain.KnowledgeSettings) error {
	f.settings = s
	return nil
}
func (f *fakeStore) KnowledgeSubmissions(projectID, status string, limit int) []domain.KnowledgeSubmission {
	var out []domain.KnowledgeSubmission
	for _, item := range f.submissions {
		if (projectID == "" || item.ProjectID == projectID) && (status == "" || item.Status == status) {
			out = append(out, item)
		}
	}
	return out
}
func (f *fakeStore) KnowledgeSubmission(id string) (domain.KnowledgeSubmission, bool) {
	item, ok := f.submissions[id]
	return item, ok
}
func (f *fakeStore) KnowledgeSubmissionIDsByArtifact(ids []string) map[string]string {
	out := map[string]string{}
	for _, item := range f.submissions {
		for _, id := range ids {
			if item.ArtifactID == id {
				out[id] = item.ID
			}
		}
	}
	return out
}
func (f *fakeStore) PutKnowledgeSubmission(item domain.KnowledgeSubmission) error {
	f.submissions[item.ID] = item
	return nil
}
func (f *fakeStore) DeleteKnowledgeSubmission(id string) error {
	delete(f.submissions, id)
	return nil
}
func (f *fakeStore) KnowledgeArtifacts(string, int) []domain.KnowledgeCandidate {
	return append([]domain.KnowledgeCandidate(nil), f.candidates...)
}
func (f *fakeStore) Artifact(runID, id string) (domain.Artifact, bool) {
	art, ok := f.artifacts[id]
	return art, ok && art.RunID == runID
}
func (f *fakeStore) Run(id string) (domain.Run, bool) {
	run, ok := f.runs[id]
	return run, ok
}
func (f *fakeStore) Agents() []domain.AgentDefinition       { return f.agents }
func (f *fakeStore) Workflows() []domain.WorkflowDefinition { return f.workflows }

type fakeUpstream struct {
	sources   []Source
	err       error
	searched  []string
	written   struct{ source, path, content string }
	writeErr  error
	fetchArgs []any
}

func (u *fakeUpstream) Tools(context.Context) ([]mcpclient.Tool, error) {
	return []mcpclient.Tool{{Name: ToolSearch}}, nil
}
func (u *fakeUpstream) ListSources(context.Context) ([]Source, error) { return u.sources, u.err }
func (u *fakeUpstream) Search(_ context.Context, query string, sources []string) (SearchResult, error) {
	u.searched = sources
	return SearchResult{Query: query}, u.err
}
func (u *fakeUpstream) Fetch(_ context.Context, source, path string, start, end int) (Document, error) {
	u.fetchArgs = []any{source, path, start, end}
	return Document{Source: source, Path: path}, u.err
}
func (u *fakeUpstream) WriteDocument(_ context.Context, source, path, content string) (WriteResult, error) {
	u.written.source, u.written.path, u.written.content = source, path, content
	if u.writeErr != nil {
		return WriteResult{}, u.writeErr
	}
	return WriteResult{Source: source, Path: path, Commit: "abc123"}, nil
}

func newTestService(store *fakeStore, up *fakeUpstream) *Service {
	svc := NewService(store, func() string { return "id1" })
	svc.dial = func(s domain.KnowledgeSettings) (Upstream, error) {
		if strings.TrimSpace(s.URL) == "" || strings.TrimSpace(s.Token) == "" {
			return nil, ErrNotConfigured
		}
		return up, nil
	}
	svc.now = func() time.Time { return time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC) }
	return svc
}

func readySettings() domain.KnowledgeSettings {
	return domain.KnowledgeSettings{
		Enabled: true, URL: "http://kb/mcp", Token: "tok", WriteEnabled: true,
		ProjectSources: map[string][]string{"p1": {"docs"}},
	}
}

func wantKind(t *testing.T, err error, kind apperr.Kind) *apperr.Error {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok || ae.Kind != kind {
		t.Fatalf("want apperr kind %d, got %v", kind, err)
	}
	return ae
}

func TestUpdateSettingsValidatesAndKeepsToken(t *testing.T) {
	store := newFakeStore()
	store.settings = domain.KnowledgeSettings{URL: "http://kb/mcp", Token: "old"}
	svc := newTestService(store, &fakeUpstream{})

	_, err := svc.UpdateSettings(SettingsInput{URL: "ftp://kb"}, "alice")
	wantKind(t, err, apperr.Invalid)

	saved, err := svc.UpdateSettings(SettingsInput{Enabled: true, URL: " http://kb/mcp ", DefaultSources: []string{" a ", "a", ""},
		ProjectSources: map[string][]string{" p1 ": {"b"}, " ": {"x"}}}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Token != "old" || saved.URL != "http://kb/mcp" || saved.UpdatedBy != "alice" {
		t.Fatalf("token should be kept, url trimmed: %+v", saved)
	}
	if strings.Join(saved.DefaultSources, ",") != "a" || len(saved.ProjectSources) != 1 || saved.ProjectSources["p1"][0] != "b" {
		t.Fatalf("sources not cleaned: %+v", saved)
	}

	saved, _ = svc.UpdateSettings(SettingsInput{URL: "http://kb/mcp", Token: "Bearer new"}, "alice")
	if saved.Token != "new" {
		t.Fatalf("Bearer prefix should be stripped, got %q", saved.Token)
	}

	_, err = svc.UpdateSettings(SettingsInput{Enabled: true, URL: "http://kb/mcp", ClearToken: true}, "alice")
	wantKind(t, err, apperr.Invalid)
	if store.settings.Token != "new" {
		t.Fatal("rejected update must not persist")
	}
	saved, _ = svc.UpdateSettings(SettingsInput{URL: "http://kb/mcp", ClearToken: true}, "alice")
	if saved.Token != "" {
		t.Fatal("ClearToken should drop the saved token")
	}
}

func TestCheckUsesOverridesAndReportsFailure(t *testing.T) {
	store := newFakeStore()
	up := &fakeUpstream{}
	svc := newTestService(store, up)
	if res := svc.Check(context.Background(), "", ""); res.OK || res.Error == "" {
		t.Fatalf("unconfigured check should fail: %+v", res)
	}
	res := svc.Check(context.Background(), "http://kb/mcp", "Bearer t")
	if !res.OK || res.Sources == nil || len(res.Tools) != 1 {
		t.Fatalf("override check should succeed with non-nil sources: %+v", res)
	}
	up.err = errors.New("boom")
	if res := svc.Check(context.Background(), "http://kb/mcp", "t"); res.OK || res.Error != "boom" {
		t.Fatalf("upstream error should surface: %+v", res)
	}
}

func TestSearchAndDocumentAreProjectScoped(t *testing.T) {
	store := newFakeStore()
	up := &fakeUpstream{}
	svc := newTestService(store, up)

	_, err := svc.Search(context.Background(), "p1", "q", nil)
	wantKind(t, err, apperr.Unavailable)

	store.settings = readySettings()
	_, err = svc.Search(context.Background(), "p1", "  ", nil)
	wantKind(t, err, apperr.Invalid)
	_, err = svc.Search(context.Background(), "p1", "q", []string{"secret"})
	wantKind(t, err, apperr.Invalid)
	out, err := svc.Search(context.Background(), "p1", "q", nil)
	if err != nil || strings.Join(out.Sources, ",") != "docs" || strings.Join(up.searched, ",") != "docs" {
		t.Fatalf("search should default to project scope: %+v %v", out, err)
	}

	_, err = svc.Document(context.Background(), "p1", "secret", "a.md", 0, 0)
	wantKind(t, err, apperr.Forbidden)
	_, err = svc.Document(context.Background(), "p1", "docs", "", 0, 0)
	wantKind(t, err, apperr.Invalid)
	if _, err := svc.Document(context.Background(), "p1", " docs ", " a.md ", 3, 9); err != nil || up.fetchArgs[0] != "docs" || up.fetchArgs[2] != 3 {
		t.Fatalf("document fetch: %v %v", err, up.fetchArgs)
	}
	up.err = errors.New("down")
	_, err = svc.Document(context.Background(), "p1", "docs", "a.md", 0, 0)
	wantKind(t, err, apperr.Upstream)
}

func TestSourcesMarksAllowed(t *testing.T) {
	store := newFakeStore()
	store.settings = readySettings()
	svc := newTestService(store, &fakeUpstream{sources: []Source{{ID: "docs"}, {ID: "secret"}}})
	list, err := svc.Sources(context.Background(), "p1")
	if err != nil || len(list) != 2 || !list[0].Allowed || list[1].Allowed {
		t.Fatalf("allowed flags wrong: %+v %v", list, err)
	}
	all, _ := svc.Sources(context.Background(), "other")
	if !all[1].Allowed {
		t.Fatal("projects without an entry should see every source")
	}
}

func TestUsageListsVisibleKnowledgeAgents(t *testing.T) {
	store := newFakeStore()
	store.agents = []domain.AgentDefinition{
		{ID: "b", Name: "B", ProjectID: "p1", KnowledgeBase: true},
		{ID: "a", Name: "A", Shared: true, KnowledgeBase: true},
		{ID: "c", Name: "C", ProjectID: "p1"},
		{ID: "x", Name: "X", ProjectID: "p2", KnowledgeBase: true},
	}
	store.workflows = []domain.WorkflowDefinition{
		{Name: "wf1", Nodes: []domain.WorkflowNode{{AgentID: "b"}, {AgentID: "b"}}},
		{Name: "wf2", Nodes: []domain.WorkflowNode{{AgentID: "b"}}},
	}
	agents, total := newTestService(store, nil).Usage("p1")
	if total != 3 || len(agents) != 2 || agents[0].ID != "a" || agents[0].Workflows == nil {
		t.Fatalf("usage: total=%d %+v", total, agents)
	}
	if strings.Join(agents[1].Workflows, ",") != "wf1,wf2" {
		t.Fatalf("workflows should be unique per agent: %v", agents[1].Workflows)
	}
}

func TestSubmitFromArtifact(t *testing.T) {
	store := newFakeStore()
	store.artifacts["art1"] = domain.Artifact{ID: "art1", RunID: "run1", NodeID: "n1",
		Content: "---\ntitle: 部署指南\nsource: docs\npath: guides/deploy.md\n---\n正文"}
	store.runs["run1"] = domain.Run{ID: "run1", WorkflowID: "wf", ExecutionContext: domain.TelemetryContext{ProjectID: "p1", IssueID: "i1"}}
	svc := newTestService(store, nil)

	_, err := svc.Submit(SubmissionInput{RunID: "run1", ArtifactID: "missing"}, "bob")
	wantKind(t, err, apperr.NotFound)

	item, err := svc.Submit(SubmissionInput{RunID: "run1", ArtifactID: "art1"}, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if item.ID != "kbs-id1" || item.Title != "部署指南" || item.Source != "docs" || item.Path != "guides/deploy.md" ||
		item.ProjectID != "p1" || item.IssueID != "i1" || item.WorkflowID != "wf" || item.CreatedBy != "bob" ||
		item.Status != domain.KnowledgeSubmissionPending || strings.TrimSpace(item.Content) != "正文" {
		t.Fatalf("submission from artifact: %+v", item)
	}

	_, err = svc.Submit(SubmissionInput{RunID: "run1", ArtifactID: "art1"}, "bob")
	if ae := wantKind(t, err, apperr.Conflict); ae.Fields["id"] != "kbs-id1" {
		t.Fatalf("conflict should carry the existing id: %+v", ae)
	}

	_, err = svc.Submit(SubmissionInput{Content: "  "}, "bob")
	wantKind(t, err, apperr.Invalid)
	raw, err := svc.Submit(SubmissionInput{Content: "x"}, "bob")
	if err != nil || raw.Title != "未命名知识" {
		t.Fatalf("raw submission default title: %+v %v", raw, err)
	}
}

func TestPublishedSubmissionsAreImmutable(t *testing.T) {
	store := newFakeStore()
	store.submissions["s"] = domain.KnowledgeSubmission{ID: "s", Status: domain.KnowledgeSubmissionPublished}
	svc := newTestService(store, nil)
	_, err := svc.Edit("s", SubmissionEdit{Title: "x"})
	wantKind(t, err, apperr.Conflict)
	wantKind(t, svc.Delete("s"), apperr.Conflict)
	_, err = svc.Reject("s", "no", "r")
	wantKind(t, err, apperr.Conflict)
	_, err = svc.Publish(context.Background(), "s", "r")
	wantKind(t, err, apperr.Conflict)
	_, err = svc.Submission("nope")
	wantKind(t, err, apperr.NotFound)
}

func TestEditResetsReviewState(t *testing.T) {
	store := newFakeStore()
	store.submissions["s"] = domain.KnowledgeSubmission{ID: "s", Title: "T", Content: "old", Status: domain.KnowledgeSubmissionRejected, Error: "e"}
	item, err := newTestService(store, nil).Edit("s", SubmissionEdit{Source: " docs ", Path: " a.md ", ReviewNote: "fix"})
	if err != nil || item.Title != "T" || item.Content != "old" || item.Source != "docs" || item.Path != "a.md" ||
		item.Status != domain.KnowledgeSubmissionPending || item.Error != "" || item.ReviewNote != "fix" {
		t.Fatalf("edit: %+v %v", item, err)
	}
}

func TestPublishGatesAndWrites(t *testing.T) {
	store := newFakeStore()
	up := &fakeUpstream{}
	svc := newTestService(store, up)
	put := func(item domain.KnowledgeSubmission) {
		item.ID, item.Status = "s", domain.KnowledgeSubmissionPending
		store.submissions["s"] = item
	}
	put(domain.KnowledgeSubmission{ProjectID: "p1", Title: "T", Source: "docs", Path: "a.md", Content: "body"})

	_, err := svc.Publish(context.Background(), "s", "r")
	wantKind(t, err, apperr.Unavailable)

	store.settings = readySettings()
	store.settings.WriteEnabled = false
	_, err = svc.Publish(context.Background(), "s", "r")
	wantKind(t, err, apperr.Forbidden)
	store.settings.WriteEnabled = true

	put(domain.KnowledgeSubmission{ProjectID: "p1", Path: "a.md", Content: "body"})
	_, err = svc.Publish(context.Background(), "s", "r")
	wantKind(t, err, apperr.Invalid)
	put(domain.KnowledgeSubmission{ProjectID: "p1", Source: "secret", Path: "a.md", Content: "body"})
	_, err = svc.Publish(context.Background(), "s", "r")
	wantKind(t, err, apperr.Forbidden)
	put(domain.KnowledgeSubmission{ProjectID: "p1", Source: "docs", Path: "../etc/passwd", Content: "body"})
	_, err = svc.Publish(context.Background(), "s", "r")
	wantKind(t, err, apperr.Invalid)
	if up.written.path != "" {
		t.Fatal("nothing should be written before all gates pass")
	}

	put(domain.KnowledgeSubmission{ProjectID: "p1", Title: "T", Source: "docs", Path: "a.md", Content: "body"})
	up.writeErr = errors.New("rejected")
	_, err = svc.Publish(context.Background(), "s", "r")
	wantKind(t, err, apperr.Upstream)
	if failed := store.submissions["s"]; failed.Error != "rejected" || failed.Status != domain.KnowledgeSubmissionPending || failed.ReviewedBy != "r" {
		t.Fatalf("upstream failure should be recorded: %+v", failed)
	}

	up.writeErr = nil
	item, err := svc.Publish(context.Background(), "s", "r")
	if err != nil {
		t.Fatal(err)
	}
	if up.written.content != "# T\n\nbody" || up.written.source != "docs" {
		t.Fatalf("title should be prepended: %+v", up.written)
	}
	if item.Status != domain.KnowledgeSubmissionPublished || item.Commit != "abc123" || item.Error != "" || item.PublishedAt.IsZero() {
		t.Fatalf("publish result: %+v", item)
	}
}

func TestRejectAndCandidates(t *testing.T) {
	store := newFakeStore()
	store.submissions["s"] = domain.KnowledgeSubmission{ID: "s", ArtifactID: "art1", Status: domain.KnowledgeSubmissionPending}
	store.candidates = []domain.KnowledgeCandidate{{Artifact: domain.Artifact{ID: "art1"}}, {Artifact: domain.Artifact{ID: "art2"}}}
	svc := newTestService(store, nil)
	list := svc.Candidates("p1")
	if list[0].SubmissionID != "s" || list[1].SubmissionID != "" {
		t.Fatalf("candidates should link queued submissions: %+v", list)
	}
	item, err := svc.Reject("s", "  dup  ", "r")
	if err != nil || item.Status != domain.KnowledgeSubmissionRejected || item.ReviewNote != "dup" || item.ReviewedBy != "r" {
		t.Fatalf("reject: %+v %v", item, err)
	}
}
