package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/runtime"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Archived outputs can refill a legacy checkout; patch inventories are not
// source files. Choose the newest version per physical path, independent of DB
// iteration order. During execution never resurrect the current/downstream
// outputs: that would make a missing new deliverable look successful.
func archivedWorkspaceFiles(run domain.Run, executingNode string) []domain.WorkspaceArtifactFile {
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
		for _, out := range n.ArtifactOutputs {
			if normalizeExecutionScope(n.ExecutionScope) == domain.ExecutionScopeRepositories {
				for _, rc := range run.RepositoryContexts() {
					if p, ok := artifactRelPath(rc.WorkspaceDir, out.Path); ok {
						excludedPaths[p] = true
					}
				}
			} else if p, ok := artifactRelPath("", out.Path); ok {
				excludedPaths[p] = true
			}
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
		hidden := false
		for _, part := range strings.Split(rel, "/") {
			if strings.HasPrefix(part, ".") {
				hidden = true
			}
		}
		if hidden {
			continue
		}
		old, exists := latest[rel]
		if !exists || a.CreatedAt.After(old.CreatedAt) || a.CreatedAt.Equal(old.CreatedAt) && (a.Attempt > old.Attempt || a.Attempt == old.Attempt && a.ID > old.ID) {
			latest[rel] = a
		}
	}
	paths := make([]string, 0, len(latest))
	for p := range latest {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	files := make([]domain.WorkspaceArtifactFile, 0, len(paths))
	for _, p := range paths {
		a := latest[p]
		sha := a.SHA256
		if sha == "" {
			sha = runtime.WorkspaceContentSHA(a.Content)
		} // legacy rows predate stored digests
		files = append(files, domain.WorkspaceArtifactFile{Path: p, Content: a.Content, SHA256: sha})
	}
	return files
}

func (e *Engine) restoreArchivedWorkspaceFiles(ctx context.Context, run domain.Run) ([]string, error) {
	return e.restoreWorkspaceFiles(ctx, run, archivedWorkspaceFiles(run, ""))
}
func (e *Engine) restoreWorkspaceFiles(ctx context.Context, run domain.Run, files []domain.WorkspaceArtifactFile) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if e.defaultExecutionRunner(run) != "" || e.foreignWorkspaceBind(run) {
		result, err := e.requestWorkspaceCheckpoint(ctx, run, domain.RunnerWorkspaceRequest{RunID: run.ID, RunStatus: run.Status, CollaborationMode: run.WorkflowSnapshot.CollaborationMode, WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout, Operation: domain.RunnerWorkspaceOperationArtifacts, ArtifactFiles: files})
		if err != nil {
			return nil, err
		}
		if result.Error != "" {
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
	if meta, ok := e.repo.WorkspaceCheckpoint(run.ID); ok && meta.SavedAt != nil {
		return nil
	}
	return archivedWorkspaceFiles(run, nodeID)
}
