package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	"agentflow/internal/knowledge/engine/gitsync"
	"agentflow/internal/knowledge/hosting"
	"agentflow/internal/repo"
)

const canaryToken = "secret-canary-token"

type writebackFixture struct {
	store    *repo.Store
	engine   *Engine
	remote   string
	work     string
	baseID   string
	sourceID string
	server   *httptest.Server
	gitURL   string
}

func newWritebackFixture(t *testing.T, gitURL string, canWrite bool) *writebackFixture {
	t.Helper()
	t.Setenv("AGENTFLOW_SESSION_SECRET", "writeback-test-key")
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	service := knowledge.NewService(store, func() string { return "test" }, nil)
	base, err := service.EnsureHostedBase("project-writeback")
	if err != nil {
		t.Fatal(err)
	}
	src, err := service.RegisterGitSource(knowledge.GitSourceInput{
		ProjectID: "project-writeback", BaseID: base.ID, Name: "示例知识库",
		GitURL: gitURL, Branch: "main", PathPrefix: "docs/",
		Credential: &knowledge.GitCredential{Kind: "https", Username: "deploy", Token: canaryToken, CanWrite: canWrite},
	})
	if err != nil {
		t.Fatal(err)
	}
	remote, work := writebackRepo(t)
	eng := NewEngine(store, store, NewMySQLKeywordIndex(store))
	eng.writeCacheRoot = t.TempDir()
	eng.writeLeaseTTL = 5 * time.Second
	eng.gitWrite = func(ctx context.Context, dir, _ string, base string, opts gitsync.WriteOptions) (gitsync.WriteResult, error) {
		return gitsync.CommitAndPush(ctx, dir, remote, base, opts)
	}
	eng.gitMergeProbe = func(ctx context.Context, dir, _ string, base, commit string) (bool, error) {
		return gitsync.RemoteContainsCommit(ctx, dir, remote, base, commit)
	}
	return &writebackFixture{
		store: store, engine: eng, remote: remote, work: work,
		baseID: base.ID, sourceID: src.ID, gitURL: gitURL,
	}
}

