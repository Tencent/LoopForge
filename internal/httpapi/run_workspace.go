package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

// runnerWorkspaceRunID names the runner directory holding run's checkout: a
// child SOP on the shared layout works in the checkout of its parent chain.
func runnerWorkspaceRunID(run domain.Run) string {
	if run.ExecutionContext.RunnerWorkspaceLayout == domain.RunnerWorkspaceLayoutShared {
		return engine.WorkspaceRunID(run)
	}
	return run.ID
}

// workspaceOwner resolves the checkout lifecycle separately from the run whose
// history is being viewed. A legacy per-node Runner checkout belongs to its own run.
func (h *runsHandlers) workspaceOwner(run domain.Run) (domain.Run, bool) {
	id := engine.WorkspaceRunID(run)
	if id == run.ID || (runnerWorkspaceRunID(run) == run.ID && h.engine.RunExecutionRunner(run) != "") {
		return run, true
	}
	owner, err := h.repo.RunHeader(id)
	if err != nil || owner.ID == "" || owner.IsPersonalWorkspace() ||
		owner.ExecutionContext.ProjectID != run.ExecutionContext.ProjectID || engine.WorkspaceRunID(owner) != owner.ID {
		return domain.Run{}, false
	}
	return owner, true
}

const (
	workspaceTreeLimit = 50000
	workspaceFileLimit = 1024 * 1024
)

type workspaceChange struct {
	Path       string `json:"path"`
	Repository string `json:"repository,omitempty"`
	Index      string `json:"index"`
	Worktree   string `json:"worktree"`
}

// workspaceRepositoryDetail describes a run repository for the 「改动」view.
// The workspace itself only reports repository names, so the branch pair the
// panel compares against (and links to on 工蜂) comes from the run record.
type workspaceRepositoryDetail struct {
	Name       string `json:"name"`
	CloneURL   string `json:"cloneUrl,omitempty"`
	BaseBranch string `json:"baseBranch,omitempty"`
	WorkBranch string `json:"workBranch,omitempty"`
}

func workspaceRepositoryDetails(run domain.Run) []workspaceRepositoryDetail {
	repositories := run.RepositoryContexts()
	details := make([]workspaceRepositoryDetail, 0, len(repositories))
	for _, rc := range repositories {
		details = append(details, workspaceRepositoryDetail{
			Name:       rc.Name,
			CloneURL:   cloneURLWithoutCredentials(rc.CloneURL),
			BaseBranch: repositoryBaseBranchName(rc),
			WorkBranch: rc.WorkBranch,
		})
	}
	return details
}

// workspaceUnavailablePayload builds the shared 「工作区不可用」 view: the panel
// renders a read-only notice with the reason instead of a change list, so the
// fields stay in one place instead of being copied per call site.
func workspaceUnavailablePayload(run domain.Run, reason string) gin.H {
	return gin.H{
		"available": false, "readOnly": true, "reason": reason,
		"runStatus": run.Status, "repositoryDetails": workspaceRepositoryDetails(run),
	}
}

// cloneURLWithoutCredentials keeps only what the panel needs to build a 工蜂
// link: a remote configured as https://user:token@host/... must not hand the
// token to the browser.
func cloneURLWithoutCredentials(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.User != nil {
		if u.Scheme == "ssh" {
			// ssh://git@host/... names the account, never a secret.
			u.User = url.User(u.User.Username())
		} else {
			u.User = nil
		}
	}
	return u.String()
}

type workspaceEntry struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	Size     int64     `json:"size,omitempty"`
	Modified time.Time `json:"modifiedAt,omitempty"`
}

type workspaceSearchResult struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Preview string `json:"preview"`
}

type workspaceCommit struct {
	ID        string   `json:"id"`
	ParentIDs []string `json:"parentIds"`
	Message   string   `json:"message"`
	Author    string   `json:"author"`
	Timestamp int64    `json:"timestamp"`
}

type workspaceCommitFile struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

const workspaceLogLimit = 80

