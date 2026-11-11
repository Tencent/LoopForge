package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	"agentflow/internal/knowledge/engine/gitsync"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

type syncFakeControl struct {
	finishOK         bool
	finishedSummary  string
	finishedCount    int
	published        bool
	publishCallbacks int
}

func (c *syncFakeControl) KnowledgeSources(string) []domain.KnowledgeSource { return nil }
func (c *syncFakeControl) ClaimKnowledgeSync(string, string, time.Duration) (bool, error) {
	return true, nil
}
func (c *syncFakeControl) RenewKnowledgeSync(string, string, time.Duration) (bool, error) {
	return true, nil
}
func (c *syncFakeControl) FinishKnowledgeSync(_ string, _ string, _ string, count int, summary string) (bool, error) {
	c.finishedCount = count
	c.finishedSummary = summary
	return c.finishOK, nil
}
func (c *syncFakeControl) PublishKnowledgeSync(ctx context.Context, _ string, _ string, _ string, _ int, publish func(context.Context) error) (bool, error) {
	c.published = true
	c.publishCallbacks++
	return true, publish(ctx)
}

type syncFakeContent struct {
	docs   []domain.KnowledgeDocument
	chunks []domain.KnowledgeChunk
}

func (c *syncFakeContent) ReplaceGitKnowledge(_ context.Context, _ string, docs []domain.KnowledgeDocument, chunks []domain.KnowledgeChunk) error {
	c.docs = docs
	c.chunks = chunks
	return nil
}

func TestSyncFailureMessageIsUserFacing(t *testing.T) {
	cacheRoot := filepath.Join(t.TempDir(), "kb-git-136d7fcc94e3db62")
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"仓库不存在或需要凭据", errors.New("clone 失败: git clone --quiet --branch main -- https://github.com/x/missing.git " + cacheRoot + "/src: fatal: could not read Username for 'https://github.com': terminal prompts disabled"), "仓库鉴权失败，请检查来源凭据及仓库读取权限"},
		{"仓库不存在", errors.New("repository not found"), "仓库不存在，请检查仓库地址及凭据的访问范围"},
		{"认证失败", errors.New("fatal: Authentication failed for 'https://github.com/x/private.git/'"), "仓库鉴权失败，请检查来源凭据及仓库读取权限"},
		{"分支不存在", errors.New("fatal: Remote branch nope not found in upstream origin"), "分支不存在，请检查分支名"},
		{"网络不可达", errors.New("fatal: unable to access 'https://github.com/x/y.git/': Could not resolve host: github.com"), "无法连接仓库服务器，请检查网络或地址"},
		{"连接超时", errors.New("fatal: unable to access 'https://github.com/x/y.git/': Operation timed out after 30000 milliseconds"), "无法连接仓库服务器，请检查网络或地址"},
		{"非公开 HTTPS", errors.New("Git source requires public HTTPS without credentials"), "Git 地址必须是指向公网服务器的 HTTPS 或 SSH 地址"},
		{"路径前缀无效", errors.New("invalid path prefix"), "路径前缀无效，请使用仓库内的相对目录"},
		{"其他错误", errors.New("boom"), "同步失败，请稍后重试"},
	}
	for _, tc := range cases {
		got := syncFailureMessage(tc.err)
		if got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
		if strings.Contains(got, "git ") || strings.Contains(got, cacheRoot) || strings.Contains(got, "fatal:") {
			t.Fatalf("%s: 用户文案泄露了原始错误: %q", tc.name, got)
		}
	}
}

func TestSyncClaimStoresFriendlyErrorWithoutGitDetails(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	cacheRoot := filepath.Join(t.TempDir(), "knowledge-cache")
	control := &syncFakeControl{finishOK: true}
	w := NewSyncWorker(&Engine{newID: func() string { return "probe" }}, control, &syncFakeContent{}, cacheRoot, "worker")
	w.syncGit = func(context.Context, string, string, string, map[string]string) (string, string, []gitsync.FileInfo, []string, []string, error) {
		return "", "", nil, nil, nil, errors.New("clone 失败: git clone --quiet --branch main -- https://github.com/x/missing.git " + cacheRoot + "/src: fatal: could not read Username for 'https://github.com': terminal prompts disabled secret-canary-token")
	}
	src := domain.KnowledgeSource{ID: "src", Kind: "git", GitURL: "https://github.com/example/missing.git", Branch: "main"}
	if err := w.syncClaim(context.Background(), src, "owner"); err != nil {
		t.Fatal(err)
	}
	if control.finishedSummary != "仓库鉴权失败，请检查来源凭据及仓库读取权限" {
		t.Fatalf("来源上应存用户能懂的中文错误: %q", control.finishedSummary)
	}
	if strings.Contains(output.String(), "secret-canary-token") {
		t.Fatal("服务日志泄露凭据")
	}
	for _, leaked := range []string{"git ", cacheRoot, "fatal:", "terminal prompts disabled"} {
		if strings.Contains(control.finishedSummary, leaked) {
			t.Fatalf("用户可见错误泄露了 %q: %q", leaked, control.finishedSummary)
		}
	}
}

