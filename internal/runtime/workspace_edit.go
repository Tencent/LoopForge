package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func WorkspaceContentSHA(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// Edits use a content hash; a new file requires the explicit "missing" version. Reject every symlink
// component and use os.Root so replacement cannot escape the workspace.
func WriteWorkspaceFile(root, path, content, expected string) (string, error) {
	if len(content) > 1<<20 || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return "", fmt.Errorf("只支持 1 MB 以内的文本文件")
	}
	rel, err := resolveWorkspaceEditPath(root, path, expected)
	if err != nil {
		return "", err
	}
	base, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer base.Close()
	info, err := base.Stat(rel)
	if os.IsNotExist(err) && expected == "missing" {
		if err := createWorkspaceFile(base, rel, content); err != nil {
			return "", err
		}
		return WorkspaceContentSHA(content), nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return "", fmt.Errorf("不是可编辑文本文件")
	}
	previous, err := base.ReadFile(rel)
	if err != nil {
		return "", err
	}
	if expected == "" || WorkspaceContentSHA(string(previous)) != expected {
		return "", fmt.Errorf("文件内容已变化，请重新读取后再保存")
	}
	if !utf8.Valid(previous) || strings.ContainsRune(string(previous), 0) {
		return "", fmt.Errorf("二进制文件不可编辑")
	}
	f, err := os.CreateTemp(filepath.Join(root, filepath.Dir(rel)), ".agentflow-edit-*")
	if err != nil {
		return "", err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err = f.Chmod(info.Mode().Perm()); err == nil {
		_, err = f.WriteString(content)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	tempRel, err := filepath.Rel(root, temp)
	if err != nil {
		return "", err
	}
	if err = base.Rename(tempRel, rel); err != nil {
		return "", err
	}
	return WorkspaceContentSHA(content), nil
}

// resolveWorkspaceEditPath cleans path into a workspace-relative path and rejects
// absolute or escaping paths, hidden components, and any symlink component. With
// expected == "missing" only the final component may be absent.
func resolveWorkspaceEditPath(root, path, expected string) (string, error) {
	rel := filepath.Clean(filepath.FromSlash(path))
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid workspace path")
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.HasPrefix(part, ".") {
			return "", fmt.Errorf("隐藏文件不可编辑")
		}
	}
	for current := rel; current != "."; current = filepath.Dir(current) {
		info, err := os.Lstat(filepath.Join(root, current))
		if os.IsNotExist(err) && current == rel && expected == "missing" {
			continue
		}
		if os.IsNotExist(err) && current == rel {
			return "", fmt.Errorf("文件 %s 不存在：新建文件时 expectedSha256 传 \"missing\"", filepath.ToSlash(rel))
		}
		if os.IsNotExist(err) {
			return "", fmt.Errorf("目录 %s 不存在：先在终端里 mkdir -p 创建目录，再新建文件", filepath.ToSlash(current))
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("符号链接不可编辑")
		}
	}
	return rel, nil
}

// createWorkspaceFile writes content to a new file that must not exist yet,
// removing the partial file when the write fails.
func createWorkspaceFile(base *os.Root, rel, content string) error {
	f, err := base.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(content)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		base.Remove(rel)
		return err
	}
	return nil
}
