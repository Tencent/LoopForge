package preview

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"github.com/klauspost/compress/zstd"
)

type cvSPreviewHost struct {
	workspace string
	facts     domain.RunnerWorkspaceFacts
	factsOK   bool
	queued    []domain.RunnerPreviewArchiveSpec
	released  []string
	cancelled []string
	events    []string
	queueErr  error
}

type cvSErrorReader struct{}

func (cvSErrorReader) Read([]byte) (int, error) { return 0, errors.New("read failure") }

func (h *cvSPreviewHost) RunWorkspace(string) string { return h.workspace }
func (h *cvSPreviewHost) RunWorkspaceFacts(string) (domain.RunnerWorkspaceFacts, bool) {
	return h.facts, h.factsOK
}

func TestPreviewConfigParsing(t *testing.T) {
	for _, tc := range []struct {
		raw    string
		scheme string
		host   string
		ok     bool
	}{
		{"preview.example.com/path?q=1", "https", "preview.example.com", true},
		{"http://localhost:8080/x", "http", "localhost:8080", true},
		{"", "", "", false},
		{"ftp://example.com", "", "", false},
		{"https://user@example.com", "", "", false},
		{"://bad", "", "", false},
	} {
		scheme, host, ok := SplitPublicURL(tc.raw)
		if scheme != tc.scheme || host != tc.host || ok != tc.ok {
			t.Fatalf("SplitPublicURL(%q) = %q %q %v", tc.raw, scheme, host, ok)
		}
	}
	t.Setenv("AGENTFLOW_PREVIEW_PUBLIC_URL", "http://preview.example.test/base")
	t.Setenv("AGENTFLOW_PREVIEW_MAX_FILES", "123")
	t.Setenv("AGENTFLOW_PREVIEW_MAX_COMPRESSED", "456")
	t.Setenv("AGENTFLOW_PREVIEW_MAX_EXPANDED", "789")
	t.Setenv("AGENTFLOW_PREVIEW_TTL", "2h")
	t.Setenv("AGENTFLOW_PREVIEW_FINALIZE_TIMEOUT", "90")
	t.Setenv("AGENTFLOW_PREVIEW_SESSION_TTL", "bad")
	cfg := LoadConfig()
	if cfg.PublicScheme != "http" || cfg.PublicHost != "preview.example.test" ||
		cfg.MaxFiles != 123 || cfg.MaxCompressed != 456 || cfg.MaxExpanded != 789 ||
		cfg.DefaultRetention != 2*time.Hour || cfg.FinalizationTimeout != 90*time.Second ||
		cfg.SessionTTL != DefaultSessionTTL {
		t.Fatalf("config = %#v", cfg)
	}
	for _, key := range []string{"AGENTFLOW_PREVIEW_MAX_FILES", "AGENTFLOW_PREVIEW_MAX_COMPRESSED", "AGENTFLOW_PREVIEW_MAX_EXPANDED", "AGENTFLOW_PREVIEW_TTL"} {
		t.Setenv(key, "-1")
	}
	cfg = LoadConfig()
	if cfg.MaxFiles != DefaultMaxFiles || cfg.MaxCompressed != DefaultMaxCompressed ||
		cfg.MaxExpanded != DefaultMaxExpanded || cfg.DefaultRetention != DefaultRetention {
		t.Fatalf("invalid values not defaulted: %#v", cfg)
	}
	t.Setenv("AGENTFLOW_PREVIEW_MAX_FILES", "999999")
	t.Setenv("AGENTFLOW_PREVIEW_MAX_COMPRESSED", "9999999999")
	t.Setenv("AGENTFLOW_PREVIEW_MAX_EXPANDED", "9999999999")
	t.Setenv("AGENTFLOW_PREVIEW_TTL", "1000h")
	cfg = LoadConfig()
	if cfg.MaxFiles != DefaultMaxFiles || cfg.MaxCompressed != DefaultMaxCompressed ||
		cfg.MaxExpanded != DefaultMaxExpanded || cfg.DefaultRetention != DefaultRetention {
		t.Fatalf("oversized values not clamped: %#v", cfg)
	}
	t.Setenv("CVS_INT", "")
	if intFromEnv("CVS_INT", 7) != 7 || int64FromEnv("CVS_INT", 8) != 8 || durationFromEnv("CVS_INT", time.Second) != time.Second {
		t.Fatal("empty helper defaults")
	}
	t.Setenv("CVS_INT", "nonsense")
	if intFromEnv("CVS_INT", 7) != 7 || int64FromEnv("CVS_INT", 8) != 8 {
		t.Fatal("invalid helper defaults")
	}
	if validSessionToken(strings.Repeat("g", 32)) || validSessionToken(strings.Repeat("a", 31)) {
		t.Fatal("invalid preview token accepted")
	}
	for _, member := range []string{"", "/absolute", "../escape", "."} {
		if _, err := SanitizeArchiveMember(member); err == nil {
			t.Fatalf("archive member %q accepted", member)
		}
	}
}

