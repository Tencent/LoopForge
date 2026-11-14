package engine

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func coverageGateSpec() *domain.GateCoverageSpec {
	return &domain.GateCoverageSpec{ReportPath: "coverage.out", ReportFormat: domain.GateCoverageFormatGoCoverprofile}
}

func TestDiffCoverageCommandRunsTestsBeforeEmittingPayload(t *testing.T) {
	gate := domain.VerificationGate{ID: "cov", Name: "覆盖率", Mode: domain.GateModeDiffCoverage, Commands: []string{"go test -coverprofile=coverage.out ./..."}, Coverage: coverageGateSpec()}
	cmd, err := diffCoverageCommand(gate, "go test -coverprofile=coverage.out ./...")
	if err != nil {
		t.Fatal(err)
	}
	testIdx := strings.Index(cmd, "go test -coverprofile=coverage.out ./...")

	markerIdx := strings.Index(cmd, diffCoverageMarker)
	reportIdx := strings.Index(cmd, diffCoverageReportMarker)
	readIdx := strings.Index(cmd, "if [ -f 'coverage.out' ]")
	endIdx := strings.Index(cmd, diffCoverageEndMarker)
	if testIdx < 0 || markerIdx < testIdx || reportIdx < markerIdx || readIdx < reportIdx || endIdx < readIdx {
		t.Fatalf("commands must run first and the report must be emitted last:\n%s", cmd)
	}
	if !strings.Contains(cmd, "--no-pager diff -U0") || !strings.Contains(cmd, "merge-base") || !strings.Contains(cmd, "core.quotepath=off") {
		t.Fatalf("diff ranges must come from merge-base..HEAD:\n%s", cmd)
	}
	if !strings.Contains(cmd, "${AGENTFLOW_BASE_BRANCH:-main}") || strings.Contains(cmd, "'${AGENTFLOW_BASE_BRANCH") {
		t.Fatalf("the gate should fall back to the repository base branch:\n%s", cmd)
	}
	if !strings.HasPrefix(cmd, "set -e") || strings.Index(cmd, "set +e") < markerIdx {
		t.Fatalf("a failing test run must stop before emitting a payload:\n%s", cmd)
	}
	// The markers are printed by echo; a bare marker line would be executed as
	// a command and fail the gate under set -e.
	for _, line := range strings.Split(cmd, "\n") {
		if line == diffCoverageMarker || line == diffCoverageReportMarker || line == diffCoverageEndMarker {
			t.Fatalf("markers must be echoed, not run as commands:\n%s", cmd)
		}
	}
}

func TestDiffCoverageCommandUsesConfiguredBaseBranch(t *testing.T) {
	spec := coverageGateSpec()
	spec.BaseBranch = "release/2.x"
	cmd, err := diffCoverageCommand(domain.VerificationGate{ID: "cov", Mode: domain.GateModeDiffCoverage, Coverage: spec}, "true")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cmd, "${AGENTFLOW_BASE_BRANCH:-main}") || !strings.Contains(cmd, "'release/2.x'") {
		t.Fatalf("an explicit base branch must win:\n%s", cmd)
	}
}

func TestDiffCoverageCommandRejectsIncompleteSpec(t *testing.T) {
	if _, err := diffCoverageCommand(domain.VerificationGate{ID: "cov"}, "true"); err == nil {
		t.Fatal("missing spec must be an error")
	}
	if _, err := diffCoverageCommand(domain.VerificationGate{ID: "cov", Coverage: &domain.GateCoverageSpec{ReportFormat: domain.GateCoverageFormatLCOV}}, "true"); err == nil {
		t.Fatal("missing report path must be an error")
	}
}

func TestSplitDiffCoverageOutputSeparatesTestLog(t *testing.T) {
	output := "ok  agentflow/internal/domain  0.002s\n" + diffCoverageMarker + "\n--- a/x.go\n+++ b/x.go\n" + diffCoverageReportMarker + "\nmode: set\nx.go:1.1,1.2 1 1\n"
	testOutput, payload, ok := splitDiffCoverageOutput(output)
	if !ok {
		t.Fatal("marker not found")
	}
	if !strings.Contains(testOutput, "ok  agentflow/internal/domain") {
		t.Fatalf("test output: %q", testOutput)
	}
	if strings.Contains(payload, "ok  agentflow") || !strings.HasPrefix(payload, "--- a/x.go") {
		t.Fatalf("payload should start at the diff: %q", payload)
	}
	if _, _, ok := splitDiffCoverageOutput("no marker here"); ok {
		t.Fatal("a payload without the marker must be reported as missing")
	}
}

func TestJudgeDiffCoveragePayloadFailsWhenReportMissing(t *testing.T) {
	gate := domain.VerificationGate{ID: "cov", Mode: domain.GateModeDiffCoverage, Coverage: coverageGateSpec()}
	payload := "--- a/x.go\n+++ b/x.go\n@@ -1,0 +2,1 @@\n" + strings.Repeat("+line\n", 1) + diffCoverageReportMarker + "\n"
	_, err := judgeDiffCoveragePayload(gate, payload)
	if err == nil || !strings.Contains(err.Error(), "未找到覆盖率报告") {
		t.Fatalf("an empty report must fail the gate, not pass it: %v", err)
	}
}