func (h *runsHandlers) runWorkspace(c *gin.Context) {
	run, ok := h.repo.RunState(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}
	owner, ok := h.workspaceOwner(run)
	if !ok {
		h.respondWorkspaceUnavailable(c, run, owner, http.StatusGone, "共享工作区所属运行已不可用", false)
		return
	}
	runnerID := h.engine.RunExecutionRunner(owner)
	if !owner.WorkspaceCleanedAt.IsZero() {
		if runnerID != "" && c.FullPath() == "/api/runs/:id/workspace" {
			if status, reason := h.probeRunnerWorkspace(c.Request.Context(), owner, runnerID); status != 0 {
				h.respondWorkspaceUnavailable(c, run, owner, status, reason, false)
				return
			}
		}
		h.respondWorkspaceUnavailable(c, run, owner, http.StatusGone, "工作区已回收；有保存的快照时可按需恢复，空闲 1 小时后再次回收", true)
		return
	}
	if runnerID != "" {
		h.proxyRunnerWorkspace(c, run, owner, runnerID)
		return
	}
	// In a child-first SOP, the child may have bound the checkout before the
	// owner has run a step of its own. Use that binding until the owner has one.
	workspace := owner
	if strings.TrimSpace(workspace.WorkspaceRoot) == "" {
		workspace.WorkspaceHost, workspace.WorkspaceRoot = run.WorkspaceHost, run.WorkspaceRoot
	}
	root, reason := h.engine.LocalRunWorkspace(workspace)
	if reason == "" {
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			reason = "工作区尚未创建或已被清理"
		}
	}
	if reason != "" {
		h.respondWorkspaceUnavailable(c, run, owner, http.StatusConflict, reason, true)
		return
	}

	if c.FullPath() != "/api/runs/:id/workspace" {
		h.touchWorkspaceBrowse(c, owner)
	}
	switch c.FullPath() {
	case "/api/runs/:id/workspace":
		changes, warning := collectWorkspaceChanges(c.Request.Context(), root, run)
		payload := gin.H{
			"available": true, "readOnly": !h.workspaceWritable(c, run), "runStatus": run.Status, "pauseRequested": run.PauseRequested,
			"changes": changes, "repositories": collectWorkspaceGitRepositories(root, run), "warning": warning,
			"repositoryDetails": workspaceRepositoryDetails(run),
		}
		h.workspaceLifecycleInfo(c, owner, payload, true)
		c.JSON(http.StatusOK, payload)
	case "/api/runs/:id/workspace/tree":
		entries, truncated, err := readWorkspaceTree(root, c.Query("path"))
		if err != nil {
			respondWorkspaceError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"entries": entries, "truncated": truncated})
	case "/api/runs/:id/workspace/file":
		path := strings.TrimSpace(c.Query("path"))
		if path == "" {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "path is required"})
			return
		}
		if ref := strings.TrimSpace(c.Query("ref")); ref != "" {
			content, size, err := readWorkspaceGitBlob(c.Request.Context(), root, run, path, ref)
			if err != nil {
				respondWorkspaceError(c, err)
				return
			}
			c.JSON(http.StatusOK, gin.H{"path": filepath.ToSlash(path), "content": content, "size": size, "ref": ref})
			return
		}
		content, size, err := readWorkspaceFile(root, path)
		if err != nil {
			respondWorkspaceError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"path": filepath.ToSlash(path), "content": content, "size": size, "sha256": runtime.WorkspaceContentSHA(content)})
	case "/api/runs/:id/workspace/log":
		log, err := collectWorkspaceLog(c.Request.Context(), root, run, c.Query("repository"))
		if err != nil {
			respondWorkspaceError(c, err)
			return
		}
		c.JSON(http.StatusOK, log)
	case "/api/runs/:id/workspace/commit":
		files, err := collectWorkspaceCommitFiles(c.Request.Context(), root, run, c.Query("repository"), c.Query("id"))
		if err != nil {
			respondWorkspaceError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"id": strings.TrimSpace(c.Query("id")), "files": files})
	case "/api/runs/:id/workspace/diff":
		path := strings.TrimSpace(c.Query("path"))
		diff, truncated, err := readWorkspaceDiff(c.Request.Context(), root, run, path)
		if err != nil {
			respondWorkspaceError(c, err)
			return
		}
		if path == "" {
			c.JSON(http.StatusOK, gin.H{"diff": diff, "truncated": truncated})
			return
		}
		original, originalTruncated, err := readWorkspaceBaseFile(c.Request.Context(), root, run, path)
		if err != nil {
			respondWorkspaceError(c, err)
			return
		}
		modified, _, err := readWorkspaceFile(root, path)
		if err != nil {
			respondWorkspaceError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"diff": diff, "original": original, "modified": modified, "truncated": truncated || originalTruncated})
	case "/api/runs/:id/workspace/search":
		query := strings.TrimSpace(c.Query("q"))
		if len([]rune(query)) < 2 {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "q must contain at least 2 characters"})
			return
		}
		results, truncated, err := searchWorkspace(c.Request.Context(), root, query)
		if err != nil {
			respondWorkspaceError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"results": results, "truncated": truncated})
	default:
		c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func (h *runsHandlers) respondWorkspaceUnavailable(c *gin.Context, run, owner domain.Run, status int, reason string, allowRestore bool) {
	if c.FullPath() != "/api/runs/:id/workspace" {
		c.JSON(status, gin.H{"error": reason})
		return
	}
	payload := workspaceUnavailablePayload(run, reason)
	if owner.ID != "" {
		h.workspaceLifecycleInfo(c, owner, payload, allowRestore)
	}
	c.JSON(http.StatusOK, payload)
}

