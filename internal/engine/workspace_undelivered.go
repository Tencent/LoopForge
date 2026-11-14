package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

const (
	undeliveredChangesEvent = "delivery.undelivered_changes"
	undeliveredFilesListed  = 20
)

// reportUndeliveredWork runs when a run succeeds, before its workspace is
// released. Nodes keep their changes in the shared checkout and only a
// delivery step commits and pushes them, so a run can succeed while the code
// it produced was never committed or pushed (#717). The run then looks
// delivered although nothing reached the repository; a run event names the
// affected files and points to the saved workspace snapshot. Workspaces on an
// execution machine or another instance are not readable here and are left
// unchecked.
func (e *Engine) reportUndeliveredWork(runID string) {
	run, ok := e.repo.RunState(runID)
	if !ok || run.Status != "succeeded" || e.sandboxes == nil || sharesWorkspace(run) {
		return
	}
	workspace, reason := e.LocalRunWorkspace(run)
	if workspace == "" || reason != "" {
		return
	}
	var messages []string
	for _, rc := range run.RepositoryContexts() {
		if rc.ReadOnly {
			continue
		}
		dir := repositoryWorkspacePath(workspace, rc)
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			continue
		}
		files, err := gitwork.UndeliveredChanges(dir)
		if err != nil {
			log.Printf("inspect undelivered work %s %s: %v", run.ID, dir, err)
			continue
		}
		if len(files) > 0 {
			messages = append(messages, undeliveredWorkMessage(rc, files))
		}
	}
	if len(messages) == 0 {
		return
	}
	e.updateRun(run.ID, func(r *domain.Run) {
		for _, msg := range messages {
			e.event(r, "", undeliveredChangesEvent, msg)
		}
	})
}

func undeliveredWorkMessage(rc domain.RepositoryContext, files []gitwork.PatchFileSummary) string {
	var added, deleted int
	listed := make([]string, 0, min(len(files), undeliveredFilesListed)+1)
	for i, f := range files {
		added += f.Added
		deleted += f.Deleted
		if i < undeliveredFilesListed {
			listed = append(listed, f.Status+" "+f.Path)
		}
	}
	if len(files) > undeliveredFilesListed {
		listed = append(listed, fmt.Sprintf("… 另有 %d 个文件", len(files)-undeliveredFilesListed))
	}
	label := strings.TrimSpace(rc.Name)
	if label == "" {
		label = strings.TrimSpace(rc.WorkspaceDir)
	}
	if label == "" {
		label = "工作区"
	}
	return fmt.Sprintf("交付警告: 仓库 %s 有 %d 个文件（+%d/-%d）的改动没有提交或没有推送到远端，本次运行没有交付这些代码。工作区清理前会保存快照，在运行详情中打开工作区即可恢复，再自行提交推送；需要平台自动推送时，请在交付节点或需求中明确要求创建分支/PR。改动文件: %s", label, len(files), added, deleted, strings.Join(listed, ", "))
}
