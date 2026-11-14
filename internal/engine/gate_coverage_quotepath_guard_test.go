package engine

import (
	"testing"

	"agentflow/internal/domain"
)

// The untracked-file path is covered by
// TestDiffCoverageScriptJudgesNonASCIIGoFileNames. These guard the committed
// path: `git diff <merge-base>` must also run with core.quotepath=off, or git
// prints `+++ "b/\303\274n\303\257.go"` and the changed lines of a committed
// non-ASCII file silently drop out of the judgement.

func TestDiffCoverageScriptJudgesCommittedNonASCIIGoFileNames(t *testing.T) {
	_, source := diffCoverageRepo(t, "calc")
	gitIn(t, source, "checkout", "-q", "-b", "feature/unicode-committed")
	writeRepoFile(t, source, "ünï.go", "package calc\n\nfunc U(a int) int {\n\treturn a * 2\n}\n")
	gitIn(t, source, "add", "ünï.go")
	gitIn(t, source, "commit", "-qm", "add ünï.go")
	gate := domain.VerificationGate{
		ID: "coverage", Mode: domain.GateModeDiffCoverage,
		// The profile covers the committed file, so the gate can only pass if
		// the diff names it in the same (unquoted) form as the profile.
		Commands: []string{`printf 'mode: set\nagentflow/ünï.go:3.19,5.2 1 1\n' > coverage.out`},
		Coverage: &domain.GateCoverageSpec{ReportPath: "coverage.out", ReportFormat: domain.GateCoverageFormatGoCoverprofile, MinChangedLines: coverageIptr(1)},
	}
	output, verdict, err := runDiffCoverageScript(t, source, gate, "master")
	if err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
	if !verdict.Applicable || verdict.ChangedLines == 0 || verdict.CoveredLines != verdict.ChangedLines || len(verdict.UnreportedFiles) != 0 || !verdict.Passed() {
		t.Fatalf("a committed non-ASCII Go file must be judged against its profile block: %+v\n%s", verdict, output)
	}
}

func TestDiffCoverageScriptFailsUncoveredCommittedNonASCIIFile(t *testing.T) {
	_, source := diffCoverageRepo(t, "calc")
	gitIn(t, source, "checkout", "-q", "-b", "feature/unicode-uncovered")
	writeRepoFile(t, source, "数据.go", "package calc\n\nfunc D(a int) int {\n\treturn a - 1\n}\n")
	gitIn(t, source, "add", "数据.go")
	gitIn(t, source, "commit", "-qm", "add 数据.go")
	gate := domain.VerificationGate{
		ID: "coverage", Mode: domain.GateModeDiffCoverage,
		// The profile only knows calc.go; the committed new file is untested.
		Commands: []string{`printf 'mode: set\nagentflow/calc.go:3.24,5.2 1 1\n' > coverage.out`},
		Coverage: &domain.GateCoverageSpec{ReportPath: "coverage.out", ReportFormat: domain.GateCoverageFormatGoCoverprofile, MinChangedLines: coverageIptr(1)},
	}
	output, verdict, err := runDiffCoverageScript(t, source, gate, "master")
	if err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
	if !verdict.Applicable || verdict.Passed() || len(verdict.UnreportedFiles) != 1 || verdict.UnreportedFiles[0] != "数据.go" {
		t.Fatalf("an untested committed non-ASCII file must fail the gate by name: %+v\n%s", verdict, output)
	}
}
