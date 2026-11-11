package gitsync

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Setenv("GIT_TERMINAL_PROMPT", "0")
	// 同步测试只使用本地仓库，拒绝 file 以外的 Git 协议。
	os.Setenv("GIT_ALLOW_PROTOCOL", "file")
	os.Exit(m.Run())
}

func fixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	if err := os.MkdirAll(filepath.Join(dir, "guides"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# 样例\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "guides", "a.md"), []byte("# A 文档\n售罄\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "init")
	return dir
}

func commitAll(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("add", "-A")
	run("commit", "-m", "change")
}

func TestSyncCloneAndIncremental(t *testing.T) {
	ctx := context.Background()
	src := fixtureRepo(t)
	cache := filepath.Join(t.TempDir(), "cache")

	dir, head, files, changed, removed, err := Sync(ctx, cache, src, "main", nil)
	if err != nil {
		t.Fatal(err)
	}
	if dir != cache || head == "" || len(files) != 2 || len(changed) != 2 || len(removed) != 0 {
		t.Fatalf("首次同步: dir=%s head=%s files=%d changed=%d removed=%d", dir, head, len(files), len(changed), len(removed))
	}
	body, err := ReadRepoFile(dir, "guides/a.md")
	if err != nil || string(body) != "# A 文档\n售罄\n" {
		t.Fatalf("工作副本应可读到刚克隆的正文: %q %v", body, err)
	}

	fp := map[string]string{}
	for _, f := range files {
		fp[f.Path] = f.SHA256
	}
	_, head2, _, changed2, removed2, err := Sync(ctx, cache, src, "main", fp)
	if err != nil {
		t.Fatal(err)
	}
	if head2 != head || changed2 == nil || removed2 == nil || len(changed2) != 0 || len(removed2) != 0 {
		t.Fatalf("无变化应为空增量: head %s→%s changed=%v removed=%v", head, head2, changed2, removed2)
	}

	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("# 样例 v2\n补货\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(src, "guides", "a.md")); err != nil {
		t.Fatal(err)
	}
	commitAll(t, src)
	_, head3, _, changed3, removed3, err := Sync(ctx, cache, src, "main", fp)
	if err != nil {
		t.Fatal(err)
	}
	if head3 == head {
		t.Fatal("拉取后 HEAD 应前进")
	}
	if len(changed3) != 1 || changed3[0] != "README.md" {
		t.Fatalf("changed 应只含 README.md: %v", changed3)
	}
	if len(removed3) != 1 || removed3[0] != "guides/a.md" {
		t.Fatalf("removed 应含被删文档: %v", removed3)
	}
	got, err := ReadRepoFile(cache, "README.md")
	if err != nil || string(got) != "# 样例 v2\n补货\n" {
		t.Fatalf("拉取后工作副本应为新正文: %q %v", got, err)
	}
	if _, err := ReadRepoFile(cache, "guides/a.md"); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("已删除文件应报不存在: %v", err)
	}
}

func TestSyncStopsWhenWorktreeIsDirty(t *testing.T) {
	ctx := context.Background()
	src := fixtureRepo(t)
	cache := filepath.Join(t.TempDir(), "cache")
	if _, _, _, _, _, err := Sync(ctx, cache, src, "main", nil); err != nil {
		t.Fatal(err)
	}
	dirty := []byte("# 本地污染\n")
	if err := os.WriteFile(filepath.Join(cache, "README.md"), dirty, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("# 远端新版本\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAll(t, src)
	_, _, _, _, _, err := Sync(ctx, cache, src, "main", nil)
	if err == nil {
		t.Fatal("工作副本有本地改动时必须中止，不能覆盖")
	}
	got, readErr := os.ReadFile(filepath.Join(cache, "README.md"))
	if readErr != nil || string(got) != string(dirty) {
		t.Fatalf("中止后应保留本地改动: %q %v", got, readErr)
	}
}

func TestValidRemoteAllowsHTTPSAndLocalFile(t *testing.T) {
	src := t.TempDir()
	if !validRemote("https://example.com/repo.git") || !validRemote(src) || !validRemote("file://"+src) {
		t.Fatal("公开 https、本地绝对路径和 file URL 应可用")
	}
}

func TestSyncRejectsBadRemoteOrBranch(t *testing.T) {
	ctx := context.Background()
	src := fixtureRepo(t)
	cacheRoot := t.TempDir()
	cases := []struct {
		url, branch string
	}{
		{"", "main"},
		{"-evil", "main"},
		{"ext::id", "main"},
		{"ssh://example.com/a.git", "main"},
		{"git@example.com:a.git", "main"},
		{"http://example.com/a.git", "main"},
		{"https://user:secret@example.com/a.git", "main"},
		{src, ""},
		{src, "--help"},
		{src, "feature/../../x"},
	}
	for _, tc := range cases {
		cache := filepath.Join(cacheRoot, "c")
		_, _, _, _, _, err := Sync(ctx, cache, tc.url, tc.branch, nil)
		if err == nil {
			t.Fatalf("url %q branch %q 应被拒绝", tc.url, tc.branch)
		}
	}
	if _, _, _, _, _, err := Sync(ctx, filepath.Join(cacheRoot, "missing-branch"), src, "no-such-branch", nil); err == nil {
		t.Fatal("不存在的分支应失败")
	}
}

func TestCollectMarkdownSkipsHiddenAndNonMarkdown(t *testing.T) {
	root := filepath.Join(t.TempDir(), "_cache")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# 标题\n")
	write("guides/a.md", "正文\n")
	write("notes.txt", "忽略\n")
	write("_draft/hidden.md", "草稿\n")
	write(".git/objects/hidden.md", "对象\n")

	files, err := CollectMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(files))
	for _, f := range files {
		got = append(got, f.Path)
		if f.SHA256 == "" || f.Lines < 1 {
			t.Fatalf("指纹或行数缺失: %+v", f)
		}
	}
	want := []string{"README.md", "guides/a.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Markdown 清单 = %v, want %v", got, want)
	}
}

