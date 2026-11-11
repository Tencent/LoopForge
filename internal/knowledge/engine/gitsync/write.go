package gitsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNoWriteChanges means the branch was created from the source branch but
// the reviewed content does not differ from it. Callers treat it as a normal
// "nothing to write" outcome, not as a transport failure.
var ErrNoWriteChanges = errors.New("提交内容与来源分支一致，无需写回")

const defaultWriteAuthorName = "LoopForge Knowledge"
const defaultWriteAuthorEmail = "knowledge@loopforge.local"

// WriteOptions describes one reviewed document commit.
type WriteOptions struct {
	Branch      string
	Path        string
	Content     string
	Message     string
	AuthorName  string
	AuthorEmail string
	// ExpectedRemote is the commit this attempt believes the branch points at.
	// A differing remote head means a peer changed the branch; refuse to
	// overwrite it instead of force-pushing.
	ExpectedRemote string
}

// WriteResult is the branch and commit created by CommitAndPush.
type WriteResult struct {
	Branch string
	Commit string
	Path   string
}

// CommitAndPush refreshes the cached clone, creates or updates the writeback
// branch from baseBranch, writes exactly one Markdown file, commits only that
// path and pushes the branch. It never pushes to baseBranch.
func CommitAndPush(ctx context.Context, dir, remote, baseBranch string, opts WriteOptions) (WriteResult, error) {
	dir = strings.TrimSpace(dir)
	baseBranch = strings.TrimSpace(baseBranch)
	opts.Branch = strings.TrimSpace(opts.Branch)
	opts.Path = strings.TrimSpace(opts.Path)
	opts.Message = strings.TrimSpace(opts.Message)
	opts.AuthorName = strings.TrimSpace(opts.AuthorName)
	opts.AuthorEmail = strings.TrimSpace(opts.AuthorEmail)
	if dir == "" {
		return WriteResult{}, errors.New("同步缓存目录不能为空")
	}
	if err := validateWriteOptions(baseBranch, opts); err != nil {
		return WriteResult{}, err
	}
	if opts.Message == "" {
		opts.Message = "docs: add reviewed knowledge"
	}
	if opts.AuthorName == "" {
		opts.AuthorName = defaultWriteAuthorName
	}
	if opts.AuthorEmail == "" {
		opts.AuthorEmail = defaultWriteAuthorEmail
	}
	if err := cleanCacheWorktree(ctx, dir); err != nil {
		return WriteResult{}, fmt.Errorf("同步缓存异常：%w", err)
	}
	// Sync switches the cache to the source branch and proves it is clean.
	if _, _, _, _, _, err := Sync(ctx, dir, remote, baseBranch, nil); err != nil {
		return WriteResult{}, err
	}
	existed, err := remoteBranchExists(ctx, dir, opts.Branch)
	if err != nil {
		return WriteResult{}, fmt.Errorf("检查写回分支失败：%w", err)
	}
	remoteHead := ""
	if existed {
		if _, err := git(ctx, dir, "fetch", "--quiet", "origin", opts.Branch); err != nil {
			return WriteResult{}, fmt.Errorf("读取写回分支失败：%w", err)
		}
		head, err := git(ctx, dir, "rev-parse", "origin/"+opts.Branch)
		if err != nil || head == "" {
			return WriteResult{}, errors.New("读取写回分支失败")
		}
		remoteHead = head
		expected := strings.TrimSpace(opts.ExpectedRemote)
		if expected != "" && expected != remoteHead {
			return WriteResult{}, errors.New("写回分支已被其他写入更新，请重新发布")
		}
	}
	// A writeback branch belongs to exactly one submission. Rebuild it from the
	// current source branch so a retry replaces the earlier attempt instead of
	// stacking commits. The lease excludes concurrent writers; force-with-lease
	// additionally refuses to move a branch a peer changed unexpectedly.
	if _, err := git(ctx, dir, "checkout", "--quiet", "-B", opts.Branch, "origin/"+baseBranch); err != nil {
		return WriteResult{}, fmt.Errorf("创建写回分支失败：%w", err)
	}
	rel, err := writeRepoFile(dir, opts.Path, opts.Content)
	if err != nil {
		return WriteResult{}, err
	}
	if _, err := git(ctx, dir, "add", "--", rel); err != nil {
		return WriteResult{}, fmt.Errorf("暂存写回文档失败：%w", err)
	}
	status, err := git(ctx, dir, "status", "--porcelain", "--", rel)
	if err != nil {
		return WriteResult{}, fmt.Errorf("检查写回文档失败：%w", err)
	}
	if strings.TrimSpace(status) == "" {
		// The reviewed content already matches the source branch. Do not create
		// an empty branch or MR; the caller reports that there is nothing to write.
		return WriteResult{}, ErrNoWriteChanges
	}
	if _, err := git(ctx, dir,
		"-c", "user.name="+opts.AuthorName,
		"-c", "user.email="+opts.AuthorEmail,
		"commit", "--quiet", "--only", "-m", opts.Message, "--", rel,
	); err != nil {
		return WriteResult{}, fmt.Errorf("创建写回提交失败：%w", err)
	}
	pushArgs := []string{"push", "--quiet"}
	if existed && remoteHead != "" {
		pushArgs = append(pushArgs, "--force-with-lease=refs/heads/"+opts.Branch+":"+remoteHead)
	}
	pushArgs = append(pushArgs, "origin", "HEAD:refs/heads/"+opts.Branch)
	if _, err := git(ctx, dir, pushArgs...); err != nil {
		return WriteResult{}, fmt.Errorf("推送写回分支失败：%w", err)
	}
	head, err := git(ctx, dir, "rev-parse", "HEAD")
	if err != nil || head == "" {
		return WriteResult{}, errors.New("读取写回提交失败")
	}
	return WriteResult{Branch: opts.Branch, Commit: head, Path: rel}, nil
}