func (h *runsHandlers) requestRunnerWorkspace(ctx context.Context, runnerID string, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, int, string) {
	if _, exists := h.repo.Runner(runnerID); !exists {
		return domain.RunnerWorkspaceResponse{}, http.StatusGone, "执行机器 Daemon 已卸载，工作区已随之删除；重新连接执行机器也无法恢复此工作区"
	}
	if h.runnerControls == nil {
		return domain.RunnerWorkspaceResponse{}, http.StatusServiceUnavailable, "远程工作区通道未配置"
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	response, err := h.runnerControls.requestWorkspace(ctx, runnerID, request)
	if err != nil {
		reason := "远程执行机器当前离线或工作区通道不可用"
		if errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, errRunnerWorkspaceUnavailable) {
			reason = "远程工作区读取超时"
		}
		return response, http.StatusConflict, reason
	}
	return response, 0, ""
}

func runnerWorkspaceErrorStatus(status int) int {
	if status < 400 || status > 599 {
		return http.StatusBadRequest
	}
	return status
}

func (h *runsHandlers) probeRunnerWorkspace(ctx context.Context, owner domain.Run, runnerID string) (int, string) {
	response, status, reason := h.requestRunnerWorkspace(ctx, runnerID, domain.RunnerWorkspaceRequest{
		RunID: owner.ID, RunStatus: owner.Status, WorkspaceLayout: owner.ExecutionContext.RunnerWorkspaceLayout,
		Operation: domain.RunnerWorkspaceOperationInfo,
	})
	if status != 0 {
		return status, reason
	}
	// A missing checkout is expected after reclamation and confirms the Runner
	// can receive a restore request.
	if response.Status == http.StatusNotFound && !owner.WorkspaceCleanedAt.IsZero() {
		return 0, ""
	}
	if response.Error != "" {
		return runnerWorkspaceErrorStatus(response.Status), response.Error
	}
	if len(response.Payload) == 0 {
		return http.StatusBadGateway, "远程执行机器返回了空响应"
	}
	return 0, ""
}