func TestImportGitIndexesRootMarkdownForEmptyPrefix(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "guide.md"), []byte("# Guide\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := []gitsync.FileInfo{{Path: "README.md"}, {Path: "docs/guide.md"}}
	for _, tc := range []struct {
		name   string
		prefix string
		want   []string
	}{
		{"空前缀索引根目录与子目录", "", []string{"README.md", "docs/guide.md"}},
		{"斜杠按空前缀处理", "/", []string{"README.md", "docs/guide.md"}},
		{"限定前缀只索引子目录", "docs/", []string{"docs/guide.md"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			control := &syncFakeControl{finishOK: true}
			content := &syncFakeContent{}
			w := NewSyncWorker(&Engine{newID: func() string { return "probe" }}, control, content, t.TempDir(), "worker")
			w.syncGit = func(context.Context, string, string, string, map[string]string) (string, string, []gitsync.FileInfo, []string, []string, error) {
				return dir, "abcdef", files, nil, nil, nil
			}
			src := domain.KnowledgeSource{ID: "src", Kind: "git", GitURL: "https://github.com/example/repo.git", Branch: "main", PathPrefix: tc.prefix, Status: "pending"}
			_, count, err := w.importGit(context.Background(), src, "owner")
			if err != nil {
				t.Fatal(err)
			}
			if count != len(tc.want) {
				t.Fatalf("count = %d want %d: %+v", count, len(tc.want), content.docs)
			}
			for i, doc := range content.docs {
				if doc.Path != tc.want[i] {
					t.Fatalf("docs = %+v want %v", content.docs, tc.want)
				}
			}
			if !control.published || control.publishCallbacks != 1 {
				t.Fatalf("同步结果应发布一次: %+v", control)
			}
		})
	}
}

type credentialSyncControl struct {
	*repo.Store
	sourceID string
}

func (c credentialSyncControl) KnowledgeSources(string) []domain.KnowledgeSource {
	if source, ok := c.KnowledgeSource(c.sourceID); ok {
		return []domain.KnowledgeSource{source}
	}
	return nil
}

