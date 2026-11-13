package engine

import (
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

// diffCoverageMarker separates a diff_coverage gate's test output from the
// structural payload (the diff and the coverage report). Picking a marker the
// test tooling would not print keeps parsing deterministic even when the test
// command itself is chatty.
const diffCoverageMarker = "::loopforge-diff-coverage::"

// diffCoverageReportMarker separates the diff from the coverage report body so
// the split does not depend on each format's header shape.
const diffCoverageBaseBranchFallback = "${AGENTFLOW_BASE_BRANCH:-main}"

const diffCoverageReportMarker = "::loopforge-coverage-report::"

// isDiffCoverageGate reports whether the gate judges changed-line coverage
// instead of a bare command exit code.
func isDiffCoverageGate(gate domain.VerificationGate) bool {
	return strings.EqualFold(strings.TrimSpace(gate.Mode), domain.GateModeDiffCoverage)
}

// diffCoverageCommand appends the payload emitter to the configured commands.
// The commands still decide the exit code: a failing test run fails the gate
// as it would in a plain command gate, and the payload is only printed when
// they succeed (set -e + &&) so stale reports are never judged.
func diffCoverageCommand(gate domain.VerificationGate, commands string) (string, error) {
	spec := gate.Coverage
	if spec == nil {
		return "", fmt.Errorf("diff_coverage gate %s has no coverage spec", gate.ID)
	}
	base := strings.TrimSpace(spec.BaseBranch)
	if base == "" {
		// The repository target owns the base branch at run time; the
		// executor exports it so the gate needs no extra configuration.
		base = diffCoverageBaseBranchFallback
	}
	report := strings.TrimSpace(spec.ReportPath)
	if report == "" {
		return "", fmt.Errorf("diff_coverage gate %s has no coverage report path", gate.ID)
	}
	// The fallback needs the shell to expand AGENTFLOW_BASE_BRANCH, so it is
	// emitted unquoted; a configured branch is quoted.
	branch := shellSingleQuote(base)
	if base == diffCoverageBaseBranchFallback {
		branch = `"` + base + `"`
	}
	var b strings.Builder
	b.WriteString("set -e\n")
	b.WriteString(commands)
	fmt.Fprintf(&b, "\necho %s\n", shellSingleQuote(diffCoverageMarker))
	fmt.Fprintf(&b, "git fetch -q origin %s >/dev/null 2>&1 || true\n", branch)
	fmt.Fprintf(&b, "base_ref=$(git merge-base %s HEAD 2>/dev/null || git rev-parse %s 2>/dev/null)\n", branch, branch)
	b.WriteString("if [ -n \"$base_ref\" ]; then git --no-pager diff -U0 \"$base_ref\" -- . ; fi\n")
	fmt.Fprintf(&b, "echo %s\n", shellSingleQuote(diffCoverageReportMarker))
	fmt.Fprintf(&b, "if [ -f %s ]; then cat %s; fi\n", shellSingleQuote(report), shellSingleQuote(report))
	return b.String(), nil
}

// splitDiffCoverageOutput separates test output from the payload tail.
func splitDiffCoverageOutput(output string) (testOutput, payload string, ok bool) {
	idx := strings.Index(output, diffCoverageMarker)
	if idx < 0 {
		return output, "", false
	}
	return output[:idx], strings.TrimSpace(output[idx+len(diffCoverageMarker):]), true
}

// judgeDiffCoveragePayload parses the diff and the report from the payload and
// returns the verdict. A report that cannot be read is an infrastructure
// failure (wrong path, wrong format, command wrote nothing), never a pass.
func judgeDiffCoveragePayload(gate domain.VerificationGate, payload string) (domain.DiffCoverageVerdict, error) {
	spec := gate.Coverage
	// The report is emitted last and always follows its own marker; the diff
	// sits between the two markers. A payload without the diff marker means the
	// diff command printed nothing.
	payload = strings.TrimSpace(payload)
	reportText := ""
	diffText := payload
	if idx := strings.Index(payload, diffCoverageReportMarker); idx >= 0 {
		diffText = payload[:idx]
		reportText = strings.TrimSpace(payload[idx+len(diffCoverageReportMarker):])
	}
	diffText = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(diffText), diffCoverageMarker))
	if strings.TrimSpace(diffText) == "" {
		// An empty diff is legitimate (nothing changed since the base), and the
		// verdict is then simply not applicable.
		return domain.DiffCoverageVerdict{Applicable: false, Reason: "自基线分支以来没有改动行"}, nil
	}
	changed, err := domain.ParseUnifiedDiffChangedLines(strings.NewReader(diffText))
	if err != nil {
		return domain.DiffCoverageVerdict{}, fmt.Errorf("读取改动行: %w", err)
	}
	if strings.TrimSpace(reportText) == "" {
		return domain.DiffCoverageVerdict{}, fmt.Errorf("未找到覆盖率报告 %s（门禁命令需生成该文件）", spec.ReportPath)
	}
	report, err := domain.ParseCoverageReport(spec.ReportFormat, strings.NewReader(reportText))
	if err != nil {
		return domain.DiffCoverageVerdict{}, fmt.Errorf("解析覆盖率报告 %s: %w", spec.ReportPath, err)
	}
	return domain.JudgeDiffCoverage(*spec, changed, report), nil
}

// coverageVerdictSummary is the one-line telemetry message for a verdict.
func coverageVerdictSummary(verdict domain.DiffCoverageVerdict) string {
	if !verdict.Applicable {
		return "不适用（" + verdict.Reason + "）"
	}
	return fmt.Sprintf("%.0f%%（阈值 %.0f%%，%d/%d 行）", verdict.CoveragePct, verdict.ThresholdPct, verdict.CoveredLines, verdict.ChangedLines)
}

// diffCoverageTaskLine is the task-prompt sentence for a gate. The agent needs
// the threshold and the "new code needs tests" rule stated up front; leaving it
// to the failure feedback wastes a self-correction round.
func diffCoverageTaskLine(gate domain.VerificationGate) string {
	threshold, minLines := domain.GateCoverageSpec{}.CoverageDefaults()
	if gate.Coverage != nil {
		threshold, minLines = gate.Coverage.CoverageDefaults()
	}
	return fmt.Sprintf("%s必须通过：本次改动的可执行行测试覆盖率不低于 %.0f%%（改动少于 %d 行时不判定），新代码需要配套测试",
		gate.Name, threshold, minLines)
}