func (h *runsHandlers) proxyRunnerWorkspace(c *gin.Context, run, owner domain.Run, runnerID string) {
	operation := ""
	switch c.FullPath() {
	case "/api/runs/:id/workspace":
		operation = domain.RunnerWorkspaceOperationInfo
	case "/api/runs/:id/workspace/tree":
		operation = domain.RunnerWorkspaceOperationTree
	case "/api/runs/:id/workspace/file":
		operation = domain.RunnerWorkspaceOperationFile
	case "/api/runs/:id/workspace/log":
		operation = domain.RunnerWorkspaceOperationLog
	case "/api/runs/:id/workspace/commit":
		operation = domain.RunnerWorkspaceOperationCommit
	case "/api/runs/:id/workspace/diff":
		operation = domain.RunnerWorkspaceOperationDiff
	case "/api/runs/:id/workspace/search":
		operation = domain.RunnerWorkspaceOperationSearch
	default:
		c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	path := c.Query("path")
	if repo := strings.TrimSpace(c.Query("repository")); repo != "" && (operation == domain.RunnerWorkspaceOperationLog || operation == domain.RunnerWorkspaceOperationCommit) {
		path = repo
	}
	ref := strings.TrimSpace(c.Query("ref"))
	if ref == "" {
		ref = strings.TrimSpace(c.Query("id"))
	}
	request := domain.RunnerWorkspaceRequest{
		RunID: runnerWorkspaceRunID(run), RunStatus: run.Status, WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout, Operation: operation,
		Path: path, Query: c.Query("q"), Ref: ref, Repositories: run.RepositoryContexts(),
	}
	response, status, reason := h.requestRunnerWorkspace(c.Request.Context(), runnerID, request)
	if status == http.StatusServiceUnavailable {
		c.JSON(status, gin.H{"error": reason})
		return
	}
	if status != 0 {
		h.respondWorkspaceUnavailable(c, run, owner, status, reason, false)
		return
	}
	if response.Error != "" {
		status := runnerWorkspaceErrorStatus(response.Status)
		if operation == domain.RunnerWorkspaceOperationInfo && status == http.StatusNotFound {
			c.JSON(http.StatusOK, workspaceUnavailablePayload(run, response.Error))
			return
		}
		c.JSON(status, map[string]string{"error": response.Error})
		return
	}
	if len(response.Payload) == 0 {
		c.JSON(http.StatusBadGateway, map[string]string{"error": "远程执行机器返回了空响应"})
		return
	}
	if operation != domain.RunnerWorkspaceOperationInfo {
		h.touchWorkspaceBrowse(c, owner)
	}
	if operation == domain.RunnerWorkspaceOperationInfo {
		var payload map[string]any
		if json.Unmarshal(response.Payload, &payload) == nil {
			payload["readOnly"] = !h.workspaceWritable(c, run)
			payload["pauseRequested"] = run.PauseRequested
			payload["repositoryDetails"] = workspaceRepositoryDetails(run)
			h.workspaceLifecycleInfo(c, owner, payload, true)
			c.JSON(200, payload)
			return
		}
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", response.Payload)
}

func searchWorkspace(ctx context.Context, root, query string) ([]workspaceSearchResult, bool, error) {
	entries, treeTruncated, err := readWorkspaceTree(root, "")
	if err != nil {
		return nil, false, err
	}
	needle := strings.ToLower(query)
	results := make([]workspaceSearchResult, 0, 32)
	var scanned int64
	truncated := treeTruncated
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if entry.Type != "file" || entry.Size > 256*1024 {
			continue
		}
		if scanned+entry.Size > 20*1024*1024 {
			truncated = true
			break
		}
		scanned += entry.Size
		content, _, readErr := readWorkspaceFile(root, entry.Path)
		if readErr != nil {
			continue
		}
		for lineIndex, line := range strings.Split(content, "\n") {
			if !strings.Contains(strings.ToLower(line), needle) {
				continue
			}
			preview := strings.TrimSpace(line)
			previewRunes := []rune(preview)
			if len(previewRunes) > 240 {
				preview = string(previewRunes[:240]) + "…"
			}
			results = append(results, workspaceSearchResult{Path: entry.Path, Line: lineIndex + 1, Preview: preview})
			if len(results) >= 100 {
				return results, true, nil
			}
		}
	}
	return results, truncated, nil
}

func respondWorkspaceError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, os.ErrNotExist) {
		status = http.StatusNotFound
	}
	c.JSON(status, map[string]string{"error": err.Error()})
}