func TestCredentialReplacementDuringSyncKeepsNewSourceReady(t *testing.T) {
	for _, replacement := range []string{"replace", "register"} {
		t.Run(replacement, func(t *testing.T) {
			dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
			if dsn == "" {
				t.Skip("MySQL DSN not set")
			}
			t.Setenv("AGENTFLOW_SESSION_SECRET", "credential-replacement-test-key")
			t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			store, err := repo.Open(dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			service := knowledge.NewService(store, nil, nil)
			base, err := service.EnsureHostedBase("credential-overlap")
			if err != nil {
				t.Fatal(err)
			}
			src, err := service.RegisterGitSource(knowledge.GitSourceInput{ProjectID: "credential-overlap", BaseID: base.ID, GitURL: "https://example.com/private.git", Credential: &knowledge.GitCredential{Kind: "https", Username: "deploy", Token: "old-grant"}})
			if err != nil {
				t.Fatal(err)
			}
			defer store.DeleteKnowledgeSource(src.ID)
			defer store.DeleteKnowledgeSourceContent(src.ID)
			remote := t.TempDir()
			run := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = remote
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("fixture Git: %s %v", out, err)
				}
			}
			run("init", "-b", "main")
			run("config", "user.email", "test@example.com")
			run("config", "user.name", "Test")
			if err := os.WriteFile(filepath.Join(remote, "README.md"), []byte("# Private source\n"), 0600); err != nil {
				t.Fatal(err)
			}
			run("add", ".")
			run("commit", "-m", "initial")
			cache := t.TempDir()
			e := NewEngine(store, store, NewMySQLKeywordIndex(store))
			control := credentialSyncControl{Store: store, sourceID: src.ID}
			oldWorker := NewSyncWorker(e, control, store, cache, "old")
			newWorker := NewSyncWorker(e, control, store, cache, "new")
			entered, release := make(chan struct{}), make(chan struct{})
			var attempts atomic.Int32
			transport := func(ctx context.Context, dir, url, branch string, old map[string]string) (string, string, []gitsync.FileInfo, []string, []string, error) {
				result, head, files, changed, removed, err := gitsync.Sync(ctx, dir, remote, branch, old)
				if err == nil && attempts.Add(1) == 1 {
					// Hold a real Git index lock while the old attempt is paused.
					lock := filepath.Join(dir, ".git", "index.lock")
					if err := os.WriteFile(lock, nil, 0600); err != nil {
						return "", "", nil, nil, nil, err
					}
					close(entered)
					<-release
					os.Remove(lock)
				}
				return result, head, files, changed, removed, err
			}
			oldWorker.syncGit = transport
			newWorker.syncGit = transport
			finished := make(chan error, 1)
			oldDone := make(chan struct{})
			go func() { defer close(oldDone); finished <- oldWorker.SyncOnce(context.Background()) }()
			var releaseOnce sync.Once
			defer func() { releaseOnce.Do(func() { close(release) }); <-oldDone }()
			select {
			case <-entered:
			case err := <-finished:
				t.Fatalf("old sync failed to start: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("old sync did not start")
			}
			grant := knowledge.GitCredential{Kind: "https", Username: "deploy", Token: "replacement-grant"}
			if replacement == "register" {
				_, err = service.RegisterGitSource(knowledge.GitSourceInput{ProjectID: "credential-overlap", BaseID: base.ID, GitURL: src.GitURL, Credential: &grant})
			} else {
				_, err = service.SetSourceCredential("credential-overlap", src.ID, grant)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := newWorker.SyncOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			current, _ := store.KnowledgeSource(src.ID)
			if current.Status != "ready" || current.DocCount != 1 {
				t.Fatalf("replacement must sync while old Git operation exits: status=%s error=%s", current.Status, current.LastError)
			}
			releaseOnce.Do(func() { close(release) })
			<-finished
			current, _ = store.KnowledgeSource(src.ID)
			if current.Status != "ready" || len(store.KnowledgeDocuments(src.ID)) != 1 {
				t.Fatal("stale sync changed the replacement result")
			}
		})
	}
}

// A writeback merge only adds one document. Incremental sync must leave the
// untouched documents (and their updated_at) in place instead of rewriting the
// whole source snapshot.
func TestIncrementalSyncWritesOnlyChangedDocuments(t *testing.T) {
	ctx := context.Background()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	remote := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = remote
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git: %s %v", out, e)
		}
	}
	git("init", "-b", "main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	os.Mkdir(filepath.Join(remote, "docs"), 0755)
	os.WriteFile(filepath.Join(remote, "docs", "a.md"), []byte("# A\n\n甲文档。\n"), 0644)
	os.WriteFile(filepath.Join(remote, "docs", "b.md"), []byte("# B\n\n乙文档。\n"), 0644)
	git("add", ".")
	git("commit", "-m", "initial")
	id := "sync-incremental"
	if err := store.PutKnowledgeSource(domain.KnowledgeSource{ID: id, Kind: "git", GitURL: "https://example.com/public.git", Branch: "main", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	w := NewSyncWorker(&Engine{newID: func() string { return "probe" }}, &syncFakeControl{finishOK: true}, store, t.TempDir(), "worker")
	w.syncGit = func(ctx context.Context, dir, _ string, branch string, old map[string]string) (string, string, []gitsync.FileInfo, []string, []string, error) {
		return gitsync.Sync(ctx, dir, remote, branch, old)
	}
	if _, _, err := w.importGit(ctx, domain.KnowledgeSource{ID: id, Kind: "git", GitURL: "https://example.com/public.git", Branch: "main"}, "owner"); err != nil {
		t.Fatal(err)
	}
	first := store.KnowledgeDocuments(id)
	if len(first) != 2 {
		t.Fatalf("first sync docs = %d", len(first))
	}
	before := map[string]time.Time{}
	for _, d := range first {
		before[d.Path] = d.UpdatedAt
	}
	// The merged writeback adds docs/c.md to the source branch.
	os.WriteFile(filepath.Join(remote, "docs", "c.md"), []byte("# C\n\n写回的新文档。\n"), 0644)
	git("add", ".")
	git("commit", "-m", "add merged knowledge")
	if _, _, err := w.importGit(ctx, domain.KnowledgeSource{ID: id, Kind: "git", GitURL: "https://example.com/public.git", Branch: "main"}, "owner"); err != nil {
		t.Fatal(err)
	}
	after := store.KnowledgeDocuments(id)
	if len(after) != 3 {
		t.Fatalf("incremental docs = %d", len(after))
	}
	for _, d := range after {
		if d.Path == "docs/c.md" {
			if !strings.Contains(d.Body, "写回的新文档") {
				t.Fatalf("new document body = %q", d.Body)
			}
			continue
		}
		if !d.UpdatedAt.Equal(before[d.Path]) {
			t.Fatalf("unchanged %s was rewritten: %v -> %v", d.Path, before[d.Path], d.UpdatedAt)
		}
	}
	// Deleting a document removes exactly that path.
	git("rm", "docs/b.md")
	git("commit", "-m", "delete b")
	if _, _, err := w.importGit(ctx, domain.KnowledgeSource{ID: id, Kind: "git", GitURL: "https://example.com/public.git", Branch: "main"}, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.KnowledgeDocument(id, "docs/b.md"); ok {
		t.Fatal("deleted document survived incremental sync")
	}
	if docs := store.KnowledgeDocuments(id); len(docs) != 2 {
		t.Fatalf("after delete docs = %d", len(docs))
	}
}