// RemoteContainsCommit reports whether commit is already an ancestor of the
// source branch. It only probes the local cache after a fetch; the credential
// context is prepared by the caller.
func RemoteContainsCommit(ctx context.Context, dir, remote, baseBranch, commit string) (bool, error) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if _, _, _, _, _, err := Sync(ctx, dir, remote, baseBranch, nil); err != nil {
			return false, err
		}
	}
	return remoteContainsCommit(ctx, dir, baseBranch, commit)
}

func remoteContainsCommit(ctx context.Context, dir, baseBranch, commit string) (bool, error) {
	baseBranch = strings.TrimSpace(baseBranch)
	commit = strings.TrimSpace(commit)
	if !validBranch(baseBranch) || !validCommit(commit) {
		return false, errors.New("合并状态参数无效")
	}
	if _, err := git(ctx, dir, "fetch", "--quiet", "origin", baseBranch); err != nil {
		return false, fmt.Errorf("读取来源分支失败：%w", err)
	}
	ref := "origin/" + baseBranch
	if _, err := git(ctx, dir, "rev-parse", "--verify", ref); err != nil {
		return false, fmt.Errorf("来源分支不存在：%w", err)
	}
	cmd := gitCommand(ctx, dir, "merge-base", "--is-ancestor", commit, ref)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	return false, safeGitError(output.String())
}

func validateWriteOptions(baseBranch string, opts WriteOptions) error {
	if !validBranch(baseBranch) {
		return errors.New("来源分支无效")
	}
	if !validBranch(opts.Branch) {
		return errors.New("写回分支无效")
	}
	if strings.HasPrefix(opts.Branch, "refs/") || strings.HasPrefix(opts.Branch, "origin/") {
		return errors.New("写回分支不能使用 refs/ 或 origin/ 前缀")
	}
	if opts.Branch == baseBranch {
		return errors.New("写回分支不能是来源分支")
	}
	if strings.ContainsAny(opts.AuthorName+opts.AuthorEmail, "\r\n\x00") {
		return errors.New("写回作者信息无效")
	}
	if !validWritePath(opts.Path) {
		return errors.New("写回路径必须是仓库内的 Markdown 相对路径")
	}
	if strings.TrimSpace(opts.Content) == "" {
		return errors.New("写回正文不能为空")
	}
	if len(opts.Content) > maxFileBytes {
		return errors.New("写回正文超出大小限制")
	}
	return nil
}