func TestStrayPreviewNavigation(t *testing.T) {
	svc, _ := cvSPreviewService(t)
	token := strings.Repeat("a", 32)
	tests := []struct {
		name, method, accept, mode, site, referer, host string
		ok                                              bool
	}{
		{"navigate", "GET", "text/html", "navigate", "same-origin", "https://preview.example/preview/" + token + "/page", "preview.example", true},
		{"head fallback", "HEAD", "text/html", "", "", "https://preview.example/preview/" + token + "/", "preview.example", true},
		{"not navigation", "POST", "text/html", "", "", "https://preview.example/preview/" + token + "/", "preview.example", false},
		{"bad referrer", "GET", "text/html", "", "", ":", "preview.example", false},
		{"cross site", "GET", "text/html", "navigate", "cross-site", "https://preview.example/preview/" + token + "/", "preview.example", false},
		{"wrong host", "GET", "text/html", "", "", "https://other.example/preview/" + token + "/", "another.example", false},
		{"wrong path", "GET", "text/html", "", "", "https://preview.example/other", "preview.example", false},
	}
	for _, tc := range tests {
		req := httptest.NewRequest(tc.method, "https://"+tc.host+"/login?q=1", nil)
		req.Header.Set("Accept", tc.accept)
		req.Header.Set("Sec-Fetch-Mode", tc.mode)
		req.Header.Set("Sec-Fetch-Site", tc.site)
		req.Header.Set("Referer", tc.referer)
		target, ok := svc.StrayNavigationTarget(req)
		if ok != tc.ok || (ok && !strings.Contains(target, "/login?q=1")) {
			t.Fatalf("%s = %q %v", tc.name, target, ok)
		}
	}
	if got := injectPreviewViewport(`<html data-af-preview-viewport></html>`); !strings.Contains(got, `data-af-preview-viewport`) {
		t.Fatal("existing viewport marker lost")
	}
	if got := injectPreviewViewport(`<body>x</body>`); !strings.HasPrefix(got, `<style`) {
		t.Fatal("viewport fallback not prepended")
	}
	if got := injectPreviewBackend(`<html><head lang="en"></head></html>`, "/preview/x", "https://api.example"); !strings.Contains(got, `data-af-preview-backend`) {
		t.Fatal("backend not injected into attributed head")
	}
	if got := injectPreviewBackend(`<body>x</body>`, "/preview/x", "https://api.example"); !strings.HasPrefix(got, `<script`) {
		t.Fatal("backend fallback not prepended")
	}
	if got := injectPreviewHistory(`<body>x</body>`, "/preview/x", false); !strings.HasPrefix(got, `<script`) {
		t.Fatal("history fallback not prepended")
	}
	if got := injectPreviewHistory(`<html data-af-preview-history></html>`, "/preview/x", false); !strings.Contains(got, `data-af-preview-history`) {
		t.Fatal("existing history marker lost")
	}
	if got := rewriteRootRelativeRefs("unchanged", ""); got != "unchanged" {
		t.Fatal("empty prefix rewrote refs")
	}
}
func (h *cvSPreviewHost) QueuePreviewArchive(_ domain.Run, spec domain.RunnerPreviewArchiveSpec) error {
	h.queued = append(h.queued, spec)
	return h.queueErr
}
func (h *cvSPreviewHost) CancelPreviewArchive(runID string) { h.cancelled = append(h.cancelled, runID) }
func (h *cvSPreviewHost) ReleaseWorkspace(runID string)     { h.released = append(h.released, runID) }
func (h *cvSPreviewHost) AppendRunEvent(_ string, typ, _ string) {
	h.events = append(h.events, typ)
}

func cvSPreviewService(t *testing.T) (*Service, *repo.Store) {
	t.Helper()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	svc := NewService(st, nil, Config{
		PublicScheme: "https", PublicHost: "preview.example", FrameAncestors: "https://studio.example",
		MaxFiles: 20, MaxCompressed: 1 << 20, MaxExpanded: 1 << 20,
		DefaultRetention: time.Hour, FinalizationTimeout: time.Minute, SessionTTL: time.Minute,
	})
	return svc, st
}