// writebackRepo creates a bare remote with README.md and docs/old.md on main.
func writebackRepo(t *testing.T) (remote, work string) {
	t.Helper()
	root := t.TempDir()
	remote, work = filepath.Join(root, "repo.git"), filepath.Join(root, "work")
	gitRun(t, root, "init", "--bare", "-b", "main", remote)
	gitRun(t, root, "init", "-b", "main", work)
	gitRun(t, work, "config", "user.email", "test@example.com")
	gitRun(t, work, "config", "user.name", "test")
	if err := os.MkdirAll(filepath.Join(work, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "docs", "old.md"), []byte("# 旧文档\n\n原有知识。\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# 示例\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-m", "initial")
	gitRun(t, work, "remote", "add", "origin", remote)
	gitRun(t, work, "push", "origin", "main")
	return remote, work
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func fakePullServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/repos/example/repo/pulls" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]string
		_ = json.Unmarshal(body, &payload)
		if payload["head"] == "" || payload["base"] != "main" {
			t.Fatalf("payload = %+v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":7,"html_url":"https://github.com/example/repo/pull/7","state":"open","merged":false}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestWriteGitDocumentCreatesBranchPushesAndMROpen(t *testing.T) {
	f := newWritebackFixture(t, "https://github.com/example/repo.git", true)
	server := fakePullServer(t, canaryToken)
	f.engine.hostingClientFor = func(repo hosting.Repo, token string) *hosting.Client {
		return &hosting.Client{BaseURL: server.URL, Token: token, HTTP: server.Client()}
	}
	result, err := f.engine.WriteGitDocument(context.Background(), knowledge.WritebackRequest{
		SubmissionID: "kbs-1", ProjectID: "project-writeback", Source: f.sourceID,
		Path: "docs/new.md", Content: "# 新知识\n\n排障结论。\n", Title: "新知识",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Branch == "" || len(result.Commit) != 40 || result.MRURL != "https://github.com/example/repo/pull/7" || result.MRStatus != domain.KnowledgeWritebackMROpen {
		t.Fatalf("result = %+v", result)
	}
	if result.Branch != "kb/writeback-kbs-1" || !strings.Contains(result.Message, "合并请求") {
		t.Fatalf("branch/message = %q / %q", result.Branch, result.Message)
	}
	if got := gitRun(t, f.remote, "show", result.Branch+":docs/new.md"); got != "# 新知识\n\n排障结论。" {
		t.Fatalf("remote content = %q", got)
	}
	record, ok := f.store.KnowledgeWritebackBySubmission("kbs-1")
	if !ok || record.Status != domain.KnowledgeWritebackMROpen || record.MRURL != result.MRURL || record.Branch != result.Branch || record.MRIID != 7 || record.Attempts != 1 {
		t.Fatalf("record = %+v ok=%v", record, ok)
	}
	src, _ := f.store.KnowledgeSource(f.sourceID)
	if src.LeaseOwner != "" || src.LeaseUntil != nil {
		t.Fatalf("lease not released: %+v", src)
	}
	config, err := os.ReadFile(filepath.Join(f.engine.writebackCacheDir(src), ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), canaryToken) || strings.Contains(string(config), "deploy") {
		t.Fatal(".git/config leaked credentials")
	}
}

func TestWriteGitDocumentUnsupportedPlatformPushesBranchOnly(t *testing.T) {
	f := newWritebackFixture(t, "https://gitee.com/example/repo.git", true)
	result, err := f.engine.WriteGitDocument(context.Background(), knowledge.WritebackRequest{
		SubmissionID: "kbs-unsupported", ProjectID: "project-writeback", Source: f.sourceID,
		Path: "docs/new.md", Content: "# 新知识\n", Title: "新知识",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Branch == "" || result.MRURL != "" || result.MRStatus != domain.KnowledgeWritebackBranchPushed {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(result.Message, "暂不支持自动创建合并请求") || !strings.Contains(result.Message, result.Branch) {
		t.Fatalf("message = %q", result.Message)
	}
	if got := gitRun(t, f.remote, "show", result.Branch+":docs/new.md"); got != "# 新知识" {
		t.Fatalf("remote content = %q", got)
	}
	record, _ := f.store.KnowledgeWritebackBySubmission("kbs-unsupported")
	if record.Status != domain.KnowledgeWritebackBranchPushed || record.MRURL != "" {
		t.Fatalf("record = %+v", record)
	}
}

func TestWriteGitDocumentRequiresCanWriteCredential(t *testing.T) {
	f := newWritebackFixture(t, "https://github.com/example/repo.git", false)
	_, err := f.engine.WriteGitDocument(context.Background(), knowledge.WritebackRequest{
		SubmissionID: "kbs-ro", ProjectID: "project-writeback", Source: f.sourceID,
		Path: "docs/new.md", Content: "# 新知识\n",
	})
	if err == nil || !strings.Contains(err.Error(), "写入权限") {
		t.Fatalf("err = %v", err)
	}
	if _, showErr := tryWritebackGit(f.remote, "show", "kb/writeback-kbs-ro:docs/new.md"); showErr == nil {
		t.Fatal("read-only credential created a branch")
	}
	if _, ok := f.store.KnowledgeWritebackBySubmission("kbs-ro"); ok {
		t.Fatal("rejected writeback must not leave a record")
	}
}

func TestWriteGitDocumentRejectsBusySource(t *testing.T) {
	f := newWritebackFixture(t, "https://github.com/example/repo.git", true)
	if ok, err := f.store.ClaimKnowledgeWrite(f.sourceID, "peer", time.Minute); err != nil || !ok {
		t.Fatalf("seed claim = %v %v", ok, err)
	}
	_, err := f.engine.WriteGitDocument(context.Background(), knowledge.WritebackRequest{
		SubmissionID: "kbs-busy", ProjectID: "project-writeback", Source: f.sourceID,
		Path: "docs/new.md", Content: "# 新知识\n",
	})
	if err == nil || !strings.Contains(err.Error(), "正在同步或写回") {
		t.Fatalf("err = %v", err)
	}
	if _, finishErr := f.store.FinishKnowledgeWrite(f.sourceID, "peer"); finishErr != nil {
		t.Fatal(finishErr)
	}
	if _, err := f.engine.WriteGitDocument(context.Background(), knowledge.WritebackRequest{
		SubmissionID: "kbs-busy", ProjectID: "project-writeback", Source: f.sourceID,
		Path: "docs/new.md", Content: "# 新知识\n",
	}); err != nil {
		t.Fatalf("retry after lease release: %v", err)
	}
}

func TestWriteGitDocumentRequiresPathPrefix(t *testing.T) {
	f := newWritebackFixture(t, "https://github.com/example/repo.git", true)
	_, err := f.engine.WriteGitDocument(context.Background(), knowledge.WritebackRequest{
		SubmissionID: "kbs-prefix", ProjectID: "project-writeback", Source: f.sourceID,
		Path: "other/new.md", Content: "# 前缀外\n",
	})
	if err == nil || !strings.Contains(err.Error(), "路径前缀") {
		t.Fatalf("err = %v", err)
	}
}

func TestWriteGitDocumentRetryReusesBranchAndReplacesCommit(t *testing.T) {
	f := newWritebackFixture(t, "https://github.com/example/repo.git", true)
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":9,"html_url":"https://github.com/example/repo/pull/9","state":"open"}`))
	}))
	defer server.Close()
	f.engine.hostingClientFor = func(repo hosting.Repo, token string) *hosting.Client {
		return &hosting.Client{BaseURL: server.URL, Token: token, HTTP: server.Client()}
	}
	first, err := f.engine.WriteGitDocument(context.Background(), knowledge.WritebackRequest{
		SubmissionID: "kbs-retry", ProjectID: "project-writeback", Source: f.sourceID,
		Path: "docs/retry.md", Content: "# 第一版\n", Title: "重试",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.MRStatus != domain.KnowledgeWritebackBranchPushed || !strings.Contains(first.Message, "合并请求创建失败") {
		t.Fatalf("first = %+v", first)
	}
	second, err := f.engine.WriteGitDocument(context.Background(), knowledge.WritebackRequest{
		SubmissionID: "kbs-retry", ProjectID: "project-writeback", Source: f.sourceID,
		Path: "docs/retry.md", Content: "# 第二版\n\n补充。\n", Title: "重试",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.MRStatus != domain.KnowledgeWritebackMROpen || second.Branch != first.Branch || second.Commit == first.Commit {
		t.Fatalf("second = %+v", second)
	}
	if got := gitRun(t, f.remote, "show", second.Branch+":docs/retry.md"); got != "# 第二版\n\n补充。" {
		t.Fatalf("content = %q", got)
	}
	if got := gitRun(t, f.remote, "rev-list", "--count", "main.."+second.Branch); got != "1" {
		t.Fatalf("branch has %s commits over main, want 1", got)
	}
	record, _ := f.store.KnowledgeWritebackBySubmission("kbs-retry")
	if record.Attempts != 2 || record.Status != domain.KnowledgeWritebackMROpen || record.MRURL == "" {
		t.Fatalf("record = %+v", record)
	}
}

func TestWriteGitDocumentFailureIsFriendlyAndLeaksNothing(t *testing.T) {
	f := newWritebackFixture(t, "https://github.com/example/repo.git", true)
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	// A protected branch rejects the push and echoes the token in stderr.
	hook := filepath.Join(f.remote, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'protected branch "+canaryToken+"' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := f.engine.WriteGitDocument(context.Background(), knowledge.WritebackRequest{
		SubmissionID: "kbs-protected", ProjectID: "project-writeback", Source: f.sourceID,
		Path: "docs/protected.md", Content: "# 被拒绝\n",
	})
	if err == nil || !strings.Contains(err.Error(), "推送被仓库拒绝") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), canaryToken) || strings.Contains(err.Error(), "protected branch") {
		t.Fatalf("error leaked remote output: %v", err)
	}
	if strings.Contains(logs.String(), canaryToken) {
		t.Fatal("log leaked the token")
	}
	record, _ := f.store.KnowledgeWritebackBySubmission("kbs-protected")
	if record.Status != domain.KnowledgeWritebackFailed || record.Error == "" {
		t.Fatalf("record = %+v", record)
	}
	src, _ := f.store.KnowledgeSource(f.sourceID)
	if src.LeaseOwner != "" {
		t.Fatal("failed writeback kept the lease")
	}
}

func TestReconcileWritebackAfterMergeTriggersIncrementalIndex(t *testing.T) {
	f := newWritebackFixture(t, "https://github.com/example/repo.git", true)
	merged := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"number":7,"html_url":"https://github.com/example/repo/pull/7","state":"open"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls/7"):
			state := "open"
			if merged {
				state = "closed"
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"number":7,"state":"` + state + `","merged":` + map[bool]string{true: "true", false: "false"}[merged] + `}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	f.engine.hostingClientFor = func(repo hosting.Repo, token string) *hosting.Client {
		return &hosting.Client{BaseURL: server.URL, Token: token, HTTP: server.Client()}
	}
	// Index the initial source first.
	worker := NewSyncWorker(f.engine, &syncFakeControl{finishOK: true}, f.store, t.TempDir(), "test")
	worker.syncGit = func(ctx context.Context, dir, _ string, branch string, old map[string]string) (string, string, []gitsync.FileInfo, []string, []string, error) {
		return gitsync.Sync(ctx, dir, f.remote, branch, old)
	}
	src, _ := f.store.KnowledgeSource(f.sourceID)
	if _, _, err := worker.importGit(context.Background(), src, "owner"); err != nil {
		t.Fatal(err)
	}
	oldDoc, ok := f.store.KnowledgeDocument(f.sourceID, "docs/old.md")
	if !ok {
		t.Fatal("initial sync did not index docs/old.md")
	}
	result, err := f.engine.WriteGitDocument(context.Background(), knowledge.WritebackRequest{
		SubmissionID: "kbs-merge", ProjectID: "project-writeback", Source: f.sourceID,
		Path: "docs/merged.md", Content: "# 合并后的新知识\n\n独特词：麒麟臂。\n", Title: "合并后的新知识",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Merge the writeback branch into main (fast-forward keeps the test small).
	gitRun(t, f.remote, "update-ref", "refs/heads/main", result.Commit)
	merged = true
	reconcileWorker := NewSyncWorker(f.engine, f.store, f.store, t.TempDir(), "test")
	reconcileWorker.reconcileStale = 0
	reconcileWorker.reconcileWritebacks(context.Background())
	record, _ := f.store.KnowledgeWritebackBySubmission("kbs-merge")
	if record.Status != domain.KnowledgeWritebackMerged || record.MRState != "merged" {
		t.Fatalf("record after merge = %+v", record)
	}
	src, _ = f.store.KnowledgeSource(f.sourceID)
	if src.Status != "pending" {
		t.Fatalf("source was not queued for incremental sync: %+v", src)
	}
	// Run the incremental import that the sync loop would perform.
	if _, _, err := worker.importGit(context.Background(), src, "owner"); err != nil {
		t.Fatal(err)
	}
	newDoc, ok := f.store.KnowledgeDocument(f.sourceID, "docs/merged.md")
	if !ok || !strings.Contains(newDoc.Body, "麒麟臂") {
		t.Fatalf("merged document not indexed: %+v ok=%v", newDoc, ok)
	}
	unchanged, _ := f.store.KnowledgeDocument(f.sourceID, "docs/old.md")
	if !unchanged.UpdatedAt.Equal(oldDoc.UpdatedAt) {
		t.Fatalf("unrelated document was rewritten: %v -> %v", oldDoc.UpdatedAt, unchanged.UpdatedAt)
	}
}

func tryWritebackGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestWriteGitDocumentCreatesGitLabMR(t *testing.T) {
	f := newWritebackFixture(t, "https://gitlab.example.com/group/sub/repo.git", true)
	var gotPath, gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotToken = r.URL.EscapedPath(), r.Header.Get("PRIVATE-TOKEN")
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"iid":4,"web_url":"https://gitlab.example.com/group/sub/repo/-/merge_requests/4","state":"opened"}`))
	}))
	defer server.Close()
	f.engine.hostingClientFor = func(repo hosting.Repo, token string) *hosting.Client {
		return &hosting.Client{BaseURL: server.URL + "/api/v4", Token: token, HTTP: server.Client()}
	}
	result, err := f.engine.WriteGitDocument(context.Background(), knowledge.WritebackRequest{
		SubmissionID: "kbs-gitlab", ProjectID: "project-writeback", Source: f.sourceID,
		Path: "docs/gitlab.md", Content: "# GitLab 写回\n", Title: "GitLab 写回",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.MRURL != "https://gitlab.example.com/group/sub/repo/-/merge_requests/4" || result.MRProvider != "gitlab" || result.MRStatus != domain.KnowledgeWritebackMROpen {
		t.Fatalf("result = %+v", result)
	}
	if gotPath != "/api/v4/projects/group%2Fsub%2Frepo/merge_requests" || gotToken != canaryToken {
		t.Fatalf("path=%q token=%q", gotPath, gotToken)
	}
	record, _ := f.store.KnowledgeWritebackBySubmission("kbs-gitlab")
	if record.MRProvider != "gitlab" || record.MRIID != 4 {
		t.Fatalf("record = %+v", record)
	}
}
