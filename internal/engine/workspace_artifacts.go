package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/runtime"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// Archived outputs can refill a legacy checkout; patch inventories are not
// source files. Choose the newest version per physical path, independent of DB
// iteration order. During execution never resurrect the current/downstream
// outputs: that would make a missing new deliverable look successful.
func archivedWorkspaceArtifacts(run domain.Run, executingNode string) map[string]domain.Artifact {
	excluded := map[string]bool{}
	if executingNode != "" {
		excluded = descendants(run.WorkflowSnapshot, executingNode)
		excluded[executingNode] = true
	}
	syntheticResults := map[string]bool{}
	for _, n := range run.WorkflowSnapshot.Nodes {
		syntheticResults[n.ID] = len(n.ArtifactOutputs) == 0
	}
	excludedPaths := map[string]bool{}
	for _, n := range run.WorkflowSnapshot.Nodes {
		if !excluded[n.ID] {
			continue
		}
		for _, p := range artifactOutputFiles(run, n) {
			excludedPaths[p] = true
		}
	}
	latest := map[string]domain.Artifact{}
	for _, a := range run.Artifacts {
		if a.Name == "repository-patch" || excluded[a.NodeID] || syntheticResults[a.NodeID] && a.Name == "result" && a.Path == "result.md" {
			continue
		}
		if executingNode != "" && run.WorkflowSnapshot.CollaborationMode != "group" {
			if n := run.Nodes[a.NodeID]; n == nil || n.Status != "succeeded" {
				continue
			}
		}
		rel, valid := artifactRelPath(a.RepositoryDir, a.Path)
		if !valid {
			continue
		}
		if excludedPaths[rel] {
			continue
		}
		if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		if hasHiddenPathSegment(rel) {
			continue
		}
		old, exists := latest[rel]
		if !exists || a.CreatedAt.After(old.CreatedAt) || a.CreatedAt.Equal(old.CreatedAt) && (a.Attempt > old.Attempt || a.Attempt == old.Attempt && a.ID > old.ID) {
			latest[rel] = a
		}
	}
	return latest
}

func archivedWorkspacePaths(artifacts map[string]domain.Artifact) []string {
	paths := make([]string, 0, len(artifacts))
	for p := range artifacts {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

func archivedWorkspaceFiles(run domain.Run, executingNode string) []domain.WorkspaceArtifactFile {
	latest := archivedWorkspaceArtifacts(run, executingNode)
	paths := archivedWorkspacePaths(latest)
	files := make([]domain.WorkspaceArtifactFile, 0, len(paths))
	for _, p := range paths {
		a := latest[p]
		if len(a.Content) > domain.MaxInlineRunnerArtifactBytes || !utf8.ValidString(a.Content) || strings.ContainsRune(a.Content, 0) {
			continue
		}
		sha := a.SHA256
		if sha == "" {
			sha = runtime.WorkspaceContentSHA(a.Content)
		} // legacy rows predate stored digests
		files = append(files, domain.WorkspaceArtifactFile{Path: p, Content: a.Content, SHA256: sha})
	}
	return files
}

func (e *Engine) restoreLocalArchivedOutputs(ctx context.Context, workspace string, run domain.Run, nodeID string) error {
	if e.hasSavedWorkspace(run) {
		return nil
	}
	artifacts := archivedWorkspaceArtifacts(run, nodeID)
	for _, path := range archivedWorkspacePaths(artifacts) {
		artifact := artifacts[path]
		_, err := runtime.RestoreMissingWorkspaceArtifact(workspace, path, artifact.SHA256, func() (io.ReadCloser, error) {
			_, body, err := e.repo.OpenArtifactContent(ctx, run.ID, artifact.ID)
			return body, err
		})
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

// hasHiddenPathSegment reports whether any slash-separated segment of rel
// starts with "." (dotfiles and dot-directories are never refilled).
func hasHiddenPathSegment(rel string) bool {
	return slices.ContainsFunc(strings.Split(rel, "/"), func(part string) bool {
		return strings.HasPrefix(part, ".")
	})
}

func (e *Engine) restoreArchivedWorkspaceFiles(ctx context.Context, run domain.Run) ([]string, error) {
	return e.restoreWorkspaceFiles(ctx, run, archivedWorkspaceFiles(run, ""))
}
func (e *Engine) restoreWorkspaceFiles(ctx context.Context, run domain.Run, files []domain.WorkspaceArtifactFile) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if e.defaultExecutionRunner(run) != "" || e.foreignWorkspaceBind(run) {
		result, err := e.requestWorkspaceCheckpoint(ctx, run, domain.RunnerWorkspaceRequest{RunID: run.ID, RunStatus: run.Status, RunKind: run.Kind, WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout, Operation: domain.RunnerWorkspaceOperationArtifacts, ArtifactFiles: files})
		if err != nil {
			return nil, err
		}
		if result.Error != "" {
			// An open IDE terminal or a snapshot holds the fence. Refilling only
			// adds missing archived files, so it waits for the next use instead
			// of failing the save or terminal input that asked for it.
			if runnerWorkspaceBusy(result) {
				return nil, nil
			}
			return nil, fmt.Errorf("补回归档产物失败（执行机器需支持 restore_artifacts）：%s", result.Error)
		}
		var report struct {
			Restored []string `json:"restoredArtifacts"`
		}
		if err = json.Unmarshal(result.Payload, &report); err != nil {
			return nil, err
		}
		return report.Restored, nil
	}
	root, reason := e.LocalRunWorkspace(run)
	if reason != "" {
		return nil, fmt.Errorf("%s", reason)
	}
	return runtime.RestoreMissingWorkspaceArtifacts(root, files)
}

func (e *Engine) legacyArchivedWorkspaceFiles(run domain.Run, nodeID string) []domain.WorkspaceArtifactFile {
	if e.hasSavedWorkspace(run) {
		return nil
	}
	return archivedWorkspaceFiles(run, nodeID)
}

func (e *Engine) hasSavedWorkspace(run domain.Run) bool {
	meta, ok := e.repo.WorkspaceCheckpoint(workspaceRunID(run))
	return ok && meta.SavedAt != nil
}

func (e *Engine) runnerArchivedInputs(run domain.Run, nodeID string) []domain.RunnerInputFile {
	if e.hasSavedWorkspace(run) {
		return nil
	}
	artifacts := archivedWorkspaceArtifacts(run, nodeID)
	files := make([]domain.RunnerInputFile, 0, len(artifacts))
	for _, path := range archivedWorkspacePaths(artifacts) {
		file := runnerInputFile(artifacts[path], path, true)
		file.Restore = true
		files = append(files, file)
	}
	return files
}
