package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

const (
	runnerWorkspaceTreeLimit = 50000
	runnerWorkspaceFileLimit = 1024 * 1024
)

type runnerWorkspaceEntry struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	Size     int64     `json:"size,omitempty"`
	Modified time.Time `json:"modifiedAt,omitempty"`
}

type runnerWorkspaceChange struct {
	Path       string `json:"path"`
	Repository string `json:"repository,omitempty"`
	Index      string `json:"index"`
	Worktree   string `json:"worktree"`
}

type runnerWorkspaceSearchResult struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Preview string `json:"preview"`
}

type runnerWorkspaceCommit struct {
	ID        string   `json:"id"`
	ParentIDs []string `json:"parentIds"`
	Message   string   `json:"message"`
	Author    string   `json:"author"`
	Timestamp int64    `json:"timestamp"`
}

type runnerWorkspaceCommitFile struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

const runnerWorkspaceLogLimit = 80

func (d *Daemon) readRunnerWorkspace(ctx context.Context, request domain.RunnerWorkspaceRequest) domain.RunnerWorkspaceResponse {
	root, err := d.runnerWorkspaceRoot(request)
	if err != nil {
		return runnerWorkspaceError(err)
	}
	if (request.Operation == domain.RunnerWorkspaceOperationRestore || request.Operation == domain.RunnerWorkspaceOperationCheckpoint) && d.workspaceTerminals.HasRunScope(request.RunID) {
		return domain.RunnerWorkspaceResponse{Status: 409, Error: "workspace terminal is still active"}
	}
	if request.Operation == domain.RunnerWorkspaceOperationRestore {
		gate := d.workspaceGate(request.RunID)
		if !gate.TryLock() {
			return domain.RunnerWorkspaceResponse{Status: 409, Error: "workspace is still in use"}
		}
		defer gate.Unlock()
		if err := runtime.RestoreWorkspaceCheckpoint(root, request.Checkpoint); err != nil {
			return runnerWorkspaceError(err)
		}
		return runnerWorkspacePayload(map[string]bool{"restored": true})
	}
	if request.Operation == domain.RunnerWorkspaceOperationPrepare {
		return d.prepareRunnerWorkspace(ctx, root, request)
	}
	if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
		if statErr == nil {
			statErr = fmt.Errorf("workspace is not a directory")
		}
		return runnerWorkspaceError(statErr)
	}

	if request.Operation == domain.RunnerWorkspaceOperationPreparationRecord {
		return d.recordRunnerWorkspacePreparation(ctx, root, request)
	}
	if request.Operation == domain.RunnerWorkspaceOperationPreparationStatus {
		return runnerWorkspacePayload(d.runnerWorkspacePreparationStatus(ctx, root, request))
	}

	groupWrite := request.CollaborationMode == "group" && (request.RunStatus == "running" || request.RunStatus == "waiting")
	switch request.Operation {
	case domain.RunnerWorkspaceOperationArtifacts:
		if !groupWrite {
			gate := d.workspaceGate(request.RunID)
			if !gate.TryLock() {
				return domain.RunnerWorkspaceResponse{Status: 409, Error: "workspace is still in use"}
			}
			defer gate.Unlock()
			if request.RunStatus == "running" || request.RunStatus == "queued" {
				return domain.RunnerWorkspaceResponse{Status: 409, Error: "run must be stopped before restoring archived outputs"}
			}
		}
		restored, err := runtime.RestoreMissingWorkspaceArtifacts(root, request.ArtifactFiles)
		if err != nil {
			return runnerWorkspaceError(err)
		}
		return runnerWorkspacePayload(map[string]any{"restoredArtifacts": restored})
	case domain.RunnerWorkspaceOperationCheckpoint:
		gate := d.workspaceGate(request.RunID)
		if !gate.TryLock() {
			return domain.RunnerWorkspaceResponse{Status: 409, Error: "workspace is still in use"}
		}
		defer gate.Unlock()
		archive, err := runtime.CreateWorkspaceCheckpoint(root)
		if err != nil {
			return runnerWorkspaceError(err)
		}
		return runnerWorkspacePayload(map[string]any{"checkpoint": archive})
	case domain.RunnerWorkspaceOperationTerminal:
		if request.Terminal == nil || request.TerminalOwner <= 0 {
			return domain.RunnerWorkspaceResponse{Status: 400, Error: "invalid terminal request"}
		}
		scope := fmt.Sprintf("%s:%d", request.RunID, request.TerminalOwner)
		if request.Terminal.Operation == "close-all" {
			d.workspaceTerminals.CloseScope(scope)
			return runnerWorkspacePayload(map[string]bool{"ok": true})
		}
		if !groupWrite && request.RunStatus != "waiting" && request.RunStatus != "succeeded" && request.RunStatus != "failed" && request.RunStatus != "cancelled" {
			return domain.RunnerWorkspaceResponse{Status: 409, Error: "stop execution before editing"}
		}
		result, err := d.handleRunnerWorkspaceTerminal(ctx, root, scope, request, groupWrite)
		if err != nil {
			return domain.RunnerWorkspaceResponse{Status: 409, Error: err.Error()}
		}
		return runnerWorkspacePayload(result)

	case domain.RunnerWorkspaceOperationWrite:
		// The same human may save files while their terminal holds the fence.
		scope := fmt.Sprintf("%s:%d", request.RunID, request.TerminalOwner)
		if !groupWrite && (request.TerminalOwner <= 0 || !d.workspaceTerminals.HasScope(scope)) {
			gate := d.workspaceGate(request.RunID)
			if !gate.TryLock() {
				return domain.RunnerWorkspaceResponse{Status: 409, Error: "execution is still stopping"}
			}
			defer gate.Unlock()
		}
		if !groupWrite && request.RunStatus != "waiting" && request.RunStatus != "succeeded" && request.RunStatus != "failed" && request.RunStatus != "cancelled" {
			return domain.RunnerWorkspaceResponse{Status: 409, Error: "run must be paused before editing"}
		}
		for _, id := range d.activeTaskIDs() {
			if !groupWrite && strings.Contains(id, request.RunID) {
				return domain.RunnerWorkspaceResponse{Status: 409, Error: "execution is still stopping"}
			}
		}
		sha, err := runtime.WriteWorkspaceFile(root, request.Path, request.Content, request.ExpectedSHA256)
		if err != nil {
			return domain.RunnerWorkspaceResponse{Status: 409, Error: err.Error()}
		}
		return runnerWorkspacePayload(map[string]any{"sha256": sha})
	case domain.RunnerWorkspaceOperationInfo:
		changes, warning := runnerCollectWorkspaceChanges(ctx, root, request.Repositories)
		payload := map[string]any{
			"available": true, "readOnly": true, "runStatus": request.RunStatus,
			"changes": changes, "repositories": runnerCollectWorkspaceGitRepositories(root, request.Repositories), "warning": warning,
		}
		return runnerWorkspacePayload(payload)
	case domain.RunnerWorkspaceOperationTree:
		entries, truncated, readErr := runnerReadWorkspaceTree(root, request.Path)
		if readErr != nil {
			return runnerWorkspaceError(readErr)
		}
		return runnerWorkspacePayload(map[string]any{"entries": entries, "truncated": truncated})
	case domain.RunnerWorkspaceOperationFile:
		if strings.TrimSpace(request.Path) == "" {
			return domain.RunnerWorkspaceResponse{Error: "path is required", Status: http.StatusBadRequest}
		}
		if strings.TrimSpace(request.Ref) != "" {
			content, size, readErr := runnerReadWorkspaceGitBlob(ctx, root, request.Repositories, request.Path, request.Ref)
			if readErr != nil {
				return runnerWorkspaceError(readErr)
			}
			return runnerWorkspacePayload(map[string]any{"path": filepath.ToSlash(request.Path), "content": content, "size": size, "ref": request.Ref})
		}
		content, size, readErr := runnerReadWorkspaceFile(root, request.Path)
		if readErr != nil {
			return runnerWorkspaceError(readErr)
		}
		return runnerWorkspacePayload(map[string]any{"path": filepath.ToSlash(request.Path), "content": content, "size": size, "sha256": runtime.WorkspaceContentSHA(content)})
	case domain.RunnerWorkspaceOperationLog:
		log, readErr := runnerCollectWorkspaceLog(ctx, root, request.Repositories, request.Path)
		if readErr != nil {
			return runnerWorkspaceError(readErr)
		}
		return runnerWorkspacePayload(log)
	case domain.RunnerWorkspaceOperationCommit:
		files, readErr := runnerCollectWorkspaceCommitFiles(ctx, root, request.Repositories, request.Path, request.Ref)
		if readErr != nil {
			return runnerWorkspaceError(readErr)
		}
		return runnerWorkspacePayload(map[string]any{"id": strings.TrimSpace(request.Ref), "files": files})
	case domain.RunnerWorkspaceOperationDiff:
		diff, truncated, readErr := runnerReadWorkspaceDiff(ctx, root, request.Repositories, request.Path)
		if readErr != nil {
			return runnerWorkspaceError(readErr)
		}
		if strings.TrimSpace(request.Path) == "" {
			return runnerWorkspacePayload(map[string]any{"diff": diff, "truncated": truncated})
		}
		original, originalTruncated, readErr := runnerReadWorkspaceBaseFile(ctx, root, request.Repositories, request.Path)
		if readErr != nil {
			return runnerWorkspaceError(readErr)
		}
		modified, _, readErr := runnerReadWorkspaceFile(root, request.Path)
		if readErr != nil {
			return runnerWorkspaceError(readErr)
		}
		return runnerWorkspacePayload(map[string]any{"diff": diff, "original": original, "modified": modified, "truncated": truncated || originalTruncated})
	case domain.RunnerWorkspaceOperationSearch:
		if len([]rune(strings.TrimSpace(request.Query))) < 2 {
			return domain.RunnerWorkspaceResponse{Error: "q must contain at least 2 characters", Status: http.StatusBadRequest}
		}
		results, truncated, readErr := runnerSearchWorkspace(ctx, root, request.Query)
		if readErr != nil {
			return runnerWorkspaceError(readErr)
		}
		return runnerWorkspacePayload(map[string]any{"results": results, "truncated": truncated})
	default:
		return domain.RunnerWorkspaceResponse{Error: "unsupported workspace operation", Status: http.StatusBadRequest}
	}
}