func resolveWorkspacePath(root, raw string, wantDir bool) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if strings.ContainsRune(raw, '\x00') || filepath.IsAbs(raw) {
		return "", "", fmt.Errorf("invalid workspace path")
	}
	rel := filepath.Clean(filepath.FromSlash(raw))
	if rel == "." {
		rel = ""
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || workspacePathSensitive(filepath.ToSlash(rel)) {
		return "", "", fmt.Errorf("workspace path is not accessible")
	}
	target := filepath.Join(root, rel)
	info, err := os.Lstat(target)
	if err != nil {
		return "", "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("symbolic links are not accessible")
	}
	if wantDir && !info.IsDir() {
		return "", "", fmt.Errorf("workspace path is not a directory")
	}
	if !wantDir && !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("workspace path is not a regular file")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", "", err
	}
	contained, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil || contained == ".." || strings.HasPrefix(contained, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("workspace path escapes its root")
	}
	return target, filepath.ToSlash(rel), nil
}

func workspacePathSensitive(rel string) bool {
	parts := strings.Split(strings.ToLower(filepath.ToSlash(rel)), "/")
	for i, part := range parts {
		switch part {
		case ".git", ".agentflow", ".ssh", ".gnupg", ".netrc", ".npmrc", "id_rsa", "id_ed25519":
			return true
		case "private-home", "runtime":
			if i > 0 && parts[i-1] == ".agentflow" {
				return true
			}
		}
		if part == ".env" || strings.HasPrefix(part, ".env.") && !strings.HasSuffix(part, ".example") {
			return true
		}
	}
	return false
}

func workspaceTreeSkipped(rel string, d fs.DirEntry) bool {
	if workspacePathSensitive(rel) {
		return true
	}
	if !d.IsDir() {
		return false
	}
	switch strings.ToLower(d.Name()) {
	case ".agentflow", "node_modules", ".cache", "vendor":
		return true
	}
	return false
}