func TestReadRepoFileRejectsEscape(t *testing.T) {
	dir := fixtureRepo(t)
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "secret.md")
	if err := os.WriteFile(outside, []byte("机密"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "guides", "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "README.md"), filepath.Join(dir, "guides", "alias.md")); err != nil {
		t.Fatal(err)
	}

	body, err := ReadRepoFile(dir, "guides/alias.md")
	if err != nil || string(body) != "# 样例\n" {
		t.Fatalf("副本内部的符号链接应可读: %q %v", body, err)
	}
	for _, bad := range []string{"../outside.md", "/etc/passwd", "guides/../../etc/passwd", "guides/a.txt", "a\x00.md"} {
		_, err := ReadRepoFile(dir, bad)
		if !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("越权路径 %q 应拒绝: %v", bad, err)
		}
		if strings.Contains(err.Error(), dir) || strings.Contains(err.Error(), outsideDir) {
			t.Fatalf("路径错误不应带出本机目录: %v", err)
		}
	}
	_, err = ReadRepoFile(dir, "guides/link.md")
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("符号链接逃逸应拒绝: %v", err)
	}
	if strings.Contains(err.Error(), outsideDir) {
		t.Fatalf("逃逸错误不应带出目标目录: %v", err)
	}
}

func TestCollectMarkdownRejectsSymlinkEscape(t *testing.T) {
	ctx := context.Background()
	src := fixtureRepo(t)
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("机密\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "leak.md")); err != nil {
		t.Fatal(err)
	}
	commitAll(t, src)
	cache := filepath.Join(t.TempDir(), "cache")
	_, _, files, _, _, err := Sync(ctx, cache, src, "main", nil)
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("克隆结果含越界符号链接时应失败: %v", err)
	}
	for _, f := range files {
		if f.Path == "leak.md" {
			t.Fatal("越界文件不应进入清单")
		}
	}
}

func TestReadRepoFileLimit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.md"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, err := ReadRepoFile(dir, "small.md")
	if err != nil || string(content) != "ok\n" {
		t.Fatalf("small file: %q %v", content, err)
	}

	exact := make([]byte, maxFileBytes)
	for i := range exact {
		exact[i] = 'x'
	}
	if err := os.WriteFile(filepath.Join(dir, "exact.md"), exact, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRepoFile(dir, "exact.md"); err != nil {
		t.Fatalf("刚好达到上限的文件应可读: %v", err)
	}

	over := make([]byte, maxFileBytes+1)
	for i := range over {
		over[i] = 'y'
	}
	if err := os.WriteFile(filepath.Join(dir, "over.md"), over, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ReadRepoFile(dir, "over.md")
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("超限文件应拒绝: %v", err)
	}
}

func TestSyncCacheIsPrivateOnCreateAndReuse(t *testing.T) {
	remote := fixtureRepo(t)
	cache := filepath.Join(t.TempDir(), "cache", "source")
	for i := 0; i < 2; i++ {
		if _, _, _, _, _, err := Sync(context.Background(), cache, remote, "main", nil); err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{cache, filepath.Dir(cache)} {
			info, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm()&0077 != 0 {
				t.Errorf("cache readable by other users: %s %o", dir, info.Mode().Perm())
			}
		}
		if i == 0 {
			os.Chmod(cache, 0755)
		}
	}
}
