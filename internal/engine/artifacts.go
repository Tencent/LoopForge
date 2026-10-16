package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/runtime"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxArtifactBytes = 1 << 20

func materializeInputs(workspace string, wf domain.WorkflowDefinition, node domain.WorkflowNode, run domain.Run) (string, error) {
	sources := map[string]bool{}
	for _, edge := range wf.Edges {
		if edge.Target == node.ID {
			sources[edge.Source] = true
		}
	}
	prior := ancestors(wf, node.ID)
	selected := latestInputArtifacts(run.Artifacts, sources, prior)
	var manifest strings.Builder
	for _, artifact := range selected {
		if artifact.Name == "repository-patch" {
			if !sources[artifact.NodeID] {
				continue
			}
			repoWorkspace := repositoryWorkspacePath(workspace, domain.RepositoryContext{WorkspaceDir: artifact.RepositoryDir})
			summary := repositoryPatchSummary(artifact)
			if workspaceHasRepoWork(repoWorkspace) {
				fmt.Fprintf(&manifest, "## %s / repository-patch (%s)\n\nSkipped applying; this run already owns the workspace. Read the files with git status / git diff.\n\n%s\n\n", artifact.NodeID, artifact.RepositoryDir, summary)
				continue
			}
			if body := repositoryPatchApplyBody(artifact); looksLikeGitDiff(body) {
				if err := applyRepositoryPatch(repoWorkspace, body); err != nil {
					return "", fmt.Errorf("apply repository-patch from %s (%s): %w", artifact.NodeID, artifact.RepositoryDir, err)
				}
				fmt.Fprintf(&manifest, "## %s / repository-patch (%s)\n\nApplied a legacy unified diff (sha %s).\n\n%s\n\n", artifact.NodeID, artifact.RepositoryDir, artifact.SHA256, summary)
				continue
			}
			fmt.Fprintf(&manifest, "## %s / repository-patch (%s)\n\nNo full diff is stored. The run workspace is the source of truth.\n\n%s\n\n", artifact.NodeID, artifact.RepositoryDir, summary)
			continue
		}
		nodeSegment := safePathSegment(artifact.NodeID)
		controlSegments := []string{"inputs", nodeSegment}
		if artifact.RepositoryDir != "" {
			controlSegments = append(controlSegments, safePathSegment(artifact.RepositoryDir))
		}
		inputDir, err := runtime.EnsureControlDirectory(workspace, controlSegments...)
		if err != nil {
			return "", err
		}
		fileName := safePathSegment(artifact.Name) + extensionFor(artifact.MediaType)
		rel := artifactInputRelPath(artifact)
		target := filepath.Join(inputDir, fileName)
		if info, err := os.Lstat(target); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return "", fmt.Errorf("artifact input path is not a regular file: %s", target)
			}
			if err := os.Chmod(target, 0o600); err != nil {
				return "", err
			}
		} else if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err := os.WriteFile(target, []byte(artifact.Content), 0o600); err != nil {
			return "", err
		}
		if err := os.Chmod(target, 0o444); err != nil {
			return "", err
		}
		writeInputManifestEntry(&manifest, artifact, rel)
	}
	handoffManifest, err := materializeSOPHandoffInputs(workspace, run)
	if err != nil {
		return "", err
	}
	manifest.WriteString(handoffManifest)
	return strings.TrimSpace(manifest.String()), nil
}

// artifactInputRelPath is where an upstream artifact is mounted for the
// consuming node, relative to the run workspace.
func artifactInputRelPath(artifact domain.Artifact) string {
	segments := []string{".agentflow", "inputs", safePathSegment(artifact.NodeID)}
	if artifact.RepositoryDir != "" {
		segments = append(segments, safePathSegment(artifact.RepositoryDir))
	}
	segments = append(segments, safePathSegment(artifact.Name)+extensionFor(artifact.MediaType))
	return strings.Join(segments, "/")
}

