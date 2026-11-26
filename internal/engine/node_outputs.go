package engine

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"agentflow/internal/domain"
)

var (
	errOutputNotRegular     = errors.New("is not a regular file")
	errOutputTooLarge       = fmt.Errorf("超过产物大小上限 %d MiB", domain.MaxSOPArtifactBytes>>20)
	errLegacyOutputTooLarge = fmt.Errorf("超过旧版执行机器的 %d MiB 单文件回传限制，请将执行机器升级到 %s", domain.MaxInlineRunnerArtifactBytes>>20, domain.RunnerVersionArtifactTransfer)
	errArtifactTransfer     = errors.New("产物交接失败")
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
	file, _, err := w.openFile(rel)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readWorkspaceOutput(file)
}

func (w workspaceOutputs) openFile(rel string) (*os.File, int64, error) {
	root, err := os.OpenRoot(string(w))
	if err != nil {
		return nil, 0, err
	}
	defer root.Close()
	info, err := root.Stat(rel)
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, errOutputNotRegular
	}
	if info.Size() > domain.MaxSOPArtifactBytes {
		return nil, 0, errOutputTooLarge
	}
	file, err := root.Open(rel)
	return file, info.Size(), err
}

func readWorkspaceOutput(file io.Reader) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(file, domain.MaxSOPArtifactBytes+1))
	if len(content) > domain.MaxSOPArtifactBytes {
		return nil, errOutputTooLarge
	}
	return content, err
}

type archivedWorkspaceOutputs struct {
	workspaceOutputs
	save func(string, io.Reader) (domain.ArtifactBlob, error)
}

func (w archivedWorkspaceOutputs) archiveFile(rel, id string) ([]byte, domain.ArtifactBlob, error) {
	file, size, err := w.openFile(rel)
	if err != nil {
		return nil, domain.ArtifactBlob{}, err
	}
	defer file.Close()
	if size <= domain.MaxInlineRunnerArtifactBytes {
		content, err := readWorkspaceOutput(file)
		return content, domain.ArtifactBlob{}, err
	}
	blob, err := w.save(id, file)
	if errors.Is(err, domain.ErrArtifactTooLarge) {
		return nil, blob, errOutputTooLarge
	}
	if err != nil {
		return nil, blob, fmt.Errorf("%w：%w", errArtifactTransfer, err)
	}
	return nil, blob, nil
}

func checkArtifactOutput(outputs nodeOutputs, rel string) error {
	if _, ok := reportedArtifactBlob(outputs, rel); ok {
		return nil
	}
	if local, ok := outputs.(archivedWorkspaceOutputs); ok {
		file, _, err := local.openFile(rel)
		if err != nil {
			return err
		}
		return file.Close()
	}
	_, err := outputs.readFile(rel)
	return err
}

func collectOutputFile(outputs nodeOutputs, rel, id string) ([]byte, domain.ArtifactBlob, error) {
	if local, ok := outputs.(archivedWorkspaceOutputs); ok {
		return local.archiveFile(rel, id)
	}
	// Preserve legacy readers of small inline metadata such as MR information.
	if blob, ok := reportedArtifactBlob(outputs, rel); ok && blob.Size > domain.MaxInlineRunnerArtifactBytes {
		return nil, blob, nil
	}
	content, err := outputs.readFile(rel)
	return content, domain.ArtifactBlob{}, err
}

func (w workspaceOutputs) repositoryPatch(dir string) ([]byte, error) {
	return collectRepositoryPatch(repositoryWorkspacePath(string(w), domain.RepositoryContext{WorkspaceDir: dir}))
}

// reportedOutputs holds what the execution machine reported for the current
// invocation of one node. It replaces the control-plane copy of the
// workspace for artifact validation, registration, and the gate result.
type reportedOutputs struct {
	inputs   []domain.RunnerInputFile
	transfer bool
	openBlob func(string) (io.ReadCloser, error)

	mu        sync.Mutex
	files     map[string]string
	blobs     map[string]domain.ArtifactBlob
	errors    map[string]string
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
	r.blobs = map[string]domain.ArtifactBlob{}
	r.errors = map[string]string{}
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
		r.clearFile(rel)
		r.files[rel] = content
	}
	for rel, blob := range outputs.FileBlobs {
		r.clearFile(rel)
		r.blobs[rel] = blob
	}
	for rel, message := range outputs.FileErrors {
		r.clearFile(rel)
		r.errors[rel] = message
	}
	for _, rel := range outputs.TooLarge {
		r.clearFile(rel)
		r.tooLarge[rel] = true
	}
	for dir, patch := range outputs.Patches {
		r.patches[dir] = patch
	}
	r.recovered = append(r.recovered, outputs.Recovered...)
}

func (r *reportedOutputs) clearFile(rel string) {
	delete(r.files, rel)
	delete(r.blobs, rel)
	delete(r.errors, rel)
	delete(r.tooLarge, rel)
}

func (r *reportedOutputs) artifactBlob(rel string) (domain.ArtifactBlob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	blob, ok := r.blobs[rel]
	return blob, ok
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
	tooLarge, message := r.tooLarge[rel], r.errors[rel]
	content, inline := r.files[rel]
	blob, staged := r.blobs[rel]
	r.mu.Unlock()
	switch {
	case tooLarge && !r.transfer:
		return nil, errLegacyOutputTooLarge
	case tooLarge:
		return nil, errOutputTooLarge
	case message != "":
		return nil, fmt.Errorf("%w：%s", errArtifactTransfer, message)
	case inline:
		return []byte(content), nil
	case staged && r.openBlob != nil:
		body, err := r.openBlob(blob.ID)
		if err != nil {
			return nil, fmt.Errorf("%w：%w", errArtifactTransfer, err)
		}
		defer body.Close()
		content, err := io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("%w：%w", errArtifactTransfer, err)
		}
		return content, nil
	default:
		return nil, &fs.PathError{Op: "read", Path: rel, Err: fs.ErrNotExist}
	}
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
func runnerArtifactInputs(wf domain.WorkflowDefinition, node domain.WorkflowNode, run domain.Run, stream ...bool) (string, []domain.RunnerInputFile) {
	transfer := len(stream) != 0 && stream[0]
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
		rel := artifactInputRelPath(run, artifact)
		files = append(files, runnerInputFile(artifact, rel, transfer))
		writeInputManifestEntry(&manifest, artifact, rel)
	}
	handoffManifest, handoffFiles := sopHandoffInputs(run, transfer)
	manifest.WriteString(handoffManifest)
	files = append(files, handoffFiles...)
	return strings.TrimSpace(manifest.String()), files
}

func runnerInputFile(artifact domain.Artifact, rel string, stream bool) domain.RunnerInputFile {
	if stream {
		return domain.RunnerInputFile{Path: rel, ArtifactID: artifact.ID, SHA256: artifact.SHA256}
	}
	return domain.RunnerInputFile{Path: rel, Content: artifact.Content}
}
