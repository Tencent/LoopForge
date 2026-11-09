package httpapi

import (
	"testing"

	"agentflow/internal/domain"
)

// A conversation workspace has no nodes. Before issue #124 a failed clone or
// image build therefore left problems empty and the run looked merely idle.
func TestAssistantRunProblemsReportWorkspacePreparationFailures(t *testing.T) {
	workspace := func(events ...domain.RunEvent) domain.Run {
		return domain.Run{ID: "chat", Kind: domain.RunKindWorkspace, Status: "waiting", Nodes: map[string]*domain.NodeRun{}, Events: events}
	}
	ev := func(typ, msg string) domain.RunEvent { return domain.RunEvent{Type: typ, Message: msg} }

	cloneFailed := workspace(
		ev("workspace.clone.started", "正在克隆仓库 web"),
		ev("workspace.clone.progress", "克隆仓库 web：Receiving objects: 27%"),
		ev("workspace.clone.failed", "克隆仓库 web 失败（耗时 5m0s）：克隆仓库 web 已 5m0s 没有任何输出，判定卡住并停止"),
	)
	got := assistantRunProblems(cloneFailed, "", 300)
	if len(got) != 1 || got[0].Kind != "failed" || got[0].NodeName != "克隆仓库" || got[0].Reason != cloneFailed.Events[2].Message {
		t.Fatalf("clone failure = %+v", got)
	}

	// A retry that starts, or a later success, clears the earlier failure.
	retried := workspace(cloneFailed.Events...)
	retried.Events = append(retried.Events, ev("workspace.clone.started", "正在克隆仓库 web"))
	if got := assistantRunProblems(retried, "", 300); len(got) != 0 {
		t.Fatalf("retrying clone still reported: %+v", got)
	}
	recovered := workspace(append(retried.Events, ev("workspace.clone.completed", "克隆仓库 web 完成"))...)
	if got := assistantRunProblems(recovered, "", 300); len(got) != 0 {
		t.Fatalf("recovered clone still reported: %+v", got)
	}

	// Each stage is judged on its own; output follows the order they run in.
	both := workspace(
		ev("workspace.image.failed", "构建沙箱镜像 backend 失败：docker build: signal: killed"),
		ev("workspace.clone.failed", "克隆仓库 web 失败"),
		ev("workspace.preparation.record_failed", "不是失败阶段"),
	)
	got = assistantRunProblems(both, "", 300)
	if len(got) != 2 || got[0].NodeName != "克隆仓库" || got[1].NodeName != "构建沙箱镜像" {
		t.Fatalf("stage failures = %+v", got)
	}

	// SOP runs already report through their failed node; the same events must
	// not add a second copy.
	sop := both
	sop.Kind = ""
	if got := assistantRunProblems(sop, "", 300); len(got) != 0 {
		t.Fatalf("SOP run reported preparation events: %+v", got)
	}
}