func readWorkspaceTree(root, raw string) ([]workspaceEntry, bool, error) {
	start, _, err := resolveWorkspacePath(root, raw, true)
	if err != nil {
		return nil, false, err
	}
	entries := make([]workspaceEntry, 0, 256)
	truncated := false
	err = filepath.WalkDir(start, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == start {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if workspaceTreeSkipped(rel, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if len(entries) >= workspaceTreeLimit {
			truncated = true
			return fs.SkipAll
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		kind := "file"
		if d.IsDir() {
			kind = "directory"
		}
		entries = append(entries, workspaceEntry{Path: rel, Name: d.Name(), Type: kind, Size: info.Size(), Modified: info.ModTime()})
		return nil
	})
	entries = pruneEmptyWorkspaceDirs(entries)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, truncated, err
}

// pruneEmptyWorkspaceDirs drops directory entries that have no visible file
// beneath them, filtering entries in place and keeping the original order.
func pruneEmptyWorkspaceDirs(entries []workspaceEntry) []workspaceEntry {
	visibleParents := map[string]bool{}
	for _, entry := range entries {
		if entry.Type != "file" {
			continue
		}
		for parent := filepath.ToSlash(filepath.Dir(entry.Path)); parent != "." && parent != ""; parent = filepath.ToSlash(filepath.Dir(parent)) {
			visibleParents[parent] = true
		}
	}
	visible := entries[:0]
	for _, entry := range entries {
		if entry.Type == "directory" && !visibleParents[entry.Path] {
			continue
		}
		visible = append(visible, entry)
	}
	return visible
}

func readWorkspaceFile(root, raw string) (string, int64, error) {
	_, rel, err := resolveWorkspacePath(root, raw, false)
	if err != nil {
		return "", 0, err
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return "", 0, err
	}
	defer rootHandle.Close()
	file, err := rootHandle.Open(filepath.FromSlash(rel))
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("workspace path is not a regular file")
	}
	if info.Size() > workspaceFileLimit {
		return "", info.Size(), fmt.Errorf("file exceeds the 1 MiB preview limit")
	}
	body, err := io.ReadAll(io.LimitReader(file, workspaceFileLimit+1))
	if err != nil {
		return "", 0, err
	}
	if len(body) > workspaceFileLimit {
		return "", info.Size(), fmt.Errorf("file exceeds the 1 MiB preview limit")
	}
	if bytes.IndexByte(body, 0) >= 0 {
		return "", info.Size(), fmt.Errorf("binary files cannot be previewed")
	}
	return string(body), info.Size(), nil
}

func workspaceGitRepositoryName(label string) string {
	label = strings.TrimSpace(label)
	if label == "" || label == "." {
		return "workspace"
	}
	return label
}

func workspaceGitRepositoryQuery(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "workspace" {
		return ""
	}
	return name
}

func collectWorkspaceGitRepositories(root string, run domain.Run) []string {
	names := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, repository := range workspaceRepositoryRoots(root, run) {
		if _, err := os.Stat(filepath.Join(repository.path, ".git")); err != nil {
			continue
		}
		name := workspaceGitRepositoryName(repository.label)
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

func workspaceRepositoryRoots(root string, run domain.Run) []struct{ path, label string } {
	repositories := run.RepositoryContexts()
	if len(repositories) == 0 {
		return []struct{ path, label string }{{root, ""}}
	}
	result := make([]struct{ path, label string }, 0, len(repositories))
	for _, repository := range repositories {
		dir := filepath.Clean(filepath.FromSlash(repository.WorkspaceDir))
		if dir == "." {
			dir = ""
		}
		if dir == ".." || strings.HasPrefix(dir, ".."+string(filepath.Separator)) {
			continue
		}
		result = append(result, struct{ path, label string }{filepath.Join(root, dir), filepath.ToSlash(dir)})
	}
	return result
}

func collectWorkspaceChanges(ctx context.Context, root string, run domain.Run) ([]workspaceChange, string) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	changes := []workspaceChange{}
	for _, repository := range workspaceRepositoryRoots(root, run) {
		if _, err := os.Stat(filepath.Join(repository.path, ".git")); err != nil {
			continue
		}
		out, err := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", repository.path, "status", "--porcelain=v1", "-z", "--untracked-files=all").Output()
		if err != nil {
			return changes, "Git 状态读取失败"
		}
		items := bytes.Split(out, []byte{0})
		for i := 0; i < len(items); i++ {
			item := items[i]
			if len(item) < 4 {
				continue
			}
			path := string(item[3:])
			if workspacePathSensitive(filepath.ToSlash(filepath.Join(repository.label, path))) {
				continue
			}
			changes = append(changes, workspaceChange{Path: filepath.ToSlash(filepath.Join(repository.label, path)), Repository: workspaceGitRepositoryName(repository.label), Index: string(item[0]), Worktree: string(item[1])})
			if item[0] == 'R' || item[0] == 'C' || item[1] == 'R' || item[1] == 'C' {
				i++ // porcelain -z follows a rename/copy with the original path
			}
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, ""
}

func readWorkspaceDiff(ctx context.Context, root string, run domain.Run, raw string) (string, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw != "" {
		if _, _, err := resolveWorkspacePath(root, raw, false); err != nil {
			return "", false, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var combined bytes.Buffer
	for _, repository := range workspaceRepositoryRoots(root, run) {
		if _, err := os.Stat(filepath.Join(repository.path, ".git")); err != nil {
			continue
		}
		rel := raw
		if repository.label != "" {
			prefix := strings.TrimSuffix(repository.label, "/") + "/"
			if raw != "" && !strings.HasPrefix(raw, prefix) {
				continue
			}
			rel = strings.TrimPrefix(raw, prefix)
		}
		// Browsing must never take .git/index.lock: a concurrent preparation
		// or commit would otherwise fail with "index.lock: File exists".
		args := []string{"--no-optional-locks", "-C", repository.path, "diff", "--no-ext-diff", "--no-color", "HEAD", "--"}
		if rel != "" {
			args = append(args, filepath.FromSlash(rel))
		}
		out, err := exec.CommandContext(ctx, "git", args...).Output()
		if err != nil {
			return "", false, fmt.Errorf("read git diff: %w", err)
		}
		combined.Write(out)
		if combined.Len() > workspaceFileLimit {
			return combined.String()[:workspaceFileLimit], true, nil
		}
	}
	return combined.String(), false, nil
}

func readWorkspaceBaseFile(ctx context.Context, root string, run domain.Run, raw string) (string, bool, error) {
	_, workspaceRel, err := resolveWorkspacePath(root, raw, false)
	if err != nil {
		return "", false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, repository := range workspaceRepositoryRoots(root, run) {
		rel := workspaceRel
		if repository.label != "" {
			prefix := strings.TrimSuffix(repository.label, "/") + "/"
			if !strings.HasPrefix(filepath.ToSlash(workspaceRel), prefix) {
				continue
			}
			rel = strings.TrimPrefix(filepath.ToSlash(workspaceRel), prefix)
		}
		if _, err := os.Stat(filepath.Join(repository.path, ".git")); err != nil {
			continue
		}
		// The base is HEAD's blob. A path HEAD never had (untracked, or
		// registered with `git add -N` by a node) has an empty base.
		inHead := exec.CommandContext(ctx, "git", "-C", repository.path, "cat-file", "-e", "HEAD:"+filepath.ToSlash(rel))
		if err := inHead.Run(); err != nil {
			return "", false, nil
		}
		out, err := exec.CommandContext(ctx, "git", "-C", repository.path, "show", "HEAD:"+filepath.ToSlash(rel)).Output()
		if err != nil {
			return "", false, fmt.Errorf("read base file: %w", err)
		}
		if len(out) > workspaceFileLimit {
			return string(out[:workspaceFileLimit]), true, nil
		}
		if bytes.IndexByte(out, 0) >= 0 {
			return "", false, fmt.Errorf("binary files cannot be previewed")
		}
		return string(out), false, nil
	}
	return "", false, nil
}

func safeGitObjectID(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 7 || len(raw) > 64 {
		return "", false
	}
	for _, r := range raw {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F' {
			continue
		}
		return "", false
	}
	return raw, true
}

func workspaceLogicalPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.ContainsRune(raw, '\x00') || filepath.IsAbs(raw) {
		return "", fmt.Errorf("invalid workspace path")
	}
	rel := filepath.Clean(filepath.FromSlash(raw))
	if rel == "." {
		rel = ""
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || workspacePathSensitive(filepath.ToSlash(rel)) {
		return "", fmt.Errorf("workspace path is not accessible")
	}
	return filepath.ToSlash(rel), nil
}

func findWorkspaceRepository(root string, run domain.Run, name string) (path, label string, ok bool) {
	name = workspaceGitRepositoryQuery(name)
	roots := workspaceRepositoryRoots(root, run)
	if name == "" {
		for _, repository := range roots {
			if repository.label == "" {
				return repository.path, repository.label, true
			}
		}
		if len(roots) == 1 {
			return roots[0].path, roots[0].label, true
		}
		return "", "", false
	}
	for _, repository := range roots {
		if repository.label == name {
			return repository.path, repository.label, true
		}
	}
	return "", "", false
}

func collectWorkspaceLog(ctx context.Context, root string, run domain.Run, name string) (gin.H, error) {
	path, label, ok := findWorkspaceRepository(root, run, name)
	if !ok {
		return nil, fmt.Errorf("repository not found")
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		return gin.H{"repository": label, "branch": "", "head": "", "commits": []workspaceCommit{}}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	branchOut, _ := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD").Output()
	headOut, _ := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "HEAD").Output()
	out, err := exec.CommandContext(ctx, "git", "-C", path, "log", "-n", strconv.Itoa(workspaceLogLimit), "--format=%H%x1f%P%x1f%s%x1f%an%x1f%at").Output()
	if err != nil {
		return gin.H{"repository": label, "branch": strings.TrimSpace(string(branchOut)), "head": strings.TrimSpace(string(headOut)), "commits": []workspaceCommit{}}, nil
	}
	commits := []workspaceCommit{}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\x1f")
		if len(parts) < 5 {
			continue
		}
		parents := []string{}
		for _, parent := range strings.Fields(parts[1]) {
			if parent != "" {
				parents = append(parents, parent)
			}
		}
		unix, _ := strconv.ParseInt(strings.TrimSpace(parts[4]), 10, 64)
		commits = append(commits, workspaceCommit{
			ID:        strings.TrimSpace(parts[0]),
			ParentIDs: parents,
			Message:   parts[2],
			Author:    parts[3],
			Timestamp: unix * 1000,
		})
	}
	return gin.H{"repository": label, "branch": strings.TrimSpace(string(branchOut)), "head": strings.TrimSpace(string(headOut)), "commits": commits}, nil
}

func collectWorkspaceCommitFiles(ctx context.Context, root string, run domain.Run, name, id string) ([]workspaceCommitFile, error) {
	commit, ok := safeGitObjectID(id)
	if !ok {
		return nil, fmt.Errorf("invalid commit id")
	}
	path, label, found := findWorkspaceRepository(root, run, name)
	if !found {
		return nil, fmt.Errorf("repository not found")
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		return []workspaceCommitFile{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", path, "diff-tree", "--no-commit-id", "--name-status", "-r", "--root", "--no-renames", "-z", commit).Output()
	if err != nil {
		return nil, fmt.Errorf("read commit files: %w", err)
	}
	items := bytes.Split(out, []byte{0})
	files := []workspaceCommitFile{}
	for i := 0; i+1 < len(items); i += 2 {
		status := strings.TrimSpace(string(items[i]))
		rel := strings.TrimSpace(string(items[i+1]))
		if status == "" || rel == "" {
			continue
		}
		full := filepath.ToSlash(filepath.Join(label, rel))
		if workspacePathSensitive(full) {
			continue
		}
		files = append(files, workspaceCommitFile{Path: full, Status: string(status[0])})
	}
	return files, nil
}

func readWorkspaceGitBlob(ctx context.Context, root string, run domain.Run, raw, ref string) (string, int64, error) {
	object, ok := safeGitObjectID(ref)
	if !ok {
		return "", 0, fmt.Errorf("invalid git ref")
	}
	rel, err := workspaceLogicalPath(raw)
	if err != nil {
		return "", 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, repository := range workspaceRepositoryRoots(root, run) {
		fileRel := rel
		if repository.label != "" {
			prefix := strings.TrimSuffix(repository.label, "/") + "/"
			if !strings.HasPrefix(rel, prefix) && rel != repository.label {
				continue
			}
			fileRel = strings.TrimPrefix(rel, prefix)
		}
		if _, err := os.Stat(filepath.Join(repository.path, ".git")); err != nil {
			continue
		}
		out, err := exec.CommandContext(ctx, "git", "-C", repository.path, "show", object+":"+fileRel).Output()
		if err != nil {
			return "", 0, nil
		}
		if len(out) > workspaceFileLimit {
			return string(out[:workspaceFileLimit]), int64(workspaceFileLimit), nil
		}
		if bytes.IndexByte(out, 0) >= 0 {
			return "", 0, fmt.Errorf("binary files cannot be previewed")
		}
		return string(out), int64(len(out)), nil
	}
	return "", 0, nil
}
