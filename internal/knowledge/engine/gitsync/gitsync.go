// Package gitsync 维护来源仓的本地同步缓存：克隆、拉取、列举 Markdown，
// 把相对路径限制在工作副本内再读文件，以及为审核写回创建分支、提交并推送。
// 缓存目录由调用方指定，不进数据库；不读取历史 commit。
package gitsync

import (
	"agentflow/internal/knowledge/gitremote"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const maxFileBytes = 8 << 20 // 8 MiB

var (
	ErrInvalidPath  = errors.New("invalid path")
	ErrFileNotFound = errors.New("file not found")
	ErrNotDeployed  = errors.New("source not deployed")
	ErrFileTooLarge = errors.New("file too large")
)

// FileInfo 是工作副本里一篇 Markdown 的相对路径、指纹和行数。
type FileInfo struct {
	Path   string
	SHA256 string
	Lines  int
}

const (
	// StagePulling 是克隆或 fetch。StageComparing 是拉取完成后的文件比对。
	StagePulling   = "pulling"
	StageComparing = "comparing"
)

type progressKey struct{}

// WithProgress 让同步在拉取和比对两个阶段回调。索引阶段由调用方在落库前自己报。
func WithProgress(ctx context.Context, fn func(stage string)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, progressKey{}, fn)
}

func notify(ctx context.Context, stage string) {
	fn, _ := ctx.Value(progressKey{}).(func(string))
	if fn != nil {
		fn(stage)
	}
}

// Sync 把 url 的 branch 克隆或更新到 cacheDir。
// oldMap 是 path→sha256 的既有指纹；返回值 changed/removed 在无增量时也是空切片。
// 工作区有本地改动时中止，不覆盖缓存。
func Sync(ctx context.Context, cacheDir, url, branch string, oldMap map[string]string) (dir, head string, files []FileInfo, changed, removed []string, err error) {
	url = strings.TrimSpace(url)
	branch = strings.TrimSpace(branch)
	if strings.TrimSpace(cacheDir) == "" || !validRemote(url) || !validBranch(branch) {
		return "", "", nil, nil, nil, fmt.Errorf("%w: 非法远程地址或分支", ErrInvalidPath)
	}

	if err = privateCacheDir(cacheDir); err != nil {
		return "", "", nil, nil, nil, err
	}

	notify(ctx, StagePulling)
	gitDir := filepath.Join(cacheDir, ".git")
	_, statErr := os.Stat(gitDir)
	switch {
	case statErr == nil:
		if _, err = git(ctx, cacheDir, "remote", "set-url", "origin", url); err != nil {
			return "", "", nil, nil, nil, err
		}
		if _, err = git(ctx, cacheDir, "fetch", "--quiet", "origin", branch); err != nil {
			return "", "", nil, nil, nil, fmt.Errorf("fetch 失败: %s", err)
		}
		status, statusErr := git(ctx, cacheDir, "status", "--porcelain")
		if statusErr != nil {
			return "", "", nil, nil, nil, fmt.Errorf("无法检查工作区: %s", statusErr)
		}
		if status != "" {
			return cacheDir, "", nil, nil, nil, fmt.Errorf("镜像工作区存在本地改动，已中止同步")
		}
		if _, err = git(ctx, cacheDir, "checkout", "--quiet", "origin/"+branch); err != nil {
			return "", "", nil, nil, nil, fmt.Errorf("checkout 失败: %s", err)
		}
	case os.IsNotExist(statErr):
		if err = os.MkdirAll(filepath.Dir(cacheDir), 0o700); err != nil {
			return "", "", nil, nil, nil, err
		}
		if _, err = git(ctx, filepath.Dir(cacheDir), "clone", "--quiet", "--branch", branch, "--", url, cacheDir); err != nil {
			return "", "", nil, nil, nil, fmt.Errorf("clone 失败: %s", err)
		}
	default:
		return "", "", nil, nil, nil, fmt.Errorf("%w: 工作副本不可用", ErrNotDeployed)
	}

	head, err = git(ctx, cacheDir, "rev-parse", "HEAD")
	if err != nil || head == "" {
		return cacheDir, "", nil, nil, nil, fmt.Errorf("无法解析 HEAD: %v", err)
	}
	notify(ctx, StageComparing)
	files, err = CollectMarkdown(cacheDir)
	if err != nil {
		return cacheDir, head, nil, nil, nil, err
	}
	changed, removed = []string{}, []string{}
	for _, f := range files {
		if old, ok := oldMap[f.Path]; !ok || old != f.SHA256 {
			changed = append(changed, f.Path)
		}
	}
	newSet := map[string]bool{}
	for _, f := range files {
		newSet[f.Path] = true
	}
	for p := range oldMap {
		if !newSet[p] {
			removed = append(removed, p)
		}
	}
	sort.Strings(changed)
	sort.Strings(removed)
	return cacheDir, head, files, changed, removed, nil
}