func writeInputManifestEntry(manifest *strings.Builder, artifact domain.Artifact, rel string) {
	fmt.Fprintf(manifest, "## %s / %s\n\n- path: `%s`\n- sha256: `%s`\n- bytes: %d\n\nRead this file; the body is not inlined in the prompt.\n\n", artifact.NodeID, artifact.Name, rel, artifact.SHA256, len(artifact.Content))
}

// recoverMisplacedArtifacts copies required deliverables into the sandbox when
// the worker wrote them to the host checkout (localPath) or a nested path.
func recoverMisplacedArtifacts(workspace, hostLocalPath string, node domain.WorkflowNode) []string {
	// Repository-scoped outputs intentionally live below each repository root.
	// Treating those paths as misplaced would copy the first matching artifact
	// into the Run root and collapse distinct outputs from multiple repositories.
	if normalizeExecutionScope(node.ExecutionScope) == domain.ExecutionScopeRepositories {
		return nil
	}
	var recovered []string
	for _, contract := range node.ArtifactOutputs {
		clean := filepath.Clean(contract.Path)
		if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			continue
		}
		dest := filepath.Join(workspace, clean)
		if _, err := os.Stat(dest); err == nil {
			continue
		}
		base := filepath.Base(clean)
		candidates := []string{findNamedFile(workspace, base, 4)}
		if host := strings.TrimSpace(hostLocalPath); host != "" {
			candidates = append(candidates, filepath.Join(host, clean), filepath.Join(host, base), findNamedFile(host, base, 2))
		}
		for _, src := range candidates {
			if src == "" || src == dest {
				continue
			}
			info, err := os.Stat(src)
			if err != nil || info.IsDir() || info.Size() > maxArtifactBytes {
				continue
			}
			data, err := os.ReadFile(src)
			if err != nil {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				continue
			}
			if err := os.WriteFile(dest, data, 0o644); err != nil {
				continue
			}
			recovered = append(recovered, contract.Path+" ← "+src)
			removeUntrackedHostLeak(hostLocalPath, src)
			break
		}
	}
	return recovered
}

