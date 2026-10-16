package engine

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"agentflow/internal/domain"
)

var (
	errOutputNotRegular = errors.New("is not a regular file")
	errOutputTooLarge   = errors.New("exceeds artifact size limit")
)

// nodeOutputs is where the engine reads what a node produced: the
// control-plane sandbox workspace, or what the execution machine reported.
// Paths are workspace-relative and slash-separated.
type nodeOutputs interface {
	readFile(rel string) ([]byte, error)
	repositoryPatch(dir string) ([]byte, error)
}

type workspaceOutputs string

func (w workspaceOutputs) readFile(rel string) ([]byte, error) {
	full := filepath.Join(string(w), filepath.FromSlash(rel))
	info, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errOutputNotRegular
	}
	if info.Size() > maxArtifactBytes {
		return nil, errOutputTooLarge
	}
	return os.ReadFile(full)
}

func (w workspaceOutputs) repositoryPatch(dir string) ([]byte, error) {
	return collectRepositoryPatch(repositoryWorkspacePath(string(w), domain.RepositoryContext{WorkspaceDir: dir}))
}

// reportedOutputs holds what the execution machine reported for the current
// invocation of one node. It replaces the control-plane copy of the
// workspace for artifact validation, registration, and the gate result.
type reportedOutputs struct {
	inputs []domain.RunnerInputFile

	mu        sync.Mutex
	files     map[string]string
	tooLarge  map[string]bool
	patches   map[string]string
	recovered []string
}

func newReportedOutputs(inputs []domain.RunnerInputFile) *reportedOutputs {
	r := &reportedOutputs{inputs: inputs}
	r.reset()
	return r
}

// reset forgets the previous invocation; the runner reports the full current
// state of the workspace every time.
func (r *reportedOutputs) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files = map[string]string{}
	r.tooLarge = map[string]bool{}
	r.patches = map[string]string{}
	r.recovered = nil
}

// absorb merges one task report. Script nodes dispatch one task per
// repository target, so reports of the same invocation accumulate.
func (r *reportedOutputs) absorb(outputs *domain.RunnerTaskOutputs) {
	if outputs == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for rel, content := range outputs.Files {
		r.files[rel] = content
		delete(r.tooLarge, rel)
	}
	for _, rel := range outputs.TooLarge {
		r.tooLarge[rel] = true
		delete(r.files, rel)
	}
	for dir, patch := range outputs.Patches {
		r.patches[dir] = patch
	}
	r.recovered = append(r.recovered, outputs.Recovered...)
}

func (r *reportedOutputs) takeRecovered() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.recovered
	r.recovered = nil
	return out
}

func (r *reportedOutputs) readFile(rel string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tooLarge[rel] {
		return nil, errOutputTooLarge
	}
	content, ok := r.files[rel]
	if !ok {
		return nil, &fs.PathError{Op: "read", Path: rel, Err: fs.ErrNotExist}
	}
	return []byte(content), nil
}

func (r *reportedOutputs) repositoryPatch(dir string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if patch := r.patches[dir]; patch != "" {
		return []byte(patch), nil
	}
	return nil, nil
}

// artifactRelPath is the workspace-relative path of a contract output for
// one repository target ("" is the workspace root). ok is false for paths
// that escape the target.
func artifactRelPath(repositoryDir, contractPath string) (string, bool) {
	clean := path.Clean(filepath.ToSlash(strings.TrimSpace(contractPath)))
	if clean == "." || clean == ".." || path.IsAbs(clean) || strings.HasPrefix(clean, "../") {
		return "", false
	}
	if dir := strings.Trim(filepath.ToSlash(strings.TrimSpace(repositoryDir)), "/"); dir != "" {
		return path.Join(dir, clean), true
	}
	return clean, true
}

// artifactOutputFiles lists every workspace-relative path the node's
// contracts resolve to, using the same targets as artifact validation.
func artifactOutputFiles(run domain.Run, node domain.WorkflowNode) []string {
	targets := []domain.RepositoryContext{{}}
	if normalizeExecutionScope(node.ExecutionScope) == domain.ExecutionScopeRepositories {
		targets = run.RepositoryContexts()
	}
	var out []string
	for _, rc := range targets {
		for _, contract := range node.ArtifactOutputs {
			if rel, ok := artifactRelPath(rc.WorkspaceDir, contract.Path); ok {
				out = append(out, rel)
			}
		}
	}
	return out
}

// runnerArtifactInputs is materializeInputs for an execution machine: the
// same selection and prompt manifest, returned as files for the runner to
// write instead of being written into a control-plane workspace. Repository
// patches are never applied; the runner's checkout is the source of truth.
func runnerArtifactInputs(wf domain.WorkflowDefinition, node domain.WorkflowNode, run domain.Run) (string, []domain.RunnerInputFile) {
	sources := map[string]bool{}
	for _, edge := range wf.Edges {
		if edge.Target == node.ID {
			sources[edge.Source] = true
		}
	}
	var manifest strings.Builder
	var files []domain.RunnerInputFile
	for _, artifact := range latestInputArtifacts(run.Artifacts, sources, ancestors(wf, node.ID)) {
		if artifact.Name == "repository-patch" {
			if !sources[artifact.NodeID] {
				continue
			}
			manifest.WriteString("## " + artifact.NodeID + " / repository-patch (" + artifact.RepositoryDir + ")\n\nNo full diff is stored. The run workspace is the source of truth.\n\n" + repositoryPatchSummary(artifact) + "\n\n")
			continue
		}
		rel := artifactInputRelPath(artifact)
		files = append(files, domain.RunnerInputFile{Path: rel, Content: artifact.Content})
		writeInputManifestEntry(&manifest, artifact, rel)
	}
	handoffManifest, handoffFiles := sopHandoffInputs(run)
	manifest.WriteString(handoffManifest)
	files = append(files, handoffFiles...)
	return strings.TrimSpace(manifest.String()), files
}
