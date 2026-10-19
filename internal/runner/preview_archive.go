package runner

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/preview"
)

func (d *Daemon) executePreviewArchive(ctx context.Context, task domain.RunnerTask) execResult {
	if err := d.client.StartTask(ctx, task.ID); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("start preview archive: %v", err)}
	}
	spec := task.PreviewArchive
	if spec == nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: "missing preview archive spec"}
	}
	root, err := filepath.Abs(d.cfg.DefaultWorkspaceRoot)
	if err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("resolve workspace root: %v", err)}
	}
	target, err := filepath.Abs(d.taskWorkspace(task))
	if err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("resolve preview workspace: %v", err)}
	}
	rel, err := filepath.Rel(root, target)
	runRoot := filepath.Join("runs", task.RunID)
	shared := filepath.Join(runRoot, "workspace")
	if err != nil || task.RunID == "" || (filepath.Clean(rel) != runRoot && filepath.Clean(rel) != shared) || strings.HasPrefix(rel, "..") {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("refusing unsafe preview workspace %q", target)}
	}
	limits := preview.Limits{MaxFiles: spec.MaxFiles, MaxCompressed: spec.MaxBytes}
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		_, archiveErr := preview.ArchiveDir(target, spec.SourcePath, spec.IndexFile, limits, pw)
		_ = pw.CloseWithError(archiveErr)
		errCh <- archiveErr
	}()
	uploadErr := d.client.UploadPreview(ctx, spec.PreviewID, spec.UploadToken, pr)
	archiveErr := <-errCh
	if archiveErr != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: archiveErr.Error()}
	}
	if uploadErr != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: uploadErr.Error()}
	}
	_ = d.client.CompletePreview(ctx, spec.PreviewID)
	_ = d.client.SendEvents(ctx, task.ID, []domain.RunnerTaskEvent{{Seq: 1, Type: "system", Data: "preview archive uploaded", Ts: time.Now().UTC()}})
	return execResult{status: domain.RunnerTaskSuccess, exitCode: 0, result: "preview archived"}
}
