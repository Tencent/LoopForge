package domain

import (
	"strings"
	"testing"
)

func f64(v float64) *float64 { return &v }
func iptr(v int) *int        { return &v }

func diffCoverageGate(spec *GateCoverageSpec) VerificationGate {
	return VerificationGate{
		ID: "coverage", Name: "改动行覆盖率", Mode: GateModeDiffCoverage, Required: true, OnFailure: "loop",
		Commands:       []string{"go test -coverprofile=coverage.out ./..."},
		ExecutionScope: ExecutionScopeRepositories,
		Coverage:       spec,
	}
}

func TestValidateDiffCoverageGateRequiresReportSpec(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*VerificationGate)
		wantText string
	}{
		{"missing spec", func(g *VerificationGate) { g.Coverage = nil }, "report spec"},
		{"missing path", func(g *VerificationGate) { g.Coverage.ReportPath = " " }, "reportPath"},
		{"missing format", func(g *VerificationGate) { g.Coverage.ReportFormat = "" }, "reportFormat"},
		{"unknown format", func(g *VerificationGate) { g.Coverage.ReportFormat = "nunit" }, "reportFormat"},
		{"run scope", func(g *VerificationGate) { g.ExecutionScope = ExecutionScopeRun }, "executionScope"},
		{"no commands", func(g *VerificationGate) { g.Commands = nil }, "commands"},
		{"threshold above 100", func(g *VerificationGate) { g.Coverage.ThresholdPct = f64(101) }, "thresholdPct"},
		{"negative threshold", func(g *VerificationGate) { g.Coverage.ThresholdPct = f64(-1) }, "thresholdPct"},
		{"negative min lines", func(g *VerificationGate) { g.Coverage.MinChangedLines = iptr(-2) }, "minChangedLines"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := diffCoverageGate(&GateCoverageSpec{ReportPath: "coverage.out", ReportFormat: GateCoverageFormatGoCoverprofile})
			tc.mutate(&gate)
			err := ValidateVerificationGate(gate)
			if err == nil || !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("want error mentioning %q, got %v", tc.wantText, err)
			}
		})
	}
	ok := diffCoverageGate(&GateCoverageSpec{ReportPath: "coverage.out", ReportFormat: GateCoverageFormatGoCoverprofile, ThresholdPct: f64(0), MinChangedLines: iptr(0)})
	if err := ValidateVerificationGate(ok); err != nil {
		t.Fatalf("valid diff-coverage gate rejected: %v", err)
	}
}

func TestValidateDiffCoverageGateRejectsCoverageOnCommandGate(t *testing.T) {
	gate := VerificationGate{ID: "build", Mode: GateModeCommand, Commands: []string{"go build ./..."}, ExecutionScope: ExecutionScopeRun,
		Coverage: &GateCoverageSpec{ReportPath: "coverage.out", ReportFormat: GateCoverageFormatLCOV}}
	if err := ValidateVerificationGate(gate); err == nil || !strings.Contains(err.Error(), "only accepts coverage settings") {
		t.Fatalf("coverage on a command gate must be rejected, got %v", err)
	}
}

func TestParseGoCoverprofileExpandsBlocks(t *testing.T) {
	report, err := ParseCoverageReport(GateCoverageFormatGoCoverprofile, strings.NewReader(`mode: set
agentflow/internal/domain/a.go:10.5,12.20 1 1
agentflow/internal/domain/a.go:20.1,20.9 1 0
agentflow/internal/domain/b.go:3.1,3.2 1 2
`))
	if err != nil {
		t.Fatal(err)
	}
	a, ok := report["agentflow/internal/domain/a.go"]
	if !ok {
		t.Fatalf("missing a.go in %v", report)
	}
	for _, line := range []int{10, 11, 12} {
		if hits, ok := a.Hits[line]; !ok || hits != 1 {
			t.Fatalf("line %d: want covered once, got %v %v", line, hits, ok)
		}
	}
	if hits, ok := a.Hits[20]; !ok || hits != 0 {
		t.Fatalf("line 20: want executable but uncovered, got %v %v", hits, ok)
	}
	if _, ok := a.Hits[13]; ok {
		t.Fatal("line 13 is outside the block and must not be executable")
	}
}

