package engine

import (
	"errors"
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
const diffCoverageReportMarker = "::loopforge-coverage-report::"

// diffCoverageEndMarker closes the payload. A CLI Runner only returns the tail
// of a command's output (internal/runner resultTailBytes); a payload missing
// either end marker was cut and must not be judged as if it were complete.
const diffCoverageEndMarker = "::loopforge-coverage-end::"

// diffCoverageErrorMarker carries a payload-side failure (no base branch) the
// shell detected, so the verdict names the cause instead of an exit status.
const diffCoverageErrorMarker = "::loopforge-coverage-error::"

const diffCoverageBaseBranchFallback = "${AGENTFLOW_BASE_BRANCH:-main}"

// diffCoverageTruncatedHint explains the only way a payload loses its markers.
const diffCoverageTruncatedHint = "门禁输出超过执行器回传上限（约 32 KiB）被截断，无法判定覆盖率；请让门禁命令只对改动涉及的包生成覆盖率报告，或减少测试命令的输出"

// isDiffCoverageGate reports whether the gate judges changed-line coverage
// instead of a bare command exit code.
func isDiffCoverageGate(gate domain.VerificationGate) bool {
	return strings.EqualFold(strings.TrimSpace(gate.Mode), domain.GateModeDiffCoverage)
}

// diffCoverageCommand appends the payload emitter to the configured commands.
// The commands still decide the exit code: a failing test run fails the gate
// as it would in a plain command gate, and the payload is only printed when
// they succeed (set -e) so stale reports are never judged. After the marker
// the emitter runs with set +e: a missing base branch is reported in the
// payload rather than as a bare exit status.
//
// The payload is kept small because a CLI Runner only returns the tail of the
// output: the diff is reduced to the file and hunk headers the parser reads,
// and a Go coverprofile or lcov report is reduced to the changed files.
// The emitter first returns to the repository root, so a command that ran
// `cd web && npm test` still yields root-relative paths for tracked and
// untracked files alike, and the report path is read relative to the root as
// documented. core.quotepath=off keeps non-ASCII file names unescaped.
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
	// emitted in double quotes; a configured branch is single-quoted.
	branch := shellSingleQuote(base)
	if base == diffCoverageBaseBranchFallback {
		branch = `"` + base + `"`
	}
	quotedReport := shellSingleQuote(report)
	var b strings.Builder
	b.WriteString("set -e\n")
	b.WriteString(commands)
	fmt.Fprintf(&b, "\necho %s\n", shellSingleQuote(diffCoverageMarker))
	b.WriteString("set +e\n")
	b.WriteString("lf_top=$(git rev-parse --show-toplevel 2>/dev/null) && [ -n \"$lf_top\" ] && cd \"$lf_top\"\n")
	fmt.Fprintf(&b, "lf_base=%s\n", branch)
	b.WriteString("git fetch -q origin \"$lf_base\" >/dev/null 2>&1\n")
	b.WriteString("lf_ref=$(git merge-base \"origin/$lf_base\" HEAD 2>/dev/null || git merge-base \"$lf_base\" HEAD 2>/dev/null || git rev-parse -q --verify \"$lf_base^{commit}\" 2>/dev/null)\n")
	b.WriteString("if [ -z \"$lf_ref\" ]; then\n")
	fmt.Fprintf(&b, "  echo %s\"找不到基线分支 $lf_base（本地和 origin 都没有），无法计算改动行\"\n", shellSingleQuote(diffCoverageErrorMarker))
	b.WriteString("else\n")
	// Files created by this node are not in the index yet (artifact
	// collection intent-adds them later), so `git diff <base>` alone would
	// miss exactly the new code this gate exists for. Diff them against
	// /dev/null without touching the index.
	b.WriteString("  lf_diff=$( { git -c core.quotepath=off --no-pager diff -U0 \"$lf_ref\" -- . ; git -c core.quotepath=off ls-files -z --others --exclude-standard -- . | xargs -0 -r -n1 git -c core.quotepath=off --no-pager diff --no-index -U0 -- /dev/null ; } 2>/dev/null | grep -E '^(diff --git |\\+\\+\\+ |@@ )')\n")
	b.WriteString("  printf '%s\\n' \"$lf_diff\"\n")
	b.WriteString("fi\n")
	fmt.Fprintf(&b, "echo %s\n", shellSingleQuote(diffCoverageReportMarker))
	switch strings.ToLower(strings.TrimSpace(spec.ReportFormat)) {
	case domain.GateCoverageFormatGoCoverprofile:
		fmt.Fprintf(&b, "if [ -f %s ]; then printf '%%s\\n' \"$lf_diff\" | awk %s - %s; fi\n", quotedReport, shellSingleQuote(goCoverprofileFilterAwk), quotedReport)
	case domain.GateCoverageFormatLCOV:
		fmt.Fprintf(&b, "if [ -f %s ]; then printf '%%s\\n' \"$lf_diff\" | awk %s - %s; fi\n", quotedReport, shellSingleQuote(lcovFilterAwk), quotedReport)
	default:
		// Cobertura XML is returned whole; see docs/diff-coverage-gate.md for
		// the CLI Runner size limit this implies.
		fmt.Fprintf(&b, "if [ -f %s ]; then cat %s; fi\n", quotedReport, quotedReport)
	}
	fmt.Fprintf(&b, "echo %s\n", shellSingleQuote(diffCoverageEndMarker))
	return b.String(), nil
}

