package domain

import (
	"reflect"
	"testing"
)

func TestAnalyzeDiagnosticToolsCollectsEvidenceAndIDs(t *testing.T) {
	d := &AssistantDiagnostics{Status: "failed", Tools: []AssistantDiagnosticTool{
		{ID: "t1", Name: "Bash", Source: "cli", Status: "failed", RequestID: "req-1"},
		{ID: "t2", Name: "Read", Source: "mcp", Status: "running", TraceID: "trace-1"},
		{ID: "t3", Name: "Edit", Source: "cli", Status: "failed"},
		{ID: "t4", Name: "Grep", Source: "cli", Status: "completed", RequestID: "req-2"},
	}}
	a := &AssistantDiagnosticAssessment{}
	requestIDs, traceIDs := map[string]bool{}, map[string]bool{}

	pending := analyzeDiagnosticTools(d, a, requestIDs, traceIDs)

	if pending != 1 {
		t.Fatalf("pending = %d, want 1", pending)
	}
	if d.Tools[1].Status != "unfinished" {
		t.Fatalf("running tool after generation stopped = %q, want unfinished", d.Tools[1].Status)
	}
	wantEvidence := []string{
		"工具 Read（t2，来源：mcp）有开始记录，未收到结束记录",
		"记录到 2 次工具失败（首个：Bash（来源：cli））；工具错误本身不直接等同于整轮生成失败",
	}
	if !reflect.DeepEqual(a.Evidence, wantEvidence) {
		t.Fatalf("evidence = %#v, want %#v", a.Evidence, wantEvidence)
	}
	if !reflect.DeepEqual(requestIDs, map[string]bool{"req-1": true, "req-2": true}) {
		t.Fatalf("requestIDs = %#v", requestIDs)
	}
	if !reflect.DeepEqual(traceIDs, map[string]bool{"trace-1": true}) {
		t.Fatalf("traceIDs = %#v", traceIDs)
	}
}

func TestAnalyzeDiagnosticToolsKeepsRunningToolWhileGenerationRuns(t *testing.T) {
	d := &AssistantDiagnostics{Status: "running", Tools: []AssistantDiagnosticTool{
		{ID: "t1", Name: "Bash", Source: "cli", Status: "running"},
	}}
	a := &AssistantDiagnosticAssessment{}

	pending := analyzeDiagnosticTools(d, a, map[string]bool{}, map[string]bool{})

	if pending != 1 || d.Tools[0].Status != "running" {
		t.Fatalf("pending = %d status = %q, want 1 running", pending, d.Tools[0].Status)
	}
	if len(a.Evidence) != 1 {
		t.Fatalf("evidence = %#v, want only the unfinished-tool entry", a.Evidence)
	}
}

func TestAnalyzeDiagnosticToolsWithoutToolsAddsNothing(t *testing.T) {
	d := &AssistantDiagnostics{Status: "completed"}
	a := &AssistantDiagnosticAssessment{}
	if pending := analyzeDiagnosticTools(d, a, map[string]bool{}, map[string]bool{}); pending != 0 || len(a.Evidence) != 0 {
		t.Fatalf("pending = %d evidence = %#v, want none", pending, a.Evidence)
	}
}