func TestParseGoCoverprofileRejectsMalformedInput(t *testing.T) {
	for _, input := range []string{
		"mode: set\nbad line without colon\n",
		"mode: set\nagentflow/x.go:10.1,12.1 1\n",
		"mode: set\nagentflow/x.go:10.1,12.1 1 nope\n",
		"mode: set\nagentflow/x.go:10.1 1 0\n",
	} {
		if _, err := ParseCoverageReport(GateCoverageFormatGoCoverprofile, strings.NewReader(input)); err == nil {
			t.Fatalf("expected parse error for %q", input)
		}
	}
	if _, err := ParseCoverageReport(GateCoverageFormatGoCoverprofile, strings.NewReader("mode: set\n")); err == nil {
		t.Fatal("empty report must fail instead of passing silently")
	}
}

func TestParseLCOV(t *testing.T) {
	report, err := ParseCoverageReport(GateCoverageFormatLCOV, strings.NewReader(`TN:
SF:/build/repo/internal/x/foo.go
DA:4,3
DA:5,0
DA:8,1
end_of_record
SF:web/app/bar.ts
DA:2,1
end_of_record
`))
	if err != nil {
		t.Fatal(err)
	}
	foo := report["/build/repo/internal/x/foo.go"]
	if foo.Hits[4] != 3 || foo.Hits[5] != 0 || foo.Hits[8] != 1 {
		t.Fatalf("unexpected lcov hits: %+v", foo.Hits)
	}
}

func TestParseCobertura(t *testing.T) {
	report, err := ParseCoverageReport(GateCoverageFormatCobertura, strings.NewReader(`<?xml version="1.0" ?>
<coverage>
  <packages>
    <package name="app">
      <classes>
        <class name="Foo" filename="src/foo.py">
          <lines>
            <line number="3" hits="1"/>
            <line number="4" hits="0"/>
          </lines>
        </class>
      </classes>
    </package>
  </packages>
</coverage>`))
	if err != nil {
		t.Fatal(err)
	}
	foo := report["src/foo.py"]
	if foo.Hits[3] != 1 || foo.Hits[4] != 0 {
		t.Fatalf("unexpected cobertura hits: %+v", foo.Hits)
	}
}

func TestParseCoverageReportRejectsUnknownFormat(t *testing.T) {
	if _, err := ParseCoverageReport("nunit", strings.NewReader("")); err == nil {
		t.Fatal("unknown format must be a config error")
	}
}

func TestParseUnifiedDiffChangedLines(t *testing.T) {
	diff := `diff --git a/internal/a.go b/internal/a.go
index 111..222 100644
--- a/internal/a.go
+++ b/internal/a.go
@@ -1,0 +2,3 @@ func existing() {
@@ -20 +25,0 @@ func deleted() {
+removed only
diff --git a/new.go b/new.go
new file mode 100644
--- /dev/null
+++ b/new.go
@@ -0,0 +1,2 @@
+package a
+func New() {}
diff --git a/gone.go b/gone.go
deleted file mode 100644
--- a/gone.go
+++ /dev/null
@@ -1,4 +0,0 @@
-package a
`
	changed, err := ParseUnifiedDiffChangedLines(strings.NewReader(diff))
	if err != nil {
		t.Fatal(err)
	}
	if got := changed["internal/a.go"]; len(got) != 1 || got[0] != (LineRange{Start: 2, End: 4}) {
		t.Fatalf("modified file ranges: %+v", got)
	}
	if got := changed["new.go"]; len(got) != 1 || got[0] != (LineRange{Start: 1, End: 2}) {
		t.Fatalf("new file ranges: %+v", got)
	}
	if _, ok := changed["gone.go"]; ok {
		t.Fatal("a deleted file has no lines left to cover")
	}
}