func TestPreviewAcceptUploadBranches(t *testing.T) {
	svc, st := cvSPreviewService(t)
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dist", "index.html"), []byte("<html>upload</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if _, err := ArchiveDir(workspace, "dist", "index.html", svc.limits(), &archive); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "run-upload", WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true, Kind: "static"}}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	token := "upload-token"
	preview := domain.RunPreview{
		ID: "preview-upload", RunID: run.ID, Status: domain.PreviewArchiving, Kind: domain.PreviewKindStatic,
		EntryFile: "index.html", OwnerRunnerID: "runner", UploadTokenHash: hashToken(token),
		UploadMaxBytes: 1 << 20, UploadExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.PutPreview(preview); err != nil {
		t.Fatal(err)
	}
	if err := svc.AcceptUpload(context.Background(), preview.ID, "runner", token, bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Preview(preview.ID); got.Status != domain.PreviewReady {
		t.Fatalf("uploaded preview = %#v", got)
	}
	if err := svc.AcceptUpload(context.Background(), "missing", "runner", token, bytes.NewReader(nil)); err == nil {
		t.Fatal("missing preview upload accepted")
	}
	for i, mutate := range []func(*domain.RunPreview){
		func(p *domain.RunPreview) { p.OwnerRunnerID = "other" },
		func(p *domain.RunPreview) { p.UploadTokenHash = hashToken("other") },
		func(p *domain.RunPreview) { p.UploadExpiresAt = time.Now().Add(-time.Hour) },
		func(p *domain.RunPreview) { p.Status = domain.PreviewFailed },
	} {
		p := preview
		p.ID = "invalid-" + string(rune('a'+i))
		p.RunID = p.ID
		mutate(&p)
		if err := st.PutPreview(p); err != nil {
			t.Fatal(err)
		}
		if err := svc.AcceptUpload(context.Background(), p.ID, "runner", token, bytes.NewReader(nil)); err == nil {
			t.Fatalf("invalid upload accepted: %#v", p)
		}
	}
	limited := preview
	limited.ID, limited.RunID, limited.Status, limited.UploadMaxBytes = "limited-upload", "limited-upload", domain.PreviewPending, 1
	if err := st.PutPreview(limited); err != nil {
		t.Fatal(err)
	}
	if err := svc.AcceptUpload(context.Background(), limited.ID, "runner", token, strings.NewReader("too large")); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("oversized upload = %v", err)
	}
}

func TestPreviewHostFinalizationBranches(t *testing.T) {
	svc, st := cvSPreviewService(t)
	host := &cvSPreviewHost{factsOK: true}
	svc.SetHost(host)
	invalid := domain.Run{ID: "invalid-run", WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true, Kind: "static", SourcePath: "../bad"}}}
	if svc.BeginFinalization(invalid) {
		t.Fatal("invalid preview held workspace")
	}
	if got, ok := st.PreviewByRun(invalid.ID); !ok || got.Status != domain.PreviewFailed {
		t.Fatalf("invalid preview = %#v, %v", got, ok)
	}

	skipped := domain.Run{ID: "skip-run", WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true, Kind: "static", SourcePath: "dist"}}}
	if err := st.PutRun(skipped); err != nil {
		t.Fatal(err)
	}
	if svc.BeginFinalization(skipped) {
		t.Fatal("non-frontend preview held workspace")
	}
	if got, _ := st.PreviewByRun(skipped.ID); got.Status != domain.PreviewSkipped {
		t.Fatalf("skipped preview = %#v", got)
	}

	runner := domain.Run{
		ID: "runner-build", WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true, Kind: "static", SourcePath: "dist"}},
		ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: "runner-1"},
	}
	if err := st.PutRun(runner); err != nil {
		t.Fatal(err)
	}
	if !svc.ArchiveBuiltPreview(runner) || len(host.queued) != 1 || host.queued[0].UploadToken == "" {
		t.Fatalf("runner queue = %#v", host.queued)
	}
	pending, _ := st.PreviewByRun(runner.ID)
	svc.OnArchiveTaskCompleted(domain.RunnerTask{RunID: runner.ID, Action: domain.RunnerTaskActionArchivePreview, Status: "failed", Error: "archive failed"})
	if got, _ := st.Preview(pending.ID); got.Status != domain.PreviewFailed {
		t.Fatalf("failed task preview = %#v", got)
	}
	svc.OnArchiveTaskCompleted(domain.RunnerTask{Action: "other"})
}