func (d *Daemon) runnerWorkspaceRoot(request domain.RunnerWorkspaceRequest) (string, error) {
	runID := strings.TrimSpace(request.RunID)
	if runID == "" || safeWorkspaceComponent(runID) != runID {
		return "", fmt.Errorf("invalid run id")
	}
	root, err := filepath.Abs(d.cfg.DefaultWorkspaceRoot)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, "runs", runID)
	if request.WorkspaceLayout == domain.RunnerWorkspaceLayoutShared {
		target = filepath.Join(target, "workspace")
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace escapes runner root")
	}
	return target, nil
}

func safeWorkspaceComponent(value string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func runnerWorkspacePayload(value any) domain.RunnerWorkspaceResponse {
	payload, err := json.Marshal(value)
	if err != nil {
		return domain.RunnerWorkspaceResponse{Error: err.Error(), Status: http.StatusInternalServerError}
	}
	return domain.RunnerWorkspaceResponse{Payload: payload, Status: http.StatusOK}
}

func runnerWorkspaceError(err error) domain.RunnerWorkspaceResponse {
	status := http.StatusBadRequest
	if errors.Is(err, os.ErrNotExist) {
		status = http.StatusNotFound
	}
	return domain.RunnerWorkspaceResponse{Error: err.Error(), Status: status}
}

func runnerResolveWorkspacePath(root, raw string, wantDir bool) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if strings.ContainsRune(raw, '\x00') || filepath.IsAbs(raw) {
		return "", "", fmt.Errorf("invalid workspace path")
	}
	rel := filepath.Clean(filepath.FromSlash(raw))
	if rel == "." {
		rel = ""
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || runnerWorkspacePathSensitive(filepath.ToSlash(rel)) {
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

func runnerWorkspacePathSensitive(rel string) bool {
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

func runnerWorkspaceTreeSkipped(rel string, entry fs.DirEntry) bool {
	if runnerWorkspacePathSensitive(rel) {
		return true
	}
	if !entry.IsDir() {
		return false
	}
	switch strings.ToLower(entry.Name()) {
	case ".agentflow", "node_modules", ".cache", "vendor":
		return true
	}
	return false
}

func runnerReadWorkspaceTree(root, raw string) ([]runnerWorkspaceEntry, bool, error) {
	start, _, err := runnerResolveWorkspacePath(root, raw, true)
	if err != nil {
		return nil, false, err
	}
	entries := make([]runnerWorkspaceEntry, 0, 256)
	truncated := false
	err = filepath.WalkDir(start, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || path == start {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if runnerWorkspaceTreeSkipped(rel, entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if len(entries) >= runnerWorkspaceTreeLimit {
			truncated = true
			return fs.SkipAll
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		kind := "file"
		if entry.IsDir() {
			kind = "directory"
		}
		entries = append(entries, runnerWorkspaceEntry{Path: rel, Name: entry.Name(), Type: kind, Size: info.Size(), Modified: info.ModTime()})
		return nil
	})
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
		if entry.Type != "directory" || visibleParents[entry.Path] {
			visible = append(visible, entry)
		}
	}
	sort.Slice(visible, func(i, j int) bool { return visible[i].Path < visible[j].Path })
	return visible, truncated, err
}

func runnerReadWorkspaceFile(root, raw string) (string, int64, error) {
	_, rel, err := runnerResolveWorkspacePath(root, raw, false)
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
	if info.Size() > runnerWorkspaceFileLimit {
		return "", info.Size(), fmt.Errorf("file exceeds the 1 MiB preview limit")
	}
	body, err := io.ReadAll(io.LimitReader(file, runnerWorkspaceFileLimit+1))
	if err != nil {
		return "", 0, err
	}
	if len(body) > runnerWorkspaceFileLimit {
		return "", info.Size(), fmt.Errorf("file exceeds the 1 MiB preview limit")
	}
	if bytes.IndexByte(body, 0) >= 0 {
		return "", info.Size(), fmt.Errorf("binary files cannot be previewed")
	}
	return string(body), info.Size(), nil
}

func runnerSearchWorkspace(ctx context.Context, root, query string) ([]runnerWorkspaceSearchResult, bool, error) {
	entries, treeTruncated, err := runnerReadWorkspaceTree(root, "")
	if err != nil {
		return nil, false, err
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	results := make([]runnerWorkspaceSearchResult, 0, 32)
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
		content, _, readErr := runnerReadWorkspaceFile(root, entry.Path)
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
			results = append(results, runnerWorkspaceSearchResult{Path: entry.Path, Line: lineIndex + 1, Preview: preview})
			if len(results) >= 100 {
				return results, true, nil
			}
		}
	}
	return results, truncated, nil
}

type runnerRepositoryRoot struct{ path, label string }

func runnerGitRepositoryName(label string) string {
	label = strings.TrimSpace(label)
	if label == "" || label == "." {
		return "workspace"
	}
	return label
}

func runnerGitRepositoryQuery(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "workspace" {
		return ""
	}
	return name
}

func runnerCollectWorkspaceGitRepositories(root string, repositories []domain.RepositoryContext) []string {
	names := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, repository := range runnerWorkspaceRepositoryRoots(root, repositories) {
		if _, err := os.Stat(filepath.Join(repository.path, ".git")); err != nil {
			continue
		}
		name := runnerGitRepositoryName(repository.label)
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

func runnerWorkspaceRepositoryRoots(root string, repositories []domain.RepositoryContext) []runnerRepositoryRoot {
	if len(repositories) == 0 {
		return []runnerRepositoryRoot{{path: root}}
	}
	result := make([]runnerRepositoryRoot, 0, len(repositories))
	for _, repository := range repositories {
		dir := filepath.Clean(filepath.FromSlash(repository.WorkspaceDir))
		if dir == "." {
			dir = ""
		}
		if dir == ".." || strings.HasPrefix(dir, ".."+string(filepath.Separator)) {
			continue
		}
		result = append(result, runnerRepositoryRoot{path: filepath.Join(root, dir), label: filepath.ToSlash(dir)})
	}
	return result
}

func runnerCollectWorkspaceChanges(ctx context.Context, root string, repositories []domain.RepositoryContext) ([]runnerWorkspaceChange, string) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	changes := []runnerWorkspaceChange{}
	for _, repository := range runnerWorkspaceRepositoryRoots(root, repositories) {
		if _, err := os.Stat(filepath.Join(repository.path, ".git")); err != nil {
			continue
		}
		out, err := exec.CommandContext(ctx, "git", "-C", repository.path, "status", "--porcelain=v1", "-z", "--untracked-files=all").Output()
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
			if runnerWorkspacePathSensitive(filepath.ToSlash(filepath.Join(repository.label, path))) {
				continue
			}
			changes = append(changes, runnerWorkspaceChange{Path: filepath.ToSlash(filepath.Join(repository.label, path)), Repository: runnerGitRepositoryName(repository.label), Index: string(item[0]), Worktree: string(item[1])})
			if item[0] == 'R' || item[0] == 'C' || item[1] == 'R' || item[1] == 'C' {
				i++
			}
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, ""
}

func runnerReadWorkspaceDiff(ctx context.Context, root string, repositories []domain.RepositoryContext, raw string) (string, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw != "" {
		if _, _, err := runnerResolveWorkspacePath(root, raw, false); err != nil {
			return "", false, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var combined bytes.Buffer
	for _, repository := range runnerWorkspaceRepositoryRoots(root, repositories) {
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
		args := []string{"-C", repository.path, "diff", "--no-ext-diff", "--no-color", "HEAD", "--"}
		if rel != "" {
			args = append(args, filepath.FromSlash(rel))
		}
		out, err := exec.CommandContext(ctx, "git", args...).Output()
		if err != nil {
			return "", false, fmt.Errorf("read git diff: %w", err)
		}
		combined.Write(out)
		if combined.Len() > runnerWorkspaceFileLimit {
			return combined.String()[:runnerWorkspaceFileLimit], true, nil
		}
	}
	return combined.String(), false, nil
}

func runnerReadWorkspaceBaseFile(ctx context.Context, root string, repositories []domain.RepositoryContext, raw string) (string, bool, error) {
	_, workspaceRel, err := runnerResolveWorkspacePath(root, raw, false)
	if err != nil {
		return "", false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, repository := range runnerWorkspaceRepositoryRoots(root, repositories) {
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
		if err := exec.CommandContext(ctx, "git", "-C", repository.path, "ls-files", "--error-unmatch", "--", filepath.FromSlash(rel)).Run(); err != nil {
			return "", false, nil
		}
		out, err := exec.CommandContext(ctx, "git", "-C", repository.path, "show", "HEAD:"+filepath.ToSlash(rel)).Output()
		if err != nil {
			return "", false, fmt.Errorf("read base file: %w", err)
		}
		if len(out) > runnerWorkspaceFileLimit {
			return string(out[:runnerWorkspaceFileLimit]), true, nil
		}
		if bytes.IndexByte(out, 0) >= 0 {
			return "", false, fmt.Errorf("binary files cannot be previewed")
		}
		return string(out), false, nil
	}
	return "", false, nil
}

func runnerSafeGitObjectID(raw string) (string, bool) {
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

func runnerWorkspaceLogicalPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.ContainsRune(raw, '\x00') || filepath.IsAbs(raw) {
		return "", fmt.Errorf("invalid workspace path")
	}
	rel := filepath.Clean(filepath.FromSlash(raw))
	if rel == "." {
		rel = ""
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || runnerWorkspacePathSensitive(filepath.ToSlash(rel)) {
		return "", fmt.Errorf("workspace path is not accessible")
	}
	return filepath.ToSlash(rel), nil
}

func runnerFindWorkspaceRepository(root string, repositories []domain.RepositoryContext, name string) (path, label string, ok bool) {
	name = runnerGitRepositoryQuery(name)
	roots := runnerWorkspaceRepositoryRoots(root, repositories)
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

func runnerCollectWorkspaceLog(ctx context.Context, root string, repositories []domain.RepositoryContext, name string) (map[string]any, error) {
	path, label, ok := runnerFindWorkspaceRepository(root, repositories, name)
	if !ok {
		return nil, fmt.Errorf("repository not found")
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		return map[string]any{"repository": label, "branch": "", "head": "", "commits": []runnerWorkspaceCommit{}}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	branchOut, _ := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD").Output()
	headOut, _ := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "HEAD").Output()
	out, err := exec.CommandContext(ctx, "git", "-C", path, "log", "-n", strconv.Itoa(runnerWorkspaceLogLimit), "--format=%H%x1f%P%x1f%s%x1f%an%x1f%at").Output()
	if err != nil {
		return map[string]any{"repository": label, "branch": strings.TrimSpace(string(branchOut)), "head": strings.TrimSpace(string(headOut)), "commits": []runnerWorkspaceCommit{}}, nil
	}
	commits := []runnerWorkspaceCommit{}
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
		commits = append(commits, runnerWorkspaceCommit{
			ID: strings.TrimSpace(parts[0]), ParentIDs: parents, Message: parts[2], Author: parts[3], Timestamp: unix * 1000,
		})
	}
	return map[string]any{"repository": label, "branch": strings.TrimSpace(string(branchOut)), "head": strings.TrimSpace(string(headOut)), "commits": commits}, nil
}

func runnerCollectWorkspaceCommitFiles(ctx context.Context, root string, repositories []domain.RepositoryContext, name, id string) ([]runnerWorkspaceCommitFile, error) {
	commit, ok := runnerSafeGitObjectID(id)
	if !ok {
		return nil, fmt.Errorf("invalid commit id")
	}
	path, label, found := runnerFindWorkspaceRepository(root, repositories, name)
	if !found {
		return nil, fmt.Errorf("repository not found")
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		return []runnerWorkspaceCommitFile{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", path, "diff-tree", "--no-commit-id", "--name-status", "-r", "--root", "--no-renames", "-z", commit).Output()
	if err != nil {
		return nil, fmt.Errorf("read commit files: %w", err)
	}
	items := bytes.Split(out, []byte{0})
	files := []runnerWorkspaceCommitFile{}
	for i := 0; i+1 < len(items); i += 2 {
		status := strings.TrimSpace(string(items[i]))
		rel := strings.TrimSpace(string(items[i+1]))
		if status == "" || rel == "" {
			continue
		}
		full := filepath.ToSlash(filepath.Join(label, rel))
		if runnerWorkspacePathSensitive(full) {
			continue
		}
		files = append(files, runnerWorkspaceCommitFile{Path: full, Status: string(status[0])})
	}
	return files, nil
}

func runnerReadWorkspaceGitBlob(ctx context.Context, root string, repositories []domain.RepositoryContext, raw, ref string) (string, int64, error) {
	object, ok := runnerSafeGitObjectID(ref)
	if !ok {
		return "", 0, fmt.Errorf("invalid git ref")
	}
	rel, err := runnerWorkspaceLogicalPath(raw)
	if err != nil {
		return "", 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, repository := range runnerWorkspaceRepositoryRoots(root, repositories) {
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
		if len(out) > runnerWorkspaceFileLimit {
			return string(out[:runnerWorkspaceFileLimit]), int64(runnerWorkspaceFileLimit), nil
		}
		if bytes.IndexByte(out, 0) >= 0 {
			return "", 0, fmt.Errorf("binary files cannot be previewed")
		}
		return string(out), int64(len(out)), nil
	}
	return "", 0, nil
}
