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

// runDiffCoverageScript runs the generated gate script in dir the way a gate
// executor would (sh -c, base branch exported) and returns the judged verdict.
func runDiffCoverageScript(t *testing.T, dir string, gate domain.VerificationGate, base string) (string, domain.DiffCoverageVerdict, error) {
	t.Helper()
	script, err := diffCoverageCommand(gate, strings.Join(gate.Commands, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "AGENTFLOW_BASE_BRANCH="+base)
	output, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("gate script failed: %v\n%s", runErr, output)
	}
	payload, err := extractDiffCoveragePayload(string(output))
	if err != nil {
		return string(output), domain.DiffCoverageVerdict{}, err
	}
	verdict, err := judgeDiffCoveragePayload(gate, payload)
	return string(output), verdict, err
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	if output, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func TestDiffCoverageScriptCountsUntrackedFilesFromTheSameNode(t *testing.T) {
	_, source := diffCoverageRepo(t, "calc")
	gitIn(t, source, "checkout", "-q", "-b", "feature/untracked")
	// The gate command itself writes a new, never-added source file plus the
	// report; the report does not mention the new file.
	extra := "package calc\n\nfunc Sub(a, b int) int {\n\tif a > b {\n\t\treturn a - b\n\t}\n\treturn b - a\n}\n\nfunc Neg(a int) int {\n\treturn -a\n}\n"
	if err := os.WriteFile(filepath.Join(source, "extra.go"), []byte(extra), 0o600); err != nil {
		t.Fatal(err)
	}
	gate := domain.VerificationGate{
		ID: "coverage", Mode: domain.GateModeDiffCoverage,
		Commands: []string{`printf 'SF:calc.go\nDA:4,1\nend_of_record\n' > coverage.lcov`},
		Coverage: &domain.GateCoverageSpec{ReportPath: "coverage.lcov", ReportFormat: domain.GateCoverageFormatLCOV, MinChangedLines: coverageIptr(1)},
	}
	output, verdict, err := runDiffCoverageScript(t, source, gate, "master")
	if err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
	if !verdict.Applicable || verdict.Passed() || len(verdict.UnreportedFiles) != 1 || verdict.UnreportedFiles[0] != "extra.go" || verdict.ChangedLines != 12 {
		t.Fatalf("an untracked new source file must be judged as uncovered: %+v\n%s", verdict, output)
	}
	// The gate must not touch the index: artifact collection owns git add -N.
	status, err := exec.Command("git", "-C", source, "status", "--porcelain", "--", "extra.go").CombinedOutput()
	if err != nil || strings.TrimSpace(string(status)) != "?? extra.go" {
		t.Fatalf("extra.go should stay untracked, got %q (%v)", status, err)
	}
}

func TestDiffCoverageScriptNamesMissingBaseBranch(t *testing.T) {
	_, source := diffCoverageRepo(t, "calc")
	gate := domain.VerificationGate{
		ID: "coverage", Mode: domain.GateModeDiffCoverage,
		Commands: []string{`true`},
		Coverage: &domain.GateCoverageSpec{ReportPath: "coverage.out", ReportFormat: domain.GateCoverageFormatGoCoverprofile},
	}
	output, _, err := runDiffCoverageScript(t, source, gate, "no-such-branch")
	if err == nil || !strings.Contains(err.Error(), "找不到基线分支 no-such-branch") {
		t.Fatalf("a missing base branch must be named instead of exit status 128: %v\n%s", err, output)
	}
}

func TestDiffCoverageScriptKeepsPayloadSmall(t *testing.T) {
	_, source := diffCoverageRepo(t, "calc")
	gitIn(t, source, "checkout", "-q", "-b", "feature/small")
	// A large change body and a large profile of untouched files: neither may
	// reach the payload, which a CLI Runner returns only up to 32 KiB.
	var body strings.Builder
	body.WriteString("package calc\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n")
	for i := 0; i < 2000; i++ {
		body.WriteString("// padding line with enough text to make the diff body large\n")
	}
	if err := os.WriteFile(filepath.Join(source, "calc.go"), []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, source, "commit", "-qam", "grow calc.go")
	var profile strings.Builder
	profile.WriteString("mode: set\n")
	for i := 0; i < 3000; i++ {
		profile.WriteString("agentflow/other/untouched" + itoa(i) + ".go:1.1,2.2 1 1\n")
	}
	profile.WriteString("agentflow/calc.go:6.1,6.10 1 1\n")
	if err := os.WriteFile(filepath.Join(source, "coverage.out"), []byte(profile.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	gate := domain.VerificationGate{
		ID: "coverage", Mode: domain.GateModeDiffCoverage,
		Commands: []string{`true`},
		Coverage: &domain.GateCoverageSpec{ReportPath: "coverage.out", ReportFormat: domain.GateCoverageFormatGoCoverprofile, MinChangedLines: coverageIptr(1)},
	}
	output, verdict, err := runDiffCoverageScript(t, source, gate, "master")
	if err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
	if !verdict.Applicable || !verdict.Passed() || verdict.ChangedLines != 1 || verdict.CoveredLines != 1 {
		t.Fatalf("the changed file's profile block must survive the filter: %+v", verdict)
	}
	payload, _ := extractDiffCoveragePayload(output)
	if len(payload) > 4<<10 || strings.Contains(payload, "padding line") || strings.Contains(payload, "untouched") {
		t.Fatalf("payload must hold only hunk headers and changed-file profile blocks (%d bytes):\n%.2000s", len(payload), payload)
	}
}
