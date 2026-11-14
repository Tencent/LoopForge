package engine

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func loopRetroSignalRun() domain.Run {
	executor := domain.TelemetrySource{Layer: "executor"}
	return domain.Run{Telemetry: []domain.TelemetryEvent{
		{NodeID: "node", Category: "process", Name: "process.finished", Source: executor, Message: "ok"},
		{NodeID: "node", Category: "process", Name: "process.finished", Source: executor, Message: "执行失败"},
		{NodeID: "node", Name: "agent.loop", Message: "第 1 轮"},
		{NodeID: "node", Category: "tool", Name: "tool.call", Phase: "finish"},
		{NodeID: "node", Category: "tool", Name: "tool.call", Phase: "start"},
		{NodeID: "node", Category: "verification", Name: "process.finished", Level: "error"},
		{NodeID: "node", Name: "agent.completed"},
		{NodeID: "node", Category: "process", Name: "process.finished", Source: executor, Message: "ok"},
		{NodeID: "node", Category: "verification", Name: "process.finished", Level: "info"},
		{NodeID: "node", Name: "agent.loop", Message: "第 2 轮"},
		{NodeID: "other", Name: "agent.completed"},
		{NodeID: "other", Name: "agent.loop", Message: "别的节点"},
		{NodeID: "other", Category: "tool", Name: "tool.call", Phase: "finish"},
	}}
}

func TestCollectLoopRetroSignalsCountsOnlyTheNode(t *testing.T) {
	got := collectLoopRetroSignals(loopRetroSignalRun(), "node")
	want := loopRetroSignals{
		completed:     1,
		missingResult: 1, // the executor finish after agent.completed no longer counts
		gatePass:      1,
		gateFail:      1,
		loopStarts:    2,
		toolFinishes:  1,
		loopIssues:    []string{"- Loop：第 1 轮", "- Loop：第 2 轮"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("signals = %+v, want %+v", got, want)
	}
	if empty := collectLoopRetroSignals(loopRetroSignalRun(), "missing"); !reflect.DeepEqual(empty, loopRetroSignals{}) {
		t.Fatalf("unknown node signals = %+v, want zero", empty)
	}
}

func TestBuildLoopRetroRendersCollectedSignals(t *testing.T) {
	body := buildLoopRetro(loopRetroSignalRun(), "node", 2, errors.New("gate failed"), "loop")
	for _, want := range []string{
		"- agent.completed：1\n- 门禁通过 / 失败：1 / 1\n- 工具调用（finish）：1\n- 进入自修正：2\n",
		"## 问题\n\n- Loop：第 1 轮\n- Loop：第 2 轮\n- 执行器有退出 0 但没有 `agent.completed`（可能撞 max-turns / 协议中断）\n",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("retro missing %q:\n%s", want, body)
		}
	}
}