func TestPreviewBuildReplacement(t *testing.T) {
	svc, st := cvSPreviewService(t)
	host := &cvSPreviewHost{}
	svc.SetHost(host)
	run := domain.Run{
		ID: "cvs-replace", ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: "runner"},
		WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true, Kind: "static", SourcePath: "dist", IndexFile: "index.html"}},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	existing := domain.RunPreview{
		ID: repo.NewPreviewID(run.ID), RunID: run.ID, Status: domain.PreviewReady, Kind: domain.PreviewKindStatic,
		StorageKey: "old-key", ContentSHA256: "old", CompressedSize: 9, ExpandedSize: 10, FileCount: 2,
		UploadTokenHash: "old-token", CreatedAt: time.Now().Add(-time.Hour), UpdatedAt: time.Now().Add(-time.Hour),
	}
	if err := st.PutPreview(existing); err != nil {
		t.Fatal(err)
	}
	if svc.BeginFinalization(run) {
		t.Fatal("ordinary finalization replaced ready preview")
	}
	if !svc.ArchiveBuiltPreview(run) {
		t.Fatal("build replacement was not queued")
	}
	got, _ := st.Preview(existing.ID)
	if got.Status != domain.PreviewArchiving || got.StorageKey != "" || got.ContentSHA256 != "" ||
		got.CompressedSize != 0 || got.FileCount != 0 || got.UploadTokenHash == "" || len(host.queued) != 1 {
		t.Fatalf("replacement = %#v, queued=%#v", got, host.queued)
	}
	skippedRun := run
	skippedRun.ID = "cvs-replace-skipped"
	if err := st.PutRun(skippedRun); err != nil {
		t.Fatal(err)
	}
	skipped := existing
	skipped.ID, skipped.RunID, skipped.Status, skipped.StorageKey = repo.NewPreviewID(skippedRun.ID), skippedRun.ID, domain.PreviewSkipped, ""
	if err := st.PutPreview(skipped); err != nil {
		t.Fatal(err)
	}
	if svc.BeginFinalization(skippedRun) || !svc.ArchiveBuiltPreview(skippedRun) {
		t.Fatal("skipped replacement branches")
	}
}

func TestPreviewFrontendDetectionBranches(t *testing.T) {
	svc, st := cvSPreviewService(t)
	workspace := t.TempDir()
	run := domain.Run{ID: "detect", WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true, SourcePath: "dist", IndexFile: "index.html"}}}
	if svc.shouldSkipNonFrontend(run, "") != false || !svc.shouldSkipNonFrontend(run, workspace) {
		t.Fatal("non-frontend detection")
	}
	if err := os.MkdirAll(filepath.Join(workspace, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dist", "index.html"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if svc.shouldSkipNonFrontend(run, workspace) || !previewEntryExists(workspace, run.WorkflowSnapshot.Preview) {
		t.Fatal("preview entry not detected")
	}
	if previewEntryExists("", run.WorkflowSnapshot.Preview) || previewEntryExists(workspace, nil) {
		t.Fatal("invalid preview entry detected")
	}
	host := &cvSPreviewHost{}
	svc.SetHost(host)
	if !svc.skipIfNonFrontend(run, t.TempDir(), ErrMissingIndex) || len(host.released) != 1 {
		t.Fatal("missing non-frontend index was not skipped")
	}
	if svc.skipIfNonFrontend(run, workspace, errors.New("other")) {
		t.Fatal("unrelated archive error skipped preview")
	}
	if _, ok := st.PreviewByRun(run.ID); !ok {
		t.Fatal("skip did not persist preview")
	}
}