func TestParseUnifiedDiffMergesAdjacentRanges(t *testing.T) {
	diff := `--- a/x.go
+++ b/x.go
@@ -1,0 +5,1 @@
@@ -1,0 +6,2 @@
`
	changed, err := ParseUnifiedDiffChangedLines(strings.NewReader(diff))
	if err != nil {
		t.Fatal(err)
	}
	if got := changed["x.go"]; len(got) != 1 || got[0] != (LineRange{Start: 5, End: 7}) {
		t.Fatalf("adjacent hunks should merge: %+v", got)
	}
}

func TestJudgeDiffCoverageCountsOnlyChangedExecutableLines(t *testing.T) {
	// Change touches lines 10-12 (covered) and 20 (uncovered). Line 11 is a
	// comment per the report (not executable) and must not dilute the score.
	report, err := ParseCoverageReport(GateCoverageFormatGoCoverprofile, strings.NewReader(`mode: set
agentflow/internal/domain/a.go:10.1,10.9 1 1
agentflow/internal/domain/a.go:12.1,12.9 1 1
agentflow/internal/domain/a.go:20.1,20.9 1 0
agentflow/internal/domain/a.go:30.1,30.9 1 0
`))
	if err != nil {
		t.Fatal(err)
	}
	changed := ChangedLines{"internal/domain/a.go": {{Start: 10, End: 12}, {Start: 20, End: 20}}}
	verdict := JudgeDiffCoverage(GateCoverageSpec{ReportPath: "coverage.out", ReportFormat: GateCoverageFormatGoCoverprofile, MinChangedLines: iptr(1)}, changed, report)
	if !verdict.Applicable {
		t.Fatalf("expected applicable verdict: %+v", verdict)
	}
	if verdict.ChangedLines != 3 || verdict.CoveredLines != 2 {
		t.Fatalf("want 2/3 changed executable lines covered (line 11 skipped, line 30 untouched), got %+v", verdict)
	}
	if verdict.Passed() {
		t.Fatalf("66%% must fail an 80%% threshold: %+v", verdict)
	}
	if len(verdict.Files) != 1 || len(verdict.Files[0].UncoveredRanges) != 1 || verdict.Files[0].UncoveredRanges[0] != (LineRange{Start: 20, End: 20}) {
		t.Fatalf("uncovered range should name line 20: %+v", verdict.Files)
	}
	feedback := verdict.Feedback()
	if !strings.Contains(feedback, "L20") || !strings.Contains(feedback, "补充测试") {
		t.Fatalf("feedback must name the uncovered line: %q", feedback)
	}
}

func TestJudgeDiffCoverageLowLegacyCoverageStillPasses(t *testing.T) {
	report, err := ParseCoverageReport(GateCoverageFormatLCOV, strings.NewReader("SF:internal/big.go\nDA:1,1\nDA:2,0\nDA:3,0\nend_of_record\nSF:internal/new.go\nDA:7,4\nend_of_record\n"))
	if err != nil {
		t.Fatal(err)
	}
	changed := ChangedLines{"internal/new.go": {{Start: 7, End: 7}}}
	verdict := JudgeDiffCoverage(GateCoverageSpec{ReportPath: "lcov.info", ReportFormat: GateCoverageFormatLCOV, MinChangedLines: iptr(1)}, changed, report)
	if !verdict.Applicable || !verdict.Passed() || verdict.CoveragePct != 100 {
		t.Fatalf("only the changed file counts, legacy low coverage is irrelevant: %+v", verdict)
	}
}