func findNamedFile(root, name string, maxDepth int) string {
	if strings.TrimSpace(root) == "" || name == "" {
		return ""
	}
	var found string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || found != "" {
			return err
		}
		if d.IsDir() {
			base := d.Name()
			if base == ".git" || base == "node_modules" || base == ".agentflow" {
				if path != root {
					return filepath.SkipDir
				}
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr == nil && rel != "." && strings.Count(rel, string(os.PathSeparator))+1 >= maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == name {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func removeUntrackedHostLeak(host, path string) {
	host = strings.TrimSpace(host)
	if host == "" || path == "" {
		return
	}
	rel, err := filepath.Rel(host, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return
	}
	cmd := exec.Command("git", "-C", host, "ls-files", "--error-unmatch", rel)
	if cmd.Run() != nil {
		_ = os.Remove(path)
	}
}

func (e *Engine) freezeAvailableArtifacts(runID string, node domain.WorkflowNode, workspace string, reported *reportedOutputs, output string) {
	current, ok := e.repo.RunState(runID)
	if !ok {
		return
	}
	var outputs nodeOutputs = workspaceOutputs(workspace)
	recovered := []string(nil)
	if reported != nil {
		outputs = reported
		recovered = reported.takeRecovered()
	} else {
		recovered = recoverMisplacedArtifacts(workspace, current.Repository.LocalPath, node)
	}
	if len(recovered) > 0 {
		e.observe(runID, node.ID, domain.TelemetryEvent{Category: "artifact", Name: "artifact.recovered", Phase: "progress", Message: "失败路径收回交付物：" + strings.Join(recovered, "；")})
	}
	artifacts, err := collectArtifactsMode(outputs, current, node, output, true)
	if err != nil || len(artifacts) == 0 {
		return
	}
	e.updateRun(runID, func(r *domain.Run) {
		r.Artifacts = append(r.Artifacts, artifacts...)
	})
	for _, artifact := range artifacts {
		e.observe(runID, node.ID, domain.TelemetryEvent{Category: "artifact", Name: "artifact.published", Phase: "finish", Message: artifact.Name, Attributes: map[string]any{"artifactId": artifact.ID, "path": artifact.Path, "mediaType": artifact.MediaType, "size": artifact.Size, "sha256": artifact.SHA256}})
	}
}

// validateArtifactContracts is deliberately run before verification gates.
// Gates may inspect declared files, but they must never be used to manufacture
// a missing deliverable that the Agent was required to produce.
func validateArtifactContracts(workspace string, run domain.Run, node domain.WorkflowNode) error {
	return validateArtifactOutputs(workspaceOutputs(workspace), run, node)
}

func validateArtifactOutputs(outputs nodeOutputs, run domain.Run, node domain.WorkflowNode) error {
	contracts := node.ArtifactOutputs
	contractTargets := []domain.RepositoryContext{{}}
	if normalizeExecutionScope(node.ExecutionScope) == domain.ExecutionScopeRepositories {
		contractTargets = run.RepositoryContexts()
		if len(contractTargets) == 0 && len(contracts) > 0 {
			return errors.New("逐仓库产物需要 Run 至少绑定一个代码仓库")
		}
	}
	for _, rc := range contractTargets {
		for _, contract := range contracts {
			rel, ok := artifactRelPath(rc.WorkspaceDir, contract.Path)
			if contract.Name == "" || !ok {
				return fmt.Errorf("invalid artifact %q path %q", contract.Name, contract.Path)
			}
			if _, err := outputs.readFile(rel); err != nil {
				switch {
				case errors.Is(err, fs.ErrNotExist) && !contract.Required:
					continue
				case errors.Is(err, errOutputNotRegular):
					return fmt.Errorf("%s (%s) is not a regular file", contract.Name, rc.WorkspaceDir)
				case errors.Is(err, errOutputTooLarge):
					return fmt.Errorf("%s exceeds %d bytes", contract.Name, maxArtifactBytes)
				}
				return fmt.Errorf("%s (%s): %w", contract.Name, rc.WorkspaceDir, err)
			}
		}
	}
	return nil
}

func collectArtifacts(workspace string, run domain.Run, node domain.WorkflowNode, output string) ([]domain.Artifact, error) {
	return collectArtifactsMode(workspaceOutputs(workspace), run, node, output, false)
}

func collectArtifactsBestEffort(workspace string, run domain.Run, node domain.WorkflowNode, output string) ([]domain.Artifact, error) {
	return collectArtifactsMode(workspaceOutputs(workspace), run, node, output, true)
}

func collectArtifactsMode(outputs nodeOutputs, run domain.Run, node domain.WorkflowNode, output string, bestEffort bool) ([]domain.Artifact, error) {
	attempt := 0
	if nr := run.Nodes[node.ID]; nr != nil {
		attempt = nr.Attempt
	}
	contracts := node.ArtifactOutputs
	out := make([]domain.Artifact, 0, len(contracts)+2)
	if len(contracts) == 0 && !bestEffort {
		out = append(out, newArtifact(run.ID, node.ID, attempt, "result", "result.md", "text/markdown", []byte(output)))
	}
	contractTargets := []domain.RepositoryContext{{}}
	if normalizeExecutionScope(node.ExecutionScope) == domain.ExecutionScopeRepositories {
		contractTargets = run.RepositoryContexts()
		if len(contractTargets) == 0 && len(contracts) > 0 && !bestEffort {
			return nil, errors.New("逐仓库产物需要 Run 至少绑定一个代码仓库")
		}
	}
	for _, rc := range contractTargets {
		for _, contract := range contracts {
			rel, ok := artifactRelPath(rc.WorkspaceDir, contract.Path)
			if contract.Name == "" || !ok {
				if bestEffort {
					continue
				}
				return nil, fmt.Errorf("invalid artifact %q path %q", contract.Name, contract.Path)
			}
			clean := filepath.Clean(contract.Path)
			content, err := outputs.readFile(rel)
			if err != nil {
				if bestEffort || errors.Is(err, fs.ErrNotExist) && !contract.Required {
					continue
				}
				if errors.Is(err, errOutputTooLarge) {
					return nil, fmt.Errorf("%s exceeds %d bytes", contract.Name, maxArtifactBytes)
				}
				return nil, fmt.Errorf("%s (%s): %w", contract.Name, rc.WorkspaceDir, err)
			}
			if len(content) > maxArtifactBytes {
				if bestEffort {
					continue
				}
				return nil, fmt.Errorf("%s exceeds %d bytes", contract.Name, maxArtifactBytes)
			}
			mediaType := contract.MediaType
			if mediaType == "" {
				mediaType = "text/markdown"
			}
			artifact := newArtifact(run.ID, node.ID, attempt, contract.Name, clean, mediaType, content)
			artifact.RepositoryDir = rc.WorkspaceDir
			out = append(out, artifact)
		}
	}
	patchTargets := run.RepositoryContexts()
	if len(patchTargets) == 0 {
		// Runs without any repository context still collect a workspace-root
		// patch so handoff between nodes keeps working.
		patchTargets = []domain.RepositoryContext{{}}
	}
	for _, rc := range patchTargets {
		patch, err := outputs.repositoryPatch(rc.WorkspaceDir)
		if err != nil {
			if !bestEffort {
				return nil, err
			}
			continue
		}
		if len(patch) == 0 {
			continue
		}
		name := "repository.patch"
		if rc.WorkspaceDir != "" {
			name = fmt.Sprintf("repository-%s.patch", rc.WorkspaceDir)
		}
		artifact := newArtifact(run.ID, node.ID, attempt, "repository-patch", name, "text/plain", patch)
		artifact.RepositoryDir = rc.WorkspaceDir
		out = append(out, artifact)
	}
	if len(out) == 0 {
		if bestEffort {
			return nil, nil
		}
		out = append(out, newArtifact(run.ID, node.ID, attempt, "result", "result.md", "text/markdown", []byte(output)))
	}
	return out, nil
}

func latestInputArtifacts(artifacts []domain.Artifact, direct, prior map[string]bool) []domain.Artifact {
	// Dedup by (node, repository, name, path): multi-repository runs emit one
	// repository-patch artifact per repository sharing the same logical name
	// but distinct paths; all of them must survive.
	type key struct{ node, repository, name, path string }
	best := map[key]domain.Artifact{}
	for _, artifact := range artifacts {
		if !includeArtifact(direct, prior, artifact) {
			continue
		}
		k := key{artifact.NodeID, artifact.RepositoryDir, artifact.Name, artifact.Path}
		prev, ok := best[k]
		if !ok || artifact.Attempt > prev.Attempt || artifact.Attempt == prev.Attempt && artifact.CreatedAt.After(prev.CreatedAt) {
			best[k] = artifact
		}
	}
	out := make([]domain.Artifact, 0, len(best))
	for _, artifact := range best {
		out = append(out, artifact)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].NodeID != out[j].NodeID {
			return out[i].NodeID < out[j].NodeID
		}
		if out[i].RepositoryDir != out[j].RepositoryDir {
			return out[i].RepositoryDir < out[j].RepositoryDir
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func includeArtifact(direct, prior map[string]bool, artifact domain.Artifact) bool {
	if direct[artifact.NodeID] {
		return true
	}
	return prior[artifact.NodeID] && artifact.Name != "repository-patch"
}

func artifactPromptBody(artifact domain.Artifact) string {
	if artifact.Name == "repository-patch" {
		return repositoryPatchSummary(artifact)
	}
	return artifact.Content
}

func newArtifact(runID, nodeID string, attempt int, name, path, mediaType string, content []byte) domain.Artifact {
	sum := sha256.Sum256(content)
	now := time.Now().UTC()
	return domain.Artifact{ID: fmt.Sprintf("artifact-%d", now.UnixNano()), RunID: runID, NodeID: nodeID, Attempt: attempt, Name: name, Path: path, MediaType: mediaType, Size: int64(len(content)), SHA256: fmt.Sprintf("%x", sum), Content: string(content), CreatedAt: now}
}

func appendArtifact(run *domain.Run, nodeID string, attempt int, name, path, mediaType string, content []byte) {
	run.Artifacts = append(run.Artifacts, newArtifact(run.ID, nodeID, attempt, name, path, mediaType, content))
}