func TestJudgeDiffCoveragePayloadNamesUncoveredLines(t *testing.T) {
	gate := domain.VerificationGate{ID: "cov", Mode: domain.GateModeDiffCoverage, Coverage: coverageGateSpec()}
	gate.Coverage.MinChangedLines = intPtr(1)
	payload := strings.Join([]string{
		"--- a/internal/x/foo.go",
		"+++ b/internal/x/foo.go",
		"@@ -1,0 +10,3 @@",
		diffCoverageReportMarker,
		"mode: set",
		"agentflow/internal/x/foo.go:10.1,10.2 1 1",
		"agentflow/internal/x/foo.go:11.1,11.2 1 0",
		"agentflow/internal/x/foo.go:12.1,12.2 1 0",
	}, "\n")
	verdict, err := judgeDiffCoveragePayload(gate, payload)
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Passed() {
		t.Fatalf("one covered line out of three must fail an 80%% threshold: %+v", verdict)
	}
	if len(verdict.Files) != 1 || domain.FormatLineRanges(verdict.Files[0].UncoveredRanges) != "L11-L12" {
		t.Fatalf("uncovered ranges: %+v", verdict.Files)
	}
	if feedback := verdict.Feedback(); !strings.Contains(feedback, "L11-L12") || !strings.Contains(feedback, "internal/x/foo.go") {
		t.Fatalf("feedback must name file and lines: %q", feedback)
	}
}

func TestJudgeDiffCoveragePayloadWithoutChangesIsNotApplicable(t *testing.T) {
	gate := domain.VerificationGate{ID: "cov", Mode: domain.GateModeDiffCoverage, Coverage: coverageGateSpec()}
	verdict, err := judgeDiffCoveragePayload(gate, diffCoverageMarker+"\n"+diffCoverageReportMarker+"\nmode: set\nx.go:1.1,1.2 1 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Applicable || verdict.Passed() != true {
		t.Fatalf("an empty diff should pass without judging: %+v", verdict)
	}
}

func TestJudgeDiffCoveragePayloadRejectsMalformedReport(t *testing.T) {
	gate := domain.VerificationGate{ID: "cov", Mode: domain.GateModeDiffCoverage, Coverage: coverageGateSpec()}
	payload := "--- a/x.go\n+++ b/x.go\n@@ -1,0 +1,1 @@\n" + diffCoverageReportMarker + "\nnot a coverprofile\n"
	if _, err := judgeDiffCoveragePayload(gate, payload); err == nil {
		t.Fatal("an unparsable report must surface as an error, never as a pass")
	}
}

func TestDiffCoverageTaskLineStatesThreshold(t *testing.T) {
	gate := domain.VerificationGate{ID: "cov", Name: "改动行覆盖率", Mode: domain.GateModeDiffCoverage, Coverage: coverageGateSpec()}
	line := diffCoverageTaskLine(gate)
	for _, want := range []string{"改动行覆盖率", "80%", "5 行", "配套测试"} {
		if !strings.Contains(line, want) {
			t.Fatalf("task line %q should mention %q", line, want)
		}
	}
}

func TestExtractDiffCoveragePayloadDetectsTruncation(t *testing.T) {
	full := "ok tests\n" + diffCoverageMarker + "\n+++ b/x.go\n@@ -1,0 +1,1 @@\n" + diffCoverageReportMarker + "\nmode: set\nx.go:1.1,1.2 1 1\n" + diffCoverageEndMarker + "\n"
	payload, err := extractDiffCoveragePayload(full)
	if err != nil || strings.Contains(payload, diffCoverageEndMarker) || !strings.HasSuffix(payload, "x.go:1.1,1.2 1 1") {
		t.Fatalf("complete output: payload=%q err=%v", payload, err)
	}
	// A CLI Runner keeps only the tail: the start marker is gone.
	tail := full[strings.Index(full, "@@"):]
	if _, err := extractDiffCoveragePayload(tail); err == nil || !strings.Contains(err.Error(), "截断") {
		t.Fatalf("a tail-truncated payload must say it was truncated: %v", err)
	}
	// Head-truncated output (visible text cut at the limit) loses the end.
	head := full[:strings.Index(full, diffCoverageReportMarker)+10]
	if _, err := extractDiffCoveragePayload(head); err == nil || !strings.Contains(err.Error(), "截断") {
		t.Fatalf("a head-truncated payload must say it was truncated: %v", err)
	}
	if _, err := extractDiffCoveragePayload("plain log"); err == nil || !strings.Contains(err.Error(), "未输出覆盖率数据") {
		t.Fatalf("output without any marker: %v", err)
	}
}

func TestJudgeDiffCoveragePayloadReportsMissingBaseBranch(t *testing.T) {
	gate := domain.VerificationGate{ID: "cov", Mode: domain.GateModeDiffCoverage, Coverage: coverageGateSpec()}
	payload := diffCoverageErrorMarker + "找不到基线分支 develop（本地和 origin 都没有），无法计算改动行\n" + diffCoverageReportMarker + "\nmode: set\nx.go:1.1,1.2 1 1\n"
	if _, err := judgeDiffCoveragePayload(gate, payload); err == nil || !strings.Contains(err.Error(), "找不到基线分支 develop") {
		t.Fatalf("a missing base branch must be named: %v", err)
	}
}

func intPtr(v int) *int { return &v }