// CollectMarkdown 枚举 root 下的 Markdown，跳过 .git 和以下划线开头的目录。
// 符号链接若解析到 root 之外，返回 ErrInvalidPath，不把外部内容算进清单。
func CollectMarkdown(root string) ([]FileInfo, error) {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("%w: 工作副本不可用", ErrNotDeployed)
	}
	var out []FileInfo
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (d.Name() == ".git" || strings.HasPrefix(d.Name(), "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		fullReal, err := filepath.EvalSymlinks(path)
		if err != nil || !pathInside(rootReal, fullReal) {
			return fmt.Errorf("%w: 路径越出工作副本: %s", ErrInvalidPath, rel)
		}
		info, statErr := os.Stat(fullReal)
		if statErr != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: 不是普通文件: %s", ErrInvalidPath, rel)
		}
		b, err := readLimited(fullReal)
		if err != nil {
			if errors.Is(err, ErrFileTooLarge) {
				return err
			}
			return fmt.Errorf("%w: %s", ErrInvalidPath, rel)
		}
		sum := sha256.Sum256(b)
		out = append(out, FileInfo{
			Path:   rel,
			SHA256: hex.EncodeToString(sum[:]),
			Lines:  strings.Count(string(b), "\n") + 1,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// ReadRepoFile 读取工作副本内的 Markdown。路径穿越、非 Markdown 和越界符号链接都拒绝。
func ReadRepoFile(repoDir, rel string) ([]byte, error) {
	full, err := resolveInside(repoDir, rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(full)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: 不是普通文件: %s", ErrInvalidPath, rel)
	}
	b, err := readLimited(full)
	if err != nil {
		if errors.Is(err, ErrFileTooLarge) {
			return nil, err
		}
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrFileNotFound, rel)
		}
		return nil, fmt.Errorf("%w: %s", ErrInvalidPath, rel)
	}
	return b, nil
}

func resolveInside(root, rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, "../") || filepath.IsAbs(clean) || strings.ContainsRune(clean, 0) {
		return "", fmt.Errorf("%w: 非法路径: %s", ErrInvalidPath, rel)
	}
	if !strings.EqualFold(filepath.Ext(clean), ".md") {
		return "", fmt.Errorf("%w: 只允许 Markdown: %s", ErrInvalidPath, rel)
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("%w: 工作副本不可用", ErrNotDeployed)
	}
	fullReal, err := filepath.EvalSymlinks(filepath.Join(rootReal, clean))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %s", ErrFileNotFound, rel)
		}
		return "", fmt.Errorf("%w: 非法路径: %s", ErrInvalidPath, rel)
	}
	if !pathInside(rootReal, fullReal) {
		return "", fmt.Errorf("%w: 路径越出工作副本: %s", ErrInvalidPath, rel)
	}
	return fullReal, nil
}

func pathInside(rootReal, fullReal string) bool {
	if fullReal == rootReal {
		return true
	}
	prefix := rootReal
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(fullReal, prefix)
}

func readLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > maxFileBytes {
		return nil, fmt.Errorf("%w: 文件超出 %d 字节读取预算", ErrFileTooLarge, maxFileBytes)
	}
	return buf, nil
}

func gitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	full := make([]string, 0, len(args)+2)
	full = append(full, "-c", "credential.helper=", "-c", "core.hooksPath=/dev/null", "-c", "http.extraHeader=", "-c", "http.cookieFile=", "-c", "http.saveCookies=false")
	if options, ok := ctx.Value(publicGitOptions{}).([]string); ok {
		for _, option := range options {
			full = append(full, "-c", option)
		}
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if _, ok := ctx.Value(publicGitOptions{}).([]string); ok {
		cmd.Env = protectedGitEnv()
	}
	if env, ok := ctx.Value(authEnvironment{}).([]string); ok {
		cmd.Env = append(cmd.Env, env...)
	}
	return cmd
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := gitCommand(ctx, dir, args...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", safeGitError(string(b))
	}
	return strings.TrimSpace(string(b)), nil
}

func validRemote(raw string) bool {
	if raw == "" || strings.HasPrefix(raw, "-") || strings.ContainsAny(raw, "\r\n\x00") {
		return false
	}
	if strings.HasPrefix(raw, "ssh://") || strings.Contains(raw, "@") && !strings.Contains(raw, "://") {
		_, err := gitremote.Parse(raw)
		return err == nil
	}
	switch {
	case strings.HasPrefix(raw, "https://"):
		u, err := url.Parse(raw)
		return err == nil && u.User == nil && u.Host != ""
	case strings.HasPrefix(raw, "file://"):
		u, err := url.Parse(raw)
		if err != nil || u.User != nil {
			return false
		}
		return u.Host == "" || u.Host == "localhost"
	default:
		// 绝对路径只用于本机仓库，测试据此避免访问网络。
		return filepath.IsAbs(raw)
	}
}

func validBranch(branch string) bool {
	if branch == "" || branch[0] == '-' || branch[0] == '.' || branch[0] == '/' {
		return false
	}
	if strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") || strings.HasSuffix(branch, ".lock") {
		return false
	}
	if strings.Contains(branch, "..") || strings.Contains(branch, "//") || strings.Contains(branch, "@{") {
		return false
	}
	for _, r := range branch {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-' || r == '/':
		default:
			return false
		}
	}
	return true
}

func protectedGitEnv() []string {
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "TMPDIR", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

// A private source directory protects the clone, including .git and plaintext
// files, while leaving pre-existing ancestor directories under caller control.
func privateCacheDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return ErrInvalidPath
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return os.Chmod(dir, 0700)
}