func TestJudgeDiffCoverageSkipsSmallChanges(t *testing.T) {
	report, _ := ParseCoverageReport(GateCoverageFormatLCOV, strings.NewReader("SF:x.go\nDA:1,0\nend_of_record\n"))
	changed := ChangedLines{"x.go": {{Start: 1, End: 1}}}
	verdict := JudgeDiffCoverage(GateCoverageSpec{ReportPath: "lcov.info", ReportFormat: GateCoverageFormatLCOV}, changed, report)
	if verdict.Applicable || !verdict.Passed() {
		t.Fatalf("a one-line change under the default minChangedLines=5 must be not applicable: %+v", verdict)
	}
	if !strings.Contains(verdict.Feedback(), "不适用") {
		t.Fatalf("feedback should say why: %q", verdict.Feedback())
	}
}

func TestJudgeDiffCoverageUnreportedFileCountsAsUncovered(t *testing.T) {
	report, _ := ParseCoverageReport(GateCoverageFormatLCOV, strings.NewReader("SF:covered.go\nDA:1,1\nend_of_record\n"))
	changed := ChangedLines{
		"covered.go":      {{Start: 1, End: 1}},
		"untested/new.go": {{Start: 1, End: 4}},
	}
	verdict := JudgeDiffCoverage(GateCoverageSpec{ReportPath: "lcov.info", ReportFormat: GateCoverageFormatLCOV, MinChangedLines: iptr(1)}, changed, report)
	if verdict.Passed() {
		t.Fatalf("a changed file with no coverage data must fail: %+v", verdict)
	}
	if len(verdict.UnreportedFiles) != 1 || verdict.UnreportedFiles[0] != "untested/new.go" {
		t.Fatalf("unreported file should be named: %+v", verdict.UnreportedFiles)
	}
}

func TestJudgeDiffCoverageExcludesTestsAndGeneratedCode(t *testing.T) {
	report, _ := ParseCoverageReport(GateCoverageFormatGoCoverprofile, strings.NewReader(`mode: set
agentflow/internal/domain/a_test.go:5.1,5.2 1 0
agentflow/internal/domain/a.pb.go:9.1,9.2 1 0
`))
	changed := ChangedLines{
		"internal/domain/a_test.go": {{Start: 5, End: 5}},
		"internal/domain/a.pb.go":   {{Start: 9, End: 9}},
	}
	spec := GateCoverageSpec{ReportPath: "coverage.out", ReportFormat: GateCoverageFormatGoCoverprofile, MinChangedLines: iptr(1)}
	verdict := JudgeDiffCoverage(spec, changed, report)
	if verdict.Applicable || verdict.ChangedLines != 0 {
		t.Fatalf("tests and generated code must not be judged: %+v", verdict)
	}

	// A project exclude can drop a file the defaults kept.
	changed = ChangedLines{"internal/domain/handler.go": {{Start: 3, End: 3}}}
	spec.Exclude = []string{"internal/domain/handler.go"}
	if verdict := JudgeDiffCoverage(spec, changed, report); verdict.ChangedLines != 0 {
		t.Fatalf("explicit exclude must win: %+v", verdict)
	}
}

func TestCoverageDefaults(t *testing.T) {
	threshold, minLines := GateCoverageSpec{}.CoverageDefaults()
	if threshold != DefaultGateCoverageThresholdPct || minLines != DefaultGateCoverageMinChangedLines {
		t.Fatalf("unexpected defaults %v %v", threshold, minLines)
	}
	threshold, minLines = GateCoverageSpec{ThresholdPct: f64(92.5), MinChangedLines: iptr(0)}.CoverageDefaults()
	if threshold != 92.5 || minLines != 0 {
		t.Fatalf("explicit values should win, got %v %v", threshold, minLines)
	}
}

func TestFormatLineRanges(t *testing.T) {
	got := FormatLineRanges([]LineRange{{Start: 12, End: 18}, {Start: 40, End: 40}})
	if got != "L12-L18,L40" {
		t.Fatalf("got %q", got)
	}
}