// goCoverprofileFilterAwk keeps the mode line and the profile blocks of files
// named in the diff (first input). Import paths end with the repository path,
// so a suffix match is enough. When no block matches, one unrelated block is
// kept so the profile still parses; it cannot affect the verdict because only
// changed files are judged.
//
// git appends a TAB to "+++ b/<path>" when the path contains a space; it is
// stripped so the key matches the profile path.
const goCoverprofileFilterAwk = `NR == FNR { if (substr($0, 1, 6) == "+++ b/") { p = substr($0, 7); sub(/\t$/, "", p); want[p] = 1 }; next }
/^mode:/ { print; next }
{
  f = $0; sub(/:[^:]*$/, "", f); keep = 0
  for (w in want) if (f == w || (length(f) > length(w) && substr(f, length(f) - length(w)) == "/" w)) { keep = 1; break }
  if (keep) { print; kept = 1 } else if (first == "") first = $0
}
END { if (!kept && first != "") print first }`

// lcovFilterAwk keeps the SF:...end_of_record records of files named in the
// diff (first input), matched by path suffix like the Go filter, plus lines
// outside records. lcov is language neutral and the judge infers the report's
// languages from the extensions it lists, so for every other extension the
// first record is reduced to its SF line and first DA line: a new file of a
// language the report covers is still judged as unreported, while CHANGELOG.md
// and friends stay out, exactly as with the unfiltered report.
const lcovFilterAwk = `function flush(   g, n, ext, keep, w) {
  if (!inrec) return
  inrec = 0
  g = f; gsub(/\\/, "/", g); sub(/^\.\//, "", g)
  n = g; sub(/.*\//, "", n); ext = ""
  if (match(n, /\.[^.]*$/)) ext = tolower(substr(n, RSTART))
  keep = 0
  for (w in want) if (g == w || (length(g) > length(w) && substr(g, length(g) - length(w)) == "/" w)) { keep = 1; break }
  if (keep) { print rec; if (!ended) print "end_of_record"; seen[ext] = 1; return }
  if (!(ext in seen) && da != "") { print "SF:" f; print da; print "end_of_record"; seen[ext] = 1 }
}
NR == FNR { if (substr($0, 1, 6) == "+++ b/") { p = substr($0, 7); sub(/\t$/, "", p); want[p] = 1 }; next }
{ sub(/\r$/, "") }
/^[ \t]*SF:/ { flush(); f = $0; sub(/^[ \t]*SF:[ \t]*/, "", f); sub(/[ \t]+$/, "", f); inrec = 1; ended = 0; rec = $0; da = ""; next }
inrec {
  rec = rec "\n" $0
  if (da == "" && $0 ~ /^[ \t]*DA:/) da = $0
  if ($0 ~ /^[ \t]*end_of_record/) { ended = 1; flush() }
  next
}
{ print }
END { flush() }`

// splitDiffCoverageOutput separates test output from the payload tail.
func splitDiffCoverageOutput(output string) (testOutput, payload string, ok bool) {
	idx := strings.Index(output, diffCoverageMarker)
	if idx < 0 {
		return output, "", false
	}
	return output[:idx], strings.TrimSpace(output[idx+len(diffCoverageMarker):]), true
}

// extractDiffCoveragePayload returns the payload between the start and end
// markers. Output that carries only one of them was truncated by the
// executor, which is reported as such instead of "no coverage data".
func extractDiffCoveragePayload(output string) (string, error) {
	_, payload, found := splitDiffCoverageOutput(output)
	if !found {
		if strings.Contains(output, diffCoverageEndMarker) || strings.Contains(output, diffCoverageReportMarker) {
			return "", errors.New(diffCoverageTruncatedHint)
		}
		return "", errors.New("门禁未输出覆盖率数据")
	}
	end := strings.LastIndex(payload, diffCoverageEndMarker)
	if end < 0 {
		return "", errors.New(diffCoverageTruncatedHint)
	}
	return strings.TrimSpace(payload[:end]), nil
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
	if idx := strings.Index(payload, diffCoverageErrorMarker); idx >= 0 {
		reason, _, _ := strings.Cut(strings.TrimSpace(payload[idx+len(diffCoverageErrorMarker):]), "\n")
		return domain.DiffCoverageVerdict{}, errors.New(strings.TrimSpace(reason))
	}
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
