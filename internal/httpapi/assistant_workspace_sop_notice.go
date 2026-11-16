package httpapi

import (
	"fmt"

	"agentflow/internal/domain"
)

// sopStopped mirrors the repo's workspace write rule for SOP runs.
func sopStopped(run domain.Run) bool {
	if domain.TerminalRunStatus(run.Status) {
		return true
	}
	if run.Status != "waiting" || !run.PauseRequested || run.CancelRequested {
		return false
	}
	for _, node := range run.Nodes {
		if node != nil && node.Status == "running" {
			return false
		}
	}
	return true
}

// borrowedSOPNotice explains why a conversation bound to an SOP workspace can
// or cannot write, and how to take the run over. Main Bot controls the run with
// the platform run tools; there is no separate SOP tool.
func borrowedSOPNotice(run domain.Run) string {
	switch {
	case domain.TerminalRunStatus(run.Status):
		return fmt.Sprintf("SOP 运行 %s 已结束（%s）：可以直接改文件、跑命令，已回收的工作区会先恢复快照；改完自己 git push 并开 PR，平台在本轮结束时把它登记到该 SOP 的需求。", run.ID, run.Status)
	case sopStopped(run):
		return fmt.Sprintf("SOP 运行 %s 已暂停：可以直接改文件、跑命令。改完调用 runs_resume 让 SOP 带着改动继续并由它交付，或 runs_cancel 取消后自己提交。", run.ID)
	case run.CancelRequested:
		return fmt.Sprintf("SOP 运行 %s 正在取消，当前节点停下后就可以改文件，稍后再试。", run.ID)
	case run.PauseRequested:
		return fmt.Sprintf("SOP 运行 %s 正在暂停，当前节点停下后就可以改文件，稍后再试。", run.ID)
	default:
		return fmt.Sprintf("SOP 运行 %s（%s）执行中，工作区只能读取。用户要改代码时先 runs_pause 暂停后改、改完 runs_resume；或 runs_cancel 取消后接管自己提交。", run.ID, run.Status)
	}
}