func TestPreviewReconcileTimeoutAndRunnerBranches(t *testing.T) {
	svc, st := cvSPreviewService(t)
	now := time.Now().UTC()
	svc.now = func() time.Time { return now }
	missing := domain.RunPreview{ID: "reconcile-missing", RunID: "missing-run", Status: domain.PreviewPending, CreatedAt: now}
	if err := st.PutPreview(missing); err != nil {
		t.Fatal(err)
	}
	svc.reconcileOne(missing, now)
	if got, _ := st.Preview(missing.ID); got.Status != domain.PreviewFailed {
		t.Fatalf("missing-run preview = %#v", got)
	}
	run := domain.Run{ID: "reconcile-run", ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	timedOut := domain.RunPreview{ID: "reconcile-timeout", RunID: run.ID, Status: domain.PreviewPending, CreatedAt: now.Add(-2 * time.Hour)}
	if err := st.PutPreview(timedOut); err != nil {
		t.Fatal(err)
	}
	svc.reconcileOne(timedOut, now)
	if got, _ := st.Preview(timedOut.ID); got.Status != domain.PreviewFailed {
		t.Fatalf("timed out preview = %#v", got)
	}
	runnerRun := domain.Run{ID: "reconcile-runner", ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner}}
	if err := st.PutRun(runnerRun); err != nil {
		t.Fatal(err)
	}
	runnerPreview := domain.RunPreview{ID: "reconcile-runner-preview", RunID: runnerRun.ID, Status: domain.PreviewArchiving, OwnerRunnerID: "runner", CreatedAt: now}
	if err := st.PutPreview(runnerPreview); err != nil {
		t.Fatal(err)
	}
	svc.reconcileOne(runnerPreview, now)
	if got, _ := st.Preview(runnerPreview.ID); got.Status != domain.PreviewArchiving {
		t.Fatalf("runner preview changed = %#v", got)
	}
	localRun := domain.Run{ID: "reconcile-local"}
	if err := st.PutRun(localRun); err != nil {
		t.Fatal(err)
	}
	localPreview := domain.RunPreview{ID: "reconcile-local-preview", RunID: localRun.ID, Status: domain.PreviewPending, CreatedAt: now, SourcePath: "dist", EntryFile: "index.html"}
	if err := st.PutPreview(localPreview); err != nil {
		t.Fatal(err)
	}
	svc.reconcileOne(localPreview, now)
	if got, _ := st.Preview(localPreview.ID); got.Status != domain.PreviewPending {
		t.Fatalf("nil-host preview changed = %#v", got)
	}
	badHost := &cvSPreviewHost{}
	svc.SetHost(badHost)
	svc.reconcileOne(localPreview, now)
	if got, _ := st.Preview(localPreview.ID); got.Status != domain.PreviewFailed {
		t.Fatalf("missing-workspace preview = %#v", got)
	}
	ready := runnerPreview
	ready.ID, ready.RunID, ready.Status, ready.StorageKey = "task-ready", "task-ready-run", domain.PreviewReady, "blob"
	if err := st.PutRun(domain.Run{ID: ready.RunID}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutPreview(ready); err != nil {
		t.Fatal(err)
	}
	host := &cvSPreviewHost{}
	svc.SetHost(host)
	svc.OnArchiveTaskCompleted(domain.RunnerTask{RunID: "missing", Action: "other"})
	svc.OnArchiveTaskCompleted(domain.RunnerTask{RunID: "missing", Action: domain.RunnerTaskActionArchivePreview})
	svc.OnArchiveTaskCompleted(domain.RunnerTask{RunID: runnerPreview.RunID, Action: domain.RunnerTaskActionArchivePreview, Status: domain.RunnerTaskSuccess})
	svc.OnArchiveTaskCompleted(domain.RunnerTask{RunID: ready.RunID, Action: domain.RunnerTaskActionArchivePreview, Status: domain.RunnerTaskSuccess})
	if len(host.released) != 1 {
		t.Fatalf("ready release = %#v", host.released)
	}
}

func TestPreviewArchiveAndServe(t *testing.T) {
	svc, st := cvSPreviewService(t)
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "dist", "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dist", "index.html"), []byte(`<html><head></head><body><script src="/assets/app.js"></script></body></html>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dist", "assets", "app.js"), []byte(`console.log("/assets/x.png")`), 0o644); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "run-serve", WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true, Kind: "static", SourcePath: "dist", IndexFile: "index.html", SPAFallback: true}}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	preview := domain.RunPreview{ID: "preview-serve", RunID: run.ID, Kind: domain.PreviewKindStatic, Status: domain.PreviewPending, SourcePath: "dist", EntryFile: "index.html", SPAFallback: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := st.PutPreview(preview); err != nil {
		t.Fatal(err)
	}
	svc.cfg.FinalizationTimeout = 0
	if err := svc.ArchiveLocal(context.Background(), run, preview, workspace); err != nil {
		t.Fatal(err)
	}
	if err := svc.ArchiveLocal(context.Background(), run, preview, workspace); !errors.Is(err, ErrPreviewBusy) {
		t.Fatalf("repeat archive = %v", err)
	}
	ready, _ := st.Preview(preview.ID)
	svc.Skip(run, "must not replace ready")
	if preserved, _ := st.Preview(preview.ID); preserved.Status != domain.PreviewReady {
		t.Fatalf("ready preview was skipped: %#v", preserved)
	}
	_, publicURL, err := svc.CreateSession(ready, httptest.NewRequest("GET", "https://studio.example/", nil), "")
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSuffix(strings.TrimPrefix(publicURL, "https://preview.example/preview/"), "/")
	for _, target := range []string{"/preview/" + token + "/", "/preview/" + token + "/assets/app.js", "/preview/" + token + "/missing-route"} {
		req := httptest.NewRequest("GET", "https://preview.example"+target, nil)
		req.Header.Set("Accept", "text/html")
		rr := httptest.NewRecorder()
		svc.ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatalf("%s status = %d: %s", target, rr.Code, rr.Body.String())
		}
	}
	req := httptest.NewRequest("GET", "https://preview.example/preview/"+token, nil)
	rr := httptest.NewRecorder()
	svc.ServeHTTP(rr, req)
	if rr.Code != 302 {
		t.Fatalf("redirect status = %d", rr.Code)
	}
}

func TestArchiveRejectionMatrix(t *testing.T) {
	workspace := t.TempDir()
	dist := filepath.Join(workspace, "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(dist, "index.html")
	if err := os.WriteFile(index, []byte("0123456789"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "app.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	limits := Limits{MaxFiles: 10, MaxCompressed: 1 << 20, MaxExpanded: 1 << 20}
	for _, tc := range []struct {
		name, source, entry string
		limits              Limits
	}{
		{"bad source", "../dist", "index.html", limits},
		{"bad index", "dist", "../index.html", limits},
		{"missing source", "missing", "index.html", limits},
		{"not directory", "dist/index.html", "index.html", limits},
		{"missing index", "dist", "missing.html", limits},
		{"file count", "dist", "index.html", Limits{MaxFiles: 1, MaxCompressed: 1 << 20, MaxExpanded: 1 << 20}},
		{"expanded", "dist", "index.html", Limits{MaxFiles: 10, MaxCompressed: 1 << 20, MaxExpanded: 1}},
		{"compressed", "dist", "index.html", Limits{MaxFiles: 10, MaxCompressed: 1, MaxExpanded: 1 << 20}},
	} {
		if _, err := ArchiveDir(workspace, tc.source, tc.entry, tc.limits, &bytes.Buffer{}); err == nil {
			t.Fatalf("%s accepted", tc.name)
		}
	}
	if err := os.Symlink("index.html", filepath.Join(dist, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := ArchiveDir(workspace, "dist", "index.html", limits, &bytes.Buffer{}); !errors.Is(err, ErrUnsupportedEntry) {
		t.Fatalf("symlink = %v", err)
	}
	if err := os.Remove(filepath.Join(dist, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(index, filepath.Join(dist, "hard")); err != nil {
		t.Fatal(err)
	}
	if _, err := ArchiveDir(workspace, "dist", "index.html", limits, &bytes.Buffer{}); !errors.Is(err, ErrUnsupportedEntry) {
		t.Fatalf("hardlink = %v", err)
	}
	counter := &countWriter{w: &bytes.Buffer{}, limit: 2}
	if _, err := counter.Write([]byte("abc")); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("count writer = %v", err)
	}
}

func TestTarInspectionAndExtractionMatrix(t *testing.T) {
	makeBundle := func(headers []*tar.Header, bodies [][]byte) []byte {
		var out bytes.Buffer
		zw, err := zstd.NewWriter(&out)
		if err != nil {
			t.Fatal(err)
		}
		tw := tar.NewWriter(zw)
		for i, header := range headers {
			if err := tw.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if i < len(bodies) && bodies[i] != nil {
				if _, err := tw.Write(bodies[i]); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	limits := Limits{MaxFiles: 4, MaxCompressed: 1 << 20, MaxExpanded: 1 << 20}
	valid := makeBundle(
		[]*tar.Header{{Name: "assets/", Typeflag: tar.TypeDir, Mode: 0o755}, {Name: "index.html", Typeflag: tar.TypeReg, Mode: 0o644, Size: 2}},
		[][]byte{nil, []byte("ok")},
	)
	if result, err := InspectTarZst(bytes.NewReader(valid), "index.html", limits); err != nil || result.FileCount != 1 {
		t.Fatalf("inspect valid = %#v, %v", result, err)
	}
	dest := filepath.Join(t.TempDir(), "expanded")
	if err := ExtractTarZst(bytes.NewReader(valid), dest, limits); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(dest, "index.html")); err != nil || string(body) != "ok" {
		t.Fatalf("extracted = %q, %v", body, err)
	}
	if body, err := ReadTarZstMember(bytes.NewReader(valid), "index.html", limits); err != nil || string(body) != "ok" {
		t.Fatalf("member = %q, %v", body, err)
	}
	if _, err := ReadTarZstMember(bytes.NewReader(valid), "missing", limits); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing member = %v", err)
	}
	special := makeBundle([]*tar.Header{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "index.html"}}, nil)
	for _, call := range []func() error{
		func() error { _, err := InspectTarZst(bytes.NewReader(special), "index.html", limits); return err },
		func() error { return ExtractTarZst(bytes.NewReader(special), filepath.Join(t.TempDir(), "x"), limits) },
		func() error { _, err := ReadTarZstMember(bytes.NewReader(special), "link", limits); return err },
	} {
		if err := call(); !errors.Is(err, ErrUnsupportedEntry) {
			t.Fatalf("special archive = %v", err)
		}
	}
	if _, err := InspectTarZst(strings.NewReader("bad"), "index.html", limits); err == nil {
		t.Fatal("malformed archive accepted")
	}
	if _, err := InspectTarZst(bytes.NewReader(valid), "missing", limits); !errors.Is(err, ErrMissingIndex) {
		t.Fatalf("missing index = %v", err)
	}
	oneByte := Limits{MaxFiles: 1, MaxCompressed: 1, MaxExpanded: 1}
	if _, err := InspectTarZst(bytes.NewReader(valid), "index.html", oneByte); err == nil {
		t.Fatal("limited archive accepted")
	}
	if err := ExtractTarZst(bytes.NewReader(valid), filepath.Join(t.TempDir(), "limited"), oneByte); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("limited extract = %v", err)
	}
	if _, err := ReadTarZstMember(bytes.NewReader(valid), "index.html", oneByte); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("limited member = %v", err)
	}
	if err := ExtractTarZst(strings.NewReader("bad"), filepath.Join(t.TempDir(), "bad"), limits); err == nil {
		t.Fatal("malformed extract accepted")
	}
	if _, err := ReadTarZstMember(strings.NewReader("bad"), "index.html", limits); err == nil {
		t.Fatal("malformed member accepted")
	}
	if err := writeDownloadZIP(context.Background(), bytes.NewReader(valid), "missing.html", limits, &bytes.Buffer{}); !errors.Is(err, ErrMissingIndex) {
		t.Fatalf("download missing index = %v", err)
	}
	if err := writeDownloadZIP(context.Background(), bytes.NewReader(special), "link", limits, &bytes.Buffer{}); !errors.Is(err, ErrUnsupportedEntry) {
		t.Fatalf("download special = %v", err)
	}
	duplicate := makeBundle(
		[]*tar.Header{{Name: "index.html", Typeflag: tar.TypeReg, Size: 1}, {Name: "index.html", Typeflag: tar.TypeReg, Size: 1}},
		[][]byte{[]byte("a"), []byte("b")},
	)
	if err := writeDownloadZIP(context.Background(), bytes.NewReader(duplicate), "index.html", limits, &bytes.Buffer{}); err == nil {
		t.Fatal("duplicate download member accepted")
	}
	if err := writeDownloadZIP(context.Background(), bytes.NewReader(valid), "index.html", Limits{MaxFiles: 1, MaxExpanded: 1}, &bytes.Buffer{}); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("limited download = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writeDownloadZIP(canceled, bytes.NewReader(valid), "index.html", limits, &bytes.Buffer{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("download cancellation = %v", err)
	}
}

func TestDBStoreValidation(t *testing.T) {
	_, st := cvSPreviewService(t)
	store := NewDBStore(st)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Put(ctx, "x", strings.NewReader("x"), 1, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled put = %v", err)
	}
	if err := store.Put(context.Background(), "x", strings.NewReader("x"), 2, ""); err == nil {
		t.Fatal("size mismatch accepted")
	}
	if err := store.Put(context.Background(), "x", cvSErrorReader{}, 0, ""); err == nil {
		t.Fatal("reader failure accepted")
	}
	if _, err := containsAny(cvSErrorReader{}, [][]byte{[]byte("needle")}); err == nil {
		t.Fatal("containsAny reader failure accepted")
	}
	if err := store.Put(context.Background(), "x", strings.NewReader("x"), 1, strings.Repeat("0", 64)); err == nil {
		t.Fatal("hash mismatch accepted")
	}
	if err := store.Put(context.Background(), "", strings.NewReader("blob"), 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Open(context.Background(), "missing"); !errors.Is(err, ErrPreviewNotFound) {
		t.Fatalf("missing blob = %v", err)
	}
	if _, _, err := store.Open(ctx, "missing"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled open = %v", err)
	}
	if err := store.Delete(ctx, "missing"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled delete = %v", err)
	}
	if err := store.Delete(context.Background(), "missing"); err != nil {
		t.Fatalf("delete missing = %v", err)
	}
}

func TestPreviewServiceLifecycle(t *testing.T) {
	svc, st := cvSPreviewService(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	run := domain.Run{
		ID: "run-cvs", Status: "succeeded",
		WorkflowSnapshot: domain.WorkflowDefinition{ProjectID: "p", Preview: &domain.PreviewContract{
			Enabled: true, Kind: "static", SourcePath: "dist", IndexFile: "index.html",
		}},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if !svc.BeginFinalization(run) {
		t.Fatal("finalization did not hold")
	}
	preview, ok := st.PreviewByRun(run.ID)
	if !ok || preview.Status != domain.PreviewPending {
		t.Fatalf("preview = %#v", preview)
	}
	if !svc.BeginFinalization(run) {
		t.Fatal("pending preview lost hold")
	}
	svc.Fail(preview, errors.New(strings.Repeat("x", 900)))
	preview, _ = st.Preview(preview.ID)
	if preview.Status != domain.PreviewFailed || len(preview.ErrorText) != 800 {
		t.Fatalf("failed preview = %#v", preview)
	}
	svc.Fail(preview, nil)
	svc.Fail(preview, ErrPreviewBusy)
	svc.Skip(run, "")
}

func TestPreviewSessionsAndDelete(t *testing.T) {
	svc, st := cvSPreviewService(t)
	run := domain.Run{ID: "run-session"}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	preview := domain.RunPreview{
		ID: "preview-session", RunID: run.ID, Kind: domain.PreviewKindStatic,
		Status: domain.PreviewReady, StorageKey: "blob", BackendURL: "https://api.example/default",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := st.PutPreview(preview); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "http://studio.example/x", nil)
	if _, _, err := svc.CreateSession(domain.RunPreview{Status: domain.PreviewPending}, req, ""); err == nil {
		t.Fatal("pending preview session created")
	}
	if _, _, err := svc.CreateSession(preview, req, "://bad"); err == nil {
		t.Fatal("invalid backend session created")
	}
	req.Header.Set("X-Forwarded-Proto", "https, http")
	session, publicURL, err := svc.CreateSession(preview, req, "")
	if err != nil || !strings.HasPrefix(publicURL, "https://preview.example/preview/") {
		t.Fatalf("session = %#v, %q, %v", session, publicURL, err)
	}
	token := strings.TrimSuffix(strings.TrimPrefix(publicURL, "https://preview.example/preview/"), "/")
	if _, got, ok := svc.LookupSession(token); !ok || got.ID != preview.ID {
		t.Fatalf("lookup = %#v, %v", got, ok)
	}
	svc.RenewSession(session)
	if _, err := svc.SetPreviewBackend(preview.ID, "https://other.example/path#frag"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetPreviewBackend(preview.ID, ""); err != nil {
		t.Fatalf("clear backend = %v", err)
	}
	if _, err := svc.SetPreviewBackend("missing", ""); !errors.Is(err, ErrPreviewNotFound) {
		t.Fatalf("missing backend = %v", err)
	}
	if _, err := svc.SetPreviewBackend(preview.ID, "ftp://bad.example"); err == nil {
		t.Fatal("invalid preview backend accepted")
	}
	if _, _, ok := svc.LookupSession("not-a-token"); ok {
		t.Fatal("invalid session token accepted")
	}
	expiredSession, expiredURL, err := svc.CreateSession(preview, req, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DB.Model(&domain.PreviewSession{}).Where("id = ?", expiredSession.ID).Update("expires_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	expiredToken := strings.TrimSuffix(strings.TrimPrefix(expiredURL, "https://preview.example/preview/"), "/")
	if _, _, ok := svc.LookupSession(expiredToken); ok {
		t.Fatal("expired session live")
	}
	expiredPreview := preview
	expiredPreview.ExpiresAt = time.Now().Add(-time.Minute)
	if _, err := svc.OpenDownload(context.Background(), expiredPreview); !errors.Is(err, ErrPreviewExpired) {
		t.Fatalf("expired download = %v", err)
	}
	downloadCtx, downloadCancel := context.WithCancel(context.Background())
	downloadCancel()
	if _, err := svc.OpenDownload(downloadCtx, preview); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled download = %v", err)
	}
	if err := svc.RevokeSession(session.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := svc.LookupSession(token); ok {
		t.Fatal("revoked session live")
	}
	host := &cvSPreviewHost{}
	svc.SetHost(host)
	if err := svc.DeleteForRun(run.ID); err != nil {
		t.Fatal(err)
	}
	if len(host.cancelled) != 1 {
		t.Fatalf("delete cancellation = %#v", host.cancelled)
	}
	if err := svc.DeleteForRun("missing"); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewReconcileAndHelpers(t *testing.T) {
	var nilService *Service
	if nilService.BeginFinalization(domain.Run{}) || nilService.Config() != (Config{}) {
		t.Fatal("nil preview service")
	}
	svc, st := cvSPreviewService(t)
	svc.now = nil
	if svc.clock().IsZero() {
		t.Fatal("default clock")
	}
	if svc.BeginFinalization(domain.Run{ID: "no-contract"}) {
		t.Fatal("run without static contract finalized")
	}
	fallback := NewService(st, nil, Config{})
	if got := fallback.PublicURL(strings.Repeat("a", 32), nil); got != "" {
		t.Fatalf("fallback public URL = %q", got)
	}
	now := time.Now().UTC()
	svc.now = func() time.Time { return now }
	missing := domain.RunPreview{ID: "missing-run", RunID: "none", Status: domain.PreviewPending, CreatedAt: now}
	if err := st.PutPreview(missing); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "expired-run"}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	expired := domain.RunPreview{ID: "expired-preview", RunID: run.ID, Status: domain.PreviewReady, StorageKey: "key", ExpiresAt: now.Add(-time.Minute), CreatedAt: now.Add(-time.Hour)}
	if err := st.PutPreview(expired); err != nil {
		t.Fatal(err)
	}
	svc.Reconcile()
	if got, _ := st.Preview(missing.ID); got.Status != domain.PreviewFailed {
		t.Fatalf("missing run reconcile = %#v", got)
	}
	if got, _ := st.Preview(expired.ID); got.Status != domain.PreviewExpired {
		t.Fatalf("expired reconcile = %#v", got)
	}
	if svc.Config().MaxFiles != 20 || svc.clock().IsZero() {
		t.Fatal("service config/clock")
	}
	var nilSvc *Service
	if nilSvc.Config() != (Config{}) {
		t.Fatal("nil config")
	}
	plain, hash, err := newOpaqueToken()
	if err != nil || !tokenHashEqual(plain, hash) || tokenHashEqual("other", hash) {
		t.Fatal("token helpers")
	}
	req := httptest.NewRequest("GET", "http://host.example/x", nil)
	req.Host = "host.example"
	other := NewService(st, nil, Config{})
	if got := other.PublicURL("t", req); got != "" {
		t.Fatalf("public URL = %q", got)
	}
}
