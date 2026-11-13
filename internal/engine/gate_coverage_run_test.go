package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// diffCoverageRepo stages a git repository whose base branch holds a small Go
// file. The workflow script then appends lines on the work branch, so the gate
// sees real committed changes when it diffs merge-base..HEAD.
func diffCoverageRepo(t *testing.T, name string) (root, source string) {
	t.Helper()
	root = t.TempDir()
	source = filepath.Join(root, name)
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "master"},
		{"config", "user.email", "coverage-test@example.com"},
		{"config", "user.name", "Coverage Test"},
	} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "calc.go"), []byte("package calc\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "calc.go"},
		{"commit", "-qm", "baseline"},
	} {
		if output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return root, source
}

// lcovReport renders an lcov report for calc.go: the first slice lists covered
// lines, the second lists executable lines the tests never hit.
func lcovReport(covered []int, uncovered ...int) string {
	var b strings.Builder
	b.WriteString("SF:calc.go\n")
	for _, line := range covered {
		b.WriteString("DA:" + itoa(line) + ",1\n")
	}
	for _, line := range uncovered {
		b.WriteString("DA:" + itoa(line) + ",0\n")
	}
	b.WriteString("end_of_record\n")
	return b.String()
}

func coverageF64(v float64) *float64 { return &v }

func coverageIptr(v int) *int { return &v }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// runDiffCoverageWorkflow appends ten lines to calc.go on the work branch, then
// runs an agent node whose diff_coverage gate is the given command.
func runDiffCoverageWorkflow(t *testing.T, gate domain.VerificationGate) (domain.Run, *repo.Store) {
	t.Helper()
	root, source := diffCoverageRepo(t, "calc")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	putGateTestAgent(t, s, "coverage-validator", []domain.VerificationGate{gate})
	wf := domain.WorkflowDefinition{
		ID: "diff-coverage-gate", Name: "Diff coverage gate", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{
				ID: "change", Name: "Change", Type: "script", ScriptLanguage: "shell",
				ExecutionScope: domain.ExecutionScopeRepositories,
				Script: `printf '\n// added 1\n// added 2\n// added 3\n// added 4\n// added 5\n// added 6\n// added 7\n// added 8\n// added 9\n// added 10\n' >> calc.go` +
					` && git add calc.go && git -c user.email=coverage-test@example.com -c user.name='Coverage Test' commit -qm 'add lines'`,
				MaxAttempts: 1,
			},
			{ID: "judge", Name: "Judge", Type: "agent", AgentID: "coverage-validator", ExecutorID: "mock", MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{{ID: "change-judge", Source: "change", Target: "judge", When: "success"}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	run, err := e.StartWithContext(wf.ID, "", domain.RepositoryContext{
		RepositoryID: "calc", Name: "calc", LocalPath: source, BaseBranch: "master", WorkBranch: "feature/coverage",
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		current, ok := s.Run(run.ID)
		if ok && (current.Status == "succeeded" || current.Status == "failed") {
			return current, s
		}
		time.Sleep(20 * time.Millisecond)
	}
	return run, s
}

func TestDiffCoverageGatePassesWhenChangedLinesAreCovered(t *testing.T) {
	gate := domain.VerificationGate{
		ID: "coverage", Name: "改动行覆盖率", Mode: domain.GateModeDiffCoverage, Required: true, OnFailure: "fail",
		ExecutionScope: domain.ExecutionScopeRepositories,
		Commands:       []string{`printf '%b' "` + strings.ReplaceAll(lcovReport([]int{7, 8, 9, 10, 11, 12, 13, 14, 15, 16}), "\n", `\n`) + `" > coverage.lcov`},
		Coverage:       &domain.GateCoverageSpec{ReportPath: "coverage.lcov", ReportFormat: domain.GateCoverageFormatLCOV},
	}
	run, s := runDiffCoverageWorkflow(t, gate)
	if run.Status != "succeeded" {
		t.Fatalf("run status = %s, node = %+v", run.Status, run.Nodes["judge"])
	}
	live, ok := s.Run(run.ID)
	if !ok {
		t.Fatal("run vanished")
	}
	var judged *domain.TelemetryEvent
	for i := range live.Telemetry {
		if live.Telemetry[i].Name == "gate.coverage.judged" {
			judged = &live.Telemetry[i]
		}
	}
	if judged == nil {
		t.Fatal("a judged gate must emit gate.coverage.judged telemetry")
	}
	if judged.Attributes["applicable"] != true || fmt.Sprint(judged.Attributes["changedLines"]) != "10" {
		t.Fatalf("judged attributes = %#v", judged.Attributes)
	}
}

func TestDiffCoverageGateFailsAndNamesUncoveredLines(t *testing.T) {
	gate := domain.VerificationGate{
		ID: "coverage", Name: "改动行覆盖率", Mode: domain.GateModeDiffCoverage, Required: true, OnFailure: "fail",
		ExecutionScope: domain.ExecutionScopeRepositories,
		Commands:       []string{`printf '%b' "` + strings.ReplaceAll(lcovReport([]int{7, 8, 9, 10, 11}, 12, 13, 14, 15, 16), "\n", `\n`) + `" > coverage.lcov`},
		Coverage: &domain.GateCoverageSpec{
			ReportPath: "coverage.lcov", ReportFormat: domain.GateCoverageFormatLCOV,
			ThresholdPct: coverageF64(80), MinChangedLines: coverageIptr(1),
		},
	}
	run, _ := runDiffCoverageWorkflow(t, gate)
	if run.Status != "failed" {
		var names []string
		for _, ev := range run.Telemetry {
			names = append(names, ev.Category+"/"+ev.Name+": "+ev.Message)
		}
		t.Fatalf("run status = %s judge=%+v events=%v", run.Status, run.Nodes["judge"].Error, names)
	}
	judge := run.Nodes["judge"]
	if !strings.Contains(judge.Error, "calc.go") || !strings.Contains(judge.Error, "L12-L16") {
		t.Fatalf("failure must name the uncovered changed lines: %q", judge.Error)
	}
}

func TestDiffCoverageGateFailsWhenReportIsMissing(t *testing.T) {
	gate := domain.VerificationGate{
		ID: "coverage", Name: "改动行覆盖率", Mode: domain.GateModeDiffCoverage, Required: true, OnFailure: "fail",
		ExecutionScope: domain.ExecutionScopeRepositories,
		Commands:       []string{`true`},
		Coverage:       &domain.GateCoverageSpec{ReportPath: "coverage.lcov", ReportFormat: domain.GateCoverageFormatLCOV, MinChangedLines: coverageIptr(1)},
	}
	run, _ := runDiffCoverageWorkflow(t, gate)
	if run.Status != "failed" {
		var names []string
		for _, ev := range run.Telemetry {
			names = append(names, ev.Category+"/"+ev.Name+": "+ev.Message)
		}
		t.Fatalf("run status = %s judge=%+v events=%v", run.Status, run.Nodes["judge"].Error, names)
	}
	if !strings.Contains(run.Nodes["judge"].Error, "coverage.lcov") {
		t.Fatalf("missing report must be reported with its path: %q", run.Nodes["judge"].Error)
	}
}
