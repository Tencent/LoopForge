package runner

import (
	"context"
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
	"agentflow/internal/gitwork"
	"agentflow/internal/runtime"
)

// prepareInvocationWorkspace writes the upstream inputs the control plane
// resolved for this node and removes a gate result left by an earlier
// invocation, so the engine never reads a stale terminal decision.
func prepareInvocationWorkspace(workspace string, inv *domain.RunnerInvocation, fetch ...func(domain.RunnerInputFile) error) error {
	if inv == nil {
		return nil
	}
	// Parallel branches consuming the same upstream artifact write the same
	// input file; chmod/write/chmod must not interleave.
	unlock := lockInvocationWorkspace(workspace)
	defer unlock()
	if _, err := runtime.RestoreMissingWorkspaceArtifacts(workspace, inv.ArchivedFiles); err != nil {
		return fmt.Errorf("restore archived outputs: %w", err)
	}
	if rel := strings.TrimSpace(inv.GateResultPath); rel != "" {
		if !domain.SafeWorkspaceRelPath(rel) {
			return fmt.Errorf("unsafe gate result path %q", rel)
		}
		if err := os.RemoveAll(filepath.Join(workspace, filepath.FromSlash(rel))); err != nil {
			return fmt.Errorf("clear gate result: %w", err)
		}
	}
	for _, input := range inv.Inputs {
		if input.ArtifactID != "" {
			if len(fetch) == 0 {
				return fmt.Errorf("no file transfer available for input %s", input.Path)
			}
			if err := fetch[0](input); err != nil {
				return fmt.Errorf("download input %s: %w", input.Path, err)
			}
			continue
		}
		if err := writeInputFile(workspace, input); err != nil {
			return err
		}
	}
	return nil
}

func writeInputFile(workspace string, input domain.RunnerInputFile) error {
	return runtime.WriteArtifactInput(workspace, input.Path, "", strings.NewReader(input.Content))
}

func (d *Daemon) prepareTaskInvocation(ctx context.Context, workspace string, task domain.RunnerTask) error {
	return prepareInvocationWorkspace(workspace, task.Invocation, func(input domain.RunnerInputFile) error {
		return retryTaskTransfer(ctx, func() error {
			if input.Restore {
				_, err := runtime.RestoreMissingWorkspaceArtifact(workspace, input.Path, input.SHA256, func() (io.ReadCloser, error) {
					return d.client.openTaskInput(ctx, task.ID, input.Path)
				})
				return err
			}
			body, err := d.client.openTaskInput(ctx, task.ID, input.Path)
			if err != nil {
				return err
			}
			defer body.Close()
			return runtime.WriteArtifactInput(workspace, input.Path, input.SHA256, body)
		})
	})
}

var invocationWorkspaceLocks sync.Map // cleaned absolute workspace -> *sync.Mutex