func validWritePath(rel string) bool {
	if rel == "" || strings.ContainsAny(rel, "\\\x00") || strings.HasPrefix(rel, "/") {
		return false
	}
	if len(rel) >= 2 && rel[1] == ':' {
		return false
	}
	if !strings.HasSuffix(strings.ToLower(rel), ".md") {
		return false
	}
	for _, r := range rel {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, segment := range strings.Split(rel, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func validCommit(commit string) bool {
	if len(commit) != 40 && len(commit) != 64 {
		return false
	}
	for _, r := range commit {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			continue
		}
		return false
	}
	return true
}

// cleanCacheWorktree discards a crashed writer's leftovers. The cache is only
// a rebuildable copy, and the caller holds the source write lease, so no other
// process can be using this directory. A stale index.lock is removed first to
// let the reset itself run.
func cleanCacheWorktree(ctx context.Context, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return nil
	}
	_ = os.Remove(filepath.Join(dir, ".git", "index.lock"))
	status, err := git(ctx, dir, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) == "" {
		return nil
	}
	if _, err := git(ctx, dir, "reset", "--hard", "--quiet"); err != nil {
		return err
	}
	_, err = git(ctx, dir, "clean", "-fdq")
	return err
}

func remoteBranchExists(ctx context.Context, dir, branch string) (bool, error) {
	out, err := git(ctx, dir, "ls-remote", "--heads", "origin", "refs/heads/"+branch)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// writeRepoFile writes one Markdown file inside the cached repository without
// following symlinks out of it. Parent directories are created one segment at
// a time and every existing segment is verified to be a real directory.
func writeRepoFile(root, rel, content string) (string, error) {
	if !validWritePath(rel) {
		return "", errors.New("写回路径必须是仓库内的 Markdown 相对路径")
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("%w: 工作副本不可用", ErrNotDeployed)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	current := rootReal
	parent := filepath.Dir(clean)
	if parent != "." {
		for _, segment := range strings.Split(parent, string(filepath.Separator)) {
			next := filepath.Join(current, segment)
			info, statErr := os.Lstat(next)
			switch {
			case os.IsNotExist(statErr):
				if err := os.Mkdir(next, 0o755); err != nil {
					return "", fmt.Errorf("%w: 无法创建目录: %s", ErrInvalidPath, rel)
				}
			case statErr != nil:
				return "", fmt.Errorf("%w: 无法读取目录: %s", ErrInvalidPath, rel)
			case info.Mode()&os.ModeSymlink != 0:
				return "", fmt.Errorf("%w: 路径包含符号链接: %s", ErrInvalidPath, rel)
			case !info.IsDir():
				return "", fmt.Errorf("%w: 路径不是目录: %s", ErrInvalidPath, rel)
			}
			current = next
		}
	}
	full := filepath.Join(current, filepath.Base(clean))
	if info, err := os.Lstat(full); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("%w: 写入目标不是普通文件: %s", ErrInvalidPath, rel)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("%w: 无法读取写入目标: %s", ErrInvalidPath, rel)
	}
	temp, err := os.CreateTemp(current, ".kb-write-*")
	if err != nil {
		return "", fmt.Errorf("%w: 无法写入文档: %s", ErrInvalidPath, rel)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.WriteString(content); err != nil {
		temp.Close()
		return "", fmt.Errorf("%w: 无法写入文档: %s", ErrInvalidPath, rel)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return "", fmt.Errorf("%w: 无法写入文档: %s", ErrInvalidPath, rel)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("%w: 无法写入文档: %s", ErrInvalidPath, rel)
	}
	if err := os.Chmod(tempName, 0o644); err != nil {
		return "", fmt.Errorf("%w: 无法写入文档: %s", ErrInvalidPath, rel)
	}
	if err := os.Rename(tempName, full); err != nil {
		return "", fmt.Errorf("%w: 无法写入文档: %s", ErrInvalidPath, rel)
	}
	return filepath.ToSlash(clean), nil
}
