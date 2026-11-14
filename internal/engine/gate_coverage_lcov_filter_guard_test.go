package engine

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// These guard the path normalisation in lcovFilterAwk. A changed file whose
// SF record the filter fails to match is dropped from the payload (its
// extension already has a sample record), so the judge reports the file as
// unreported and the gate fails although the tests covered it. Each case puts
// an unrelated .ts record first so the per-extension sample cannot mask a
// dropped record.

func lcovGuardGate() domain.VerificationGate {
	return domain.VerificationGate{
		ID: "coverage", Mode: domain.GateModeDiffCoverage,
		Commands: []string{`true`},
		Coverage: &domain.GateCoverageSpec{ReportPath: "coverage/lcov.info", ReportFormat: domain.GateCoverageFormatLCOV, MinChangedLines: coverageIptr(1), Exclude: []string{"coverage/**"}},
	}
}

func assertLCOVFileCovered(t *testing.T, source, file, why string) {
	t.Helper()
	output, verdict, err := runDiffCoverageScript(t, source, lcovGuardGate(), "master")
	if err != nil {
		t.Fatalf("%v\n%.2000s", err, output)
	}
	if !verdict.Applicable || verdict.ChangedLines != 3 || verdict.CoveredLines != 3 || len(verdict.UnreportedFiles) != 0 || !verdict.Passed() {
		payload, _ := extractDiffCoveragePayload(output)
		t.Fatalf("%s: %s must be judged 3/3 from its own record: %+v\n%.2000s", why, file, verdict, payload)
	}
}

func TestDiffCoverageScriptKeepsLCOVRecordsOfFileNamesWithSpaces(t *testing.T) {
	_, source := diffCoverageRepo(t, "web")
	gitIn(t, source, "checkout", "-q", "-b", "feature/lcov-space")
	// git writes "+++ b/src/my file.ts<TAB>" for this untracked file.
	writeRepoFile(t, source, "src/my file.ts", "export const a = 1\nexport const b = 2\nexport const c = 3\n")
	writeRepoFile(t, source, "coverage/lcov.info", "TN:\n"+
		"SF:src/other.ts\nDA:1,1\nDA:2,1\nend_of_record\n"+
		"SF:src/my file.ts\nDA:1,1\nDA:2,1\nDA:3,1\nend_of_record\n")
	assertLCOVFileCovered(t, source, "src/my file.ts", "the diff's trailing TAB must not hide a file name with a space")
}

func TestDiffCoverageScriptAcceptsCRLFLCOVReports(t *testing.T) {
	_, source := diffCoverageRepo(t, "web")
	gitIn(t, source, "checkout", "-q", "-b", "feature/lcov-crlf")
	writeRepoFile(t, source, "src/a.ts", "export const a = 1\nexport const b = 2\nexport const c = 3\n")
	gitIn(t, source, "add", "src/a.ts")
	gitIn(t, source, "commit", "-qm", "add a.ts")
	// Reports written on Windows runners end every line with CRLF.
	report := "TN:\n" +
		"SF:" + source + "/src/other.ts\nDA:1,1\nDA:2,1\nend_of_record\n" +
		"SF:" + source + "/src/a.ts\nDA:1,1\nDA:2,1\nDA:3,1\nend_of_record\n"
	writeRepoFile(t, source, "coverage/lcov.info", strings.ReplaceAll(report, "\n", "\r\n"))
	assertLCOVFileCovered(t, source, "src/a.ts", "CRLF line endings must not break SF path matching")
}

func TestDiffCoverageScriptMatchesWindowsLCOVPaths(t *testing.T) {
	_, source := diffCoverageRepo(t, "web")
	gitIn(t, source, "checkout", "-q", "-b", "feature/lcov-windows")
	writeRepoFile(t, source, "src/a.ts", "export const a = 1\nexport const b = 2\nexport const c = 3\n")
	gitIn(t, source, "add", "src/a.ts")
	gitIn(t, source, "commit", "-qm", "add a.ts")
	writeRepoFile(t, source, "coverage/lcov.info", "TN:\n"+
		`SF:C:\work\web\src\other.ts`+"\nDA:1,1\nDA:2,1\nend_of_record\n"+
		`SF:C:\work\web\src\a.ts`+"\nDA:1,1\nDA:2,1\nDA:3,1\nend_of_record\n")
	assertLCOVFileCovered(t, source, "src/a.ts", "backslash-separated SF paths must match the diff's forward-slash path")
}