func lockInvocationWorkspace(workspace string) func() {
	key := filepath.Clean(workspace)
	if abs, err := filepath.Abs(key); err == nil {
		key = abs
	}
	value, _ := invocationWorkspaceLocks.LoadOrStore(key, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// collectTaskOutputs reports the requested output files, the
// repository-patch inventory of every checkout, and workspace facts. It runs
// after success and failure alike so a failed node keeps its diagnostics.
func collectTaskOutputs(workspace string, inv *domain.RunnerInvocation) *domain.RunnerTaskOutputs {
	if inv == nil || !inv.ReportOutputs {
		return nil
	}
	out := &domain.RunnerTaskOutputs{}
	if inv.RecoverOutputs {
		limit := int64(maxArtifactUploadBytes)
		if inv.ArtifactTransfer {
			limit = domain.MaxSOPArtifactBytes
		}
		out.Recovered = recoverOutputFiles(workspace, inv.OutputFiles, limit)
	}
	paths := inv.OutputFiles
	if inv.ArtifactTransfer {
		// Gate control JSON stays in the terminal report.
		paths = nil
		if inv.GateResultPath != "" {
			paths = []string{inv.GateResultPath}
		}
	}
	out.Files, out.TooLarge = readOutputFiles(workspace, paths)
	out.Patches = collectPatchInventories(workspace, patchInventoryDirs(inv))
	frontend, evidence := domain.DetectFrontendProject(workspace)
	out.Facts = &domain.RunnerWorkspaceFacts{Frontend: frontend, FrontendEvidence: evidence}
	return out
}

func (d *Daemon) uploadTaskOutputs(ctx context.Context, task domain.RunnerTask, out *domain.RunnerTaskOutputs) {
	inv := task.Invocation
	if inv == nil || !inv.ArtifactTransfer || out == nil {
		return
	}
	out.FileBlobs = make(map[string]domain.ArtifactBlob)
	out.FileErrors = make(map[string]string)
	root, rootErr := os.OpenRoot(d.taskWorkspace(task))
	if rootErr == nil {
		defer root.Close()
	}
	for _, rel := range inv.OutputFiles {
		if rel == inv.GateResultPath || !domain.SafeWorkspaceRelPath(rel) {
			continue
		}
		err := rootErr
		if err == nil {
			outBlob, uploadErr := d.uploadOutputFile(ctx, task.ID, root, rel)
			err = uploadErr
			if err == nil {
				out.FileBlobs[rel] = outBlob
				continue
			}
		}
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case errors.Is(err, domain.ErrArtifactTooLarge):
			out.TooLarge = append(out.TooLarge, rel)
		default:
			out.FileErrors[rel] = fmt.Sprintf("%.512s", err.Error())
		}
	}
}

func (d *Daemon) uploadOutputFile(ctx context.Context, taskID string, root *os.Root, rel string) (domain.ArtifactBlob, error) {
	var blob domain.ArtifactBlob
	info, err := root.Lstat(rel)
	if err != nil {
		return blob, err
	}
	if !info.Mode().IsRegular() {
		return blob, errors.New("output is not a regular file")
	}
	file, err := root.Open(rel)
	if err != nil {
		return blob, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return blob, err
	}
	if !info.Mode().IsRegular() {
		return blob, errors.New("output is not a regular file")
	}
	if info.Size() > domain.MaxSOPArtifactBytes {
		return blob, domain.ErrArtifactTooLarge
	}
	err = retryTaskTransfer(ctx, func() error {
		var err error
		blob, err = d.client.uploadTaskOutput(ctx, taskID, rel, io.NewSectionReader(file, 0, info.Size()), info.Size())
		return err
	})
	return blob, err
}

// readOutputFiles reads every safe, regular output file within the upload
// limit. Oversized files are reported by path instead; both results stay nil
// when nothing qualifies.
func readOutputFiles(workspace string, rels []string) (files map[string]string, tooLarge []string) {
	for _, rel := range rels {
		if !domain.SafeWorkspaceRelPath(rel) {
			continue
		}
		full := filepath.Join(workspace, filepath.FromSlash(rel))
		info, err := os.Lstat(full)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if info.Size() > maxArtifactUploadBytes {
			tooLarge = append(tooLarge, rel)
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		if files == nil {
			files = map[string]string{}
		}
		files[rel] = string(data)
	}
	return files, tooLarge
}

// patchInventoryDirs lists the workspace-relative checkout directories whose
// patch inventory is reported: the workspace root without repositories,
// otherwise each repository's checkout directory.
func patchInventoryDirs(inv *domain.RunnerInvocation) []string {
	repositories := invocationRepositoryContexts(inv)
	if len(repositories) == 0 {
		return []string{""}
	}
	dirs := make([]string, 0, len(repositories))
	for _, repository := range repositories {
		dirs = append(dirs, filepath.ToSlash(safeRunnerRepoDir(repository.WorkspaceDir)))
	}
	return dirs
}

// collectPatchInventories maps each directory with a non-empty patch
// inventory to it; the map stays nil when no directory has changes.
func collectPatchInventories(workspace string, dirs []string) map[string]string {
	var patches map[string]string
	for _, dir := range dirs {
		patch, err := gitwork.PatchInventory(filepath.Join(workspace, filepath.FromSlash(dir)))
		if err != nil || len(patch) == 0 {
			continue
		}
		if patches == nil {
			patches = map[string]string{}
		}
		patches[dir] = string(patch)
	}
	return patches
}

// recoverOutputFiles moves a missing root-level output into place when the
// Agent wrote a file with the same name elsewhere in the workspace.
func recoverOutputFiles(workspace string, outputs []string, limit int64) []string {
	var recovered []string
	for _, rel := range outputs {
		if !domain.SafeWorkspaceRelPath(rel) {
			continue
		}
		dest := filepath.Join(workspace, filepath.FromSlash(rel))
		if _, err := os.Lstat(dest); err == nil {
			continue
		}
		src := findNamedFile(workspace, path.Base(rel), 4)
		if src == "" || src == dest {
			continue
		}
		info, err := os.Lstat(src)
		if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
			continue
		}
		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		if os.MkdirAll(filepath.Dir(dest), 0o755) != nil || os.WriteFile(dest, data, 0o644) != nil {
			continue
		}
		from, _ := filepath.Rel(workspace, src)
		recovered = append(recovered, rel+" ← "+filepath.ToSlash(from))
	}
	return recovered
}

func findNamedFile(root, name string, maxDepth int) string {
	var found string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return err
		}
		if d.IsDir() {
			if p == root {
				return nil
			}
			switch d.Name() {
			case ".git", "node_modules", ".agentflow":
				return filepath.SkipDir
			}
			rel, relErr := filepath.Rel(root, p)
			if relErr == nil && strings.Count(rel, string(os.PathSeparator))+1 >= maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == name && d.Type().IsRegular() {
			found = p
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
