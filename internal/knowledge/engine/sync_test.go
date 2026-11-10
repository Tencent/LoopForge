package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/knowledge/engine/gitsync"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGitSyncContentSurvivesCacheAndDeletion(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MySQL DSN not set")
	}
	s, err := repo.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	script, err := os.ReadFile("../../../migrations/026_builtin_knowledge.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range s5SplitSQL(string(script)) {
		if err = s.DB.Exec(stmt).Error; err != nil && !s5IsDuplicateSchemaError(err) {
			t.Fatal(err)
		}
	}
	remote := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = remote
		if out, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git: %s %v", out, e)
		}
	}
	git("init", "-b", "main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	os.Mkdir(filepath.Join(remote, "docs"), 0755)
	os.WriteFile(filepath.Join(remote, "docs", "guide.md"), []byte("# Guide\n\n麒麟服务器配置\n"), 0644)
	os.WriteFile(filepath.Join(remote, "other.md"), []byte("# Outside\n"), 0644)
	git("add", ".")
	git("commit", "-m", "initial")
	id := "sync-integration"
	defer s.DeleteKnowledgeSource(id)
	defer s.ReplaceGitKnowledge(context.Background(), id, nil, nil)
	src := domain.KnowledgeSource{ID: id, Kind: "git", GitURL: "https://example.com/public.git", Branch: "main", PathPrefix: "docs/", Status: "pending"}
	s.PutKnowledgeSource(src)
	e := NewEngine(s, s, NewMySQLKeywordIndex(s))
	cache := t.TempDir()
	w := NewSyncWorker(e, s, s, cache, "test")
	w.syncGit = func(ctx context.Context, dir, url, branch string, old map[string]string) (string, string, []gitsync.FileInfo, []string, []string, error) {
		return gitsync.Sync(ctx, dir, remote, branch, old)
	}
	run := func() {
		t.Helper()
		if err := w.SyncOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		src, _ = s.KnowledgeSource(id)
		if src.Status != "ready" {
			t.Fatalf("%+v", src)
		}
	}
	// Hold the clone phase across multiple TTLs; the heartbeat must exclude a peer.
	originalSync := w.syncGit
	w.ttl = 600 * time.Millisecond
	started := make(chan struct{})
	release := make(chan struct{})
	w.syncGit = func(ctx context.Context, dir, url, branch string, old map[string]string) (string, string, []gitsync.FileInfo, []string, []string, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return "", "", nil, nil, nil, ctx.Err()
		}
		return originalSync(ctx, dir, url, branch, old)
	}
	finished := make(chan error, 1)
	go func() { finished <- w.SyncOnce(context.Background()) }()
	<-started
	time.Sleep(1300 * time.Millisecond)
	if ok, err := s.ClaimKnowledgeSync(id, "peer", time.Second); err != nil || ok {
		t.Fatalf("heartbeat failed: %v %v", ok, err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	w.syncGit = originalSync
	src, _ = s.KnowledgeSource(id)
	if src.Status != "ready" {
		t.Fatalf("sync: %+v", src)
	}
	if len(s.KnowledgeDocuments(id)) != 1 {
		t.Fatal("prefix filter")
	}
	os.RemoveAll(w.cacheDir(id))
	if _, err = e.Fetch(context.Background(), id, "docs/guide.md", 0, 0); err != nil {
		t.Fatal(err)
	}
	hits, err := e.Search(context.Background(), "麒麟", []string{id})
	if err != nil || len(hits.Results) == 0 {
		t.Fatalf("search %v %v", hits, err)
	}
	src.Status = "pending"
	s.PutKnowledgeSource(src)
	run()
	git("rm", "docs/guide.md")
	git("commit", "-m", "delete")
	src.Status = "pending"
	s.PutKnowledgeSource(src)
	run()
	hits, err = e.Search(context.Background(), "麒麟", []string{id})
	if err != nil || len(hits.Results) != 0 {
		t.Fatalf("deleted hit %v %v", hits, err)
	}
	if _, err = e.Fetch(context.Background(), id, "docs/guide.md", 0, 0); err == nil {
		t.Fatal("deleted body survived")
	}
	src.Status = "pending"
	src.GitURL = remote
	if err = s.PutKnowledgeSource(src); err != nil {
		t.Fatal(err)
	}
	if err = w.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	src, _ = s.KnowledgeSource(id)
	if src.Status != "error" || src.LastError == "" || src.LeaseOwner != "" {
		t.Fatalf("invalid remote status: %+v", src)
	}

}

type roleProbe struct{ called chan struct{} }

func (p *roleProbe) KnowledgeSources(string) []domain.KnowledgeSource {
	p.called <- struct{}{}
	return nil
}
func (p *roleProbe) ClaimKnowledgeSync(string, string, time.Duration) (bool, error) {
	panic("unexpected")
}
func (p *roleProbe) RenewKnowledgeSync(string, string, time.Duration) (bool, error) {
	panic("unexpected")
}
func (p *roleProbe) FinishKnowledgeSync(string, string, string, int, string) (bool, error) {
	panic("unexpected")
}
func TestAPIRoleDoesNotStartKnowledgeSync(t *testing.T) {
	p := &roleProbe{make(chan struct{}, 1)}
	w := NewSyncWorker(nil, p, nil, t.TempDir(), "api")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.StartSyncLoop(ctx, false)
	select {
	case <-p.called:
		t.Fatal("API polled sources")
	case <-time.After(50 * time.Millisecond):
	}
	w.StartSyncLoop(ctx, true)
	select {
	case <-p.called:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
}

func (p *roleProbe) PublishKnowledgeSync(context.Context, string, string, string, int, func(context.Context) error) (bool, error) {
	panic("unexpected")
}

func TestSyncCacheOutsideSandboxWorkspaceSurvivesReclaim(t *testing.T) {
	workspaceRoot := t.TempDir()
	cacheRoot := filepath.Join(t.TempDir(), "knowledge-cache")
	w := NewSyncWorker(nil, nil, nil, cacheRoot, "test")
	cache := w.cacheDir("src")
	if rel, err := filepath.Rel(workspaceRoot, cache); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("sync cache %q is inside sandbox workspace %q", cache, workspaceRoot)
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(cache, "pack")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(workspaceRoot, "persist-run-stale")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	removed, err := runtime.NewManager(workspaceRoot).ReclaimExcept(nil)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("reclaimed %d workspaces, want 1", removed)
	}
	if body, err := os.ReadFile(marker); err != nil || string(body) != "keep" {
		t.Fatalf("sandbox reclaim touched the sync cache: %v %q", err, body)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale workspace survived reclaim: %v", err)
	}
}

func TestImportGitWritesIntoConfiguredCacheRoot(t *testing.T) {
	cacheRoot := filepath.Join(t.TempDir(), "knowledge-cache")
	w := NewSyncWorker(nil, nil, nil, cacheRoot, "test")
	var got string
	w.syncGit = func(_ context.Context, dir, _, _ string, _ map[string]string) (string, string, []gitsync.FileInfo, []string, []string, error) {
		got = dir
		return "", "", nil, nil, nil, errors.New("stop after path check")
	}
	src := domain.KnowledgeSource{ID: "src", GitURL: "https://example.com/public.git", Branch: "main"}
	if _, _, err := w.importGit(context.Background(), src, "owner"); err == nil {
		t.Fatal("expected the injected sync error")
	}
	if want := filepath.Join(cacheRoot, "src"); got != want {
		t.Fatalf("sync cache dir = %q, want %q", got, want)
	}
}
