package domain

import (
	"bufio"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

// ChangedLines maps a repository-relative file path to the ranges of lines
// this change added or modified. Deleted lines never appear: they have nothing
// left to cover.
type ChangedLines map[string][]LineRange

// LineRange is an inclusive span of 1-based line numbers.
type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// coverageSection is one file's coverage report entry: the executable line
// numbers with how many times tests hit them.
type coverageSection struct {
	Hits map[int]int64
}

// coverageReport is a parsed coverage report keyed by file path. Paths keep
// whatever the report used; matching against ChangedLines resolves prefixes.
type coverageReport map[string]*coverageSection

// CoverageFileResult is the per-file part of a diff-coverage verdict.
type CoverageFileResult struct {
	File            string      `json:"file"`
	ChangedLines    int         `json:"changedLines"`
	CoveredLines    int         `json:"coveredLines"`
	UncoveredRanges []LineRange `json:"uncoveredRanges,omitempty"`
}

// DiffCoverageVerdict is the outcome of judging a change's test coverage.
type DiffCoverageVerdict struct {
	// Applicable is false when the change touched too few executable lines
	// (or only excluded files); the gate then passes without spending a
	// self-correction round.
	Applicable   bool                 `json:"applicable"`
	Reason       string               `json:"reason,omitempty"`
	ThresholdPct float64              `json:"thresholdPct"`
	ChangedLines int                  `json:"changedLines"`
	CoveredLines int                  `json:"coveredLines"`
	CoveragePct  float64              `json:"coveragePct"`
	Files        []CoverageFileResult `json:"files,omitempty"`
	// UnreportedFiles are changed files the coverage report never mentions.
	// Their changed lines count as uncovered: a file whose tests never ran is
	// exactly the case this gate exists for.
	UnreportedFiles []string `json:"unreportedFiles,omitempty"`
}

// Passed reports whether the verdict clears the gate threshold.
func (v DiffCoverageVerdict) Passed() bool {
	return !v.Applicable || v.CoveragePct >= v.ThresholdPct
}

// Feedback renders the verdict as the text handed back to the agent. It names
// files and lines rather than repeating a truncated command log, because the
// agent's next step is writing tests for those lines.
func (v DiffCoverageVerdict) Feedback() string {
	if !v.Applicable {
		return "改动行覆盖率门禁不适用：" + v.Reason
	}
	var b strings.Builder
	fmt.Fprintf(&b, "改动行覆盖率 %.0f%%，低于阈值 %.0f%%（改动可执行行 %d，已被测试覆盖 %d）。",
		v.CoveragePct, v.ThresholdPct, v.ChangedLines, v.CoveredLines)
	if len(v.Files) > 0 || len(v.UnreportedFiles) > 0 {
		b.WriteString("请为以下未覆盖的改动行补充测试：")
		for _, file := range v.UnreportedFiles {
			fmt.Fprintf(&b, " %s（无覆盖率数据，整文件未被执行）；", file)
		}
		for _, file := range v.Files {
			if len(file.UncoveredRanges) == 0 {
				continue
			}
			fmt.Fprintf(&b, " %s %s；", file.File, FormatLineRanges(file.UncoveredRanges))
		}
	}
	b.WriteString("不要通过删除代码、扩大 exclude 或给这些行加忽略标记来规避。")
	return b.String()
}

// FormatLineRanges renders ranges as L12-L18, L40.
func FormatLineRanges(ranges []LineRange) string {
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		if r.Start == r.End {
			parts = append(parts, "L"+strconv.Itoa(r.Start))
			continue
		}
		parts = append(parts, "L"+strconv.Itoa(r.Start)+"-L"+strconv.Itoa(r.End))
	}
	return strings.Join(parts, ",")
}

// CoverageDefaults fills unset tuning knobs. Kept separate from judgement so
// the caller can show the effective thresholds in the task prompt.
func (s GateCoverageSpec) CoverageDefaults() (thresholdPct float64, minChangedLines int) {
	thresholdPct = DefaultGateCoverageThresholdPct
	if s.ThresholdPct != nil {
		thresholdPct = *s.ThresholdPct
	}
	minChangedLines = DefaultGateCoverageMinChangedLines
	if s.MinChangedLines != nil {
		minChangedLines = *s.MinChangedLines
	}
	return thresholdPct, minChangedLines
}

// defaultCoverageExcludes are paths that never count as "new code to test":
// tests themselves, generated code, vendored dependencies. Project-specific
// globs from the gate spec are applied on top.
var defaultCoverageExcludes = []string{
	"**/*_test.go",
	// JS/TS and Python test files: lcov/cobertura reports rarely list them,
	// and their extensions are in the report's language, so without these
	// a test the agent adds to satisfy the gate would count as uncovered.
	"**/*.test.*",
	"**/*.spec.*",
	"**/__tests__/**",
	"**/test_*.py",
	"**/*_test.py",
	"**/testdata/**",
	"**/*_gen.go",
	"**/*.pb.go",
	"**/*.pb.gw.go",
	"**/zz_generated*.go",
	"**/*.gen.go",
	"**/vendor/**",
	"**/node_modules/**",
	"**/*.min.js",
	"**/dist/**",
	"**/build/**",
}

// PathExcluded reports whether path is matched by the gate's exclude globs or
// the built-in generated/test defaults. Exclude wins over include, and the
// defaults cannot be re-included by accident: code we would not ask a human to
// test should not fail the gate.
func (s GateCoverageSpec) PathExcluded(file string) bool {
	file = normalizeCoveragePath(file)
	for _, glob := range defaultCoverageExcludes {
		if globMatch(glob, file) {
			return true
		}
	}
	for _, glob := range s.Exclude {
		if globMatch(glob, file) {
			return true
		}
	}
	if len(s.Include) == 0 {
		return false
	}
	for _, glob := range s.Include {
		if globMatch(glob, file) {
			return false
		}
	}
	return true
}

func normalizeCoveragePath(file string) string {
	file = strings.TrimSpace(strings.ReplaceAll(file, "\\", "/"))
	file = strings.TrimPrefix(file, "./")
	return path.Clean(file)
}

// globMatch supports "**" (zero or more path segments) plus the standard
// path.Match wildcards, which is what people write in coverage configs.
func globMatch(glob, file string) bool {
	globsSegs := splitGlobSegments(glob)
	fileSegs := strings.Split(normalizeCoveragePath(file), "/")
	var match func(gi, fi int) bool
	match = func(gi, fi int) bool {
		for gi < len(globsSegs) {
			seg := globsSegs[gi]
			if seg == "**" {
				// Try consuming zero segments, then one, and so on.
				for skip := fi; skip <= len(fileSegs); skip++ {
					if match(gi+1, skip) {
						return true
					}
				}
				return false
			}
			if fi >= len(fileSegs) {
				return false
			}
			ok, err := path.Match(seg, fileSegs[fi])
			if err != nil || !ok {
				return false
			}
			gi++
			fi++
		}
		return fi == len(fileSegs)
	}
	return match(0, 0)
}

func splitGlobSegments(glob string) []string {
	raw := strings.Split(normalizeCoveragePath(glob), "/")
	segs := make([]string, 0, len(raw))
	for _, seg := range raw {
		if seg == "" || seg == "." {
			continue
		}
		segs = append(segs, seg)
	}
	return segs
}

// JudgeDiffCoverage intersects the changed line ranges with a coverage report
// and decides whether the change meets the threshold. skipped lists changed
// files that the gate's exclude rules removed from consideration.
func JudgeDiffCoverage(spec GateCoverageSpec, changed ChangedLines, report coverageReport) DiffCoverageVerdict {
	thresholdPct, minChangedLines := spec.CoverageDefaults()
	verdict := DiffCoverageVerdict{ThresholdPct: thresholdPct}

	languageExts := reportLanguageExtensions(spec.ReportFormat, report)
	var judged []string
	for file := range changed {
		if spec.PathExcluded(file) {
			continue
		}
		if _, found := lookupCoverageSection(report, file); !found && !languageExts[coverageFileExt(file)] {
			// A file the report does not mention and that is not written in
			// the report's language (CHANGELOG.md, config.yaml, go.mod next
			// to a Go profile) is not code this report could ever cover.
			continue
		}
		judged = append(judged, file)
	}
	sort.Strings(judged)
	// A report can carry a file the change did not touch; only changed files
	// count here. Path matching happens inside the loop because reports from
	// Go use import paths while lcov/cobertura may use absolute paths.
	for _, file := range judged {
		ranges := changed[file]
		section, found := lookupCoverageSection(report, file)
		result := CoverageFileResult{File: file}
		var uncovered []LineRange
		for _, r := range ranges {
			for line := r.Start; line <= r.End; line++ {
				if !found {
					// No coverage data: the file never ran under tests.
					result.ChangedLines++
					uncovered = appendLine(uncovered, line)
					continue
				}
				hits, executable := section.Hits[line]
				if !executable {
					// Comments, blanks, declarations: not something tests can hit.
					continue
				}
				result.ChangedLines++
				if hits > 0 {
					result.CoveredLines++
				} else {
					uncovered = appendLine(uncovered, line)
				}
			}
		}
		if result.ChangedLines == 0 {
			continue
		}
		if !found {
			verdict.UnreportedFiles = append(verdict.UnreportedFiles, file)
		}
		result.UncoveredRanges = mergeLineRanges(uncovered)
		verdict.ChangedLines += result.ChangedLines
		verdict.CoveredLines += result.CoveredLines
		verdict.Files = append(verdict.Files, result)
	}
	if verdict.ChangedLines < minChangedLines {
		verdict.Applicable = false
		verdict.Reason = fmt.Sprintf("改动的可执行行只有 %d 行，少于 %d 行不判定", verdict.ChangedLines, minChangedLines)
		return verdict
	}
	verdict.Applicable = true
	verdict.CoveragePct = 100 * float64(verdict.CoveredLines) / float64(verdict.ChangedLines)
	return verdict
}

// reportLanguageExtensions is the set of file extensions a report speaks for.
// A Go coverprofile only ever covers *.go; lcov and cobertura are language
// neutral, so the extensions of the files they mention stand in for the
// language. Changed files outside this set are not judged when the report
// does not name them.
func reportLanguageExtensions(format string, report coverageReport) map[string]bool {
	exts := map[string]bool{}
	if strings.EqualFold(strings.TrimSpace(format), GateCoverageFormatGoCoverprofile) {
		exts[".go"] = true
		return exts
	}
	for name := range report {
		if ext := coverageFileExt(name); ext != "" {
			exts[ext] = true
		}
	}
	return exts
}

func coverageFileExt(file string) string {
	return strings.ToLower(path.Ext(normalizeCoveragePath(file)))
}

func appendLine(ranges []LineRange, line int) []LineRange {
	if n := len(ranges); n > 0 && ranges[n-1].End == line-1 {
		ranges[n-1].End = line
		return ranges
	}
	return append(ranges, LineRange{Start: line, End: line})
}

func mergeLineRanges(lines []LineRange) []LineRange {
	out := make([]LineRange, 0, len(lines))
	for _, r := range lines {
		out = appendLine(out, r.Start)
		if r.End != r.Start {
			for line := r.Start + 1; line <= r.End; line++ {
				out = appendLine(out, line)
			}
		}
	}
	return out
}

// lookupCoverageSection resolves a repository path against report paths. Go
// coverprofiles name packages by import path (agentflow/internal/domain), lcov
// and cobertura often name absolute paths. Exact match first, then a unique
// suffix match, which handles both without guessing when ambiguous.
func lookupCoverageSection(report coverageReport, file string) (*coverageSection, bool) {
	file = normalizeCoveragePath(file)
	if section, ok := report[file]; ok {
		return section, true
	}
	var match *coverageSection
	suffixes := 0
	for name, section := range report {
		if strings.HasSuffix(name, "/"+file) {
			match = section
			suffixes++
		}
	}
	if suffixes == 1 {
		return match, true
	}
	return nil, false
}

// ParseCoverageReport parses a report in one of GateCoverageReportFormats.
func ParseCoverageReport(format string, r io.Reader) (coverageReport, error) {
	report := coverageReport{}
	var err error
	switch strings.ToLower(strings.TrimSpace(format)) {
	case GateCoverageFormatGoCoverprofile:
		err = parseGoCoverprofile(r, report)
	case GateCoverageFormatLCOV:
		err = parseLCOV(r, report)
	case GateCoverageFormatCobertura:
		err = parseCobertura(r, report)
	default:
		return nil, fmt.Errorf("unsupported coverage report format %q", format)
	}
	if err != nil {
		return nil, err
	}
	if len(report) == 0 {
		return nil, fmt.Errorf("coverage report (%s) contained no file entries", format)
	}
	return report, nil
}

func sectionFor(report coverageReport, file string) *coverageSection {
	key := normalizeCoveragePath(file)
	section, ok := report[key]
	if !ok {
		section = &coverageSection{Hits: map[int]int64{}}
		report[key] = section
	}
	return section
}

// parseGoCoverprofile reads `go test -coverprofile` output:
//
//	mode: set
//	agentflow/internal/domain/foo.go:12.34,15.2 3 1
//
// Each block marks every line it spans as executable, which is how the
// profile models statements crossing lines.
func parseGoCoverprofile(r io.Reader, report coverageReport) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		file, rest, ok := strings.Cut(line, ":")
		if !ok {
			return fmt.Errorf("go coverprofile line %d: missing colon: %q", lineNo, clipCoverageLine(line))
		}
		// "file:startLine.col,endLine.col numStmts hitCount"
		startEnd, remainder, ok := strings.Cut(strings.TrimSpace(rest), " ")
		if !ok {
			return fmt.Errorf("go coverprofile line %d: missing statement count: %q", lineNo, clipCoverageLine(line))
		}
		startLine, endLine, err := parseGoCoverBlock(startEnd)
		if err != nil {
			return fmt.Errorf("go coverprofile line %d: %w", lineNo, err)
		}
		_, hitText, ok := strings.Cut(strings.TrimSpace(remainder), " ")
		if !ok {
			return fmt.Errorf("go coverprofile line %d: missing hit count: %q", lineNo, clipCoverageLine(line))
		}
		hits, err := strconv.ParseInt(strings.TrimSpace(hitText), 10, 64)
		if err != nil {
			return fmt.Errorf("go coverprofile line %d: invalid hit count %q", lineNo, strings.TrimSpace(hitText))
		}
		section := sectionFor(report, file)
		for line2 := startLine; line2 <= endLine; line2++ {
			if existing, ok := section.Hits[line2]; ok && existing > 0 {
				continue
			}
			section.Hits[line2] = hits
		}
	}
	return scanner.Err()
}

func clipCoverageLine(line string) string {
	if len(line) <= 120 {
		return line
	}
	return line[:120] + "…"
}

// parseGoCoverBlock parses "12.34,15.2" into line span 12..15.
func parseGoCoverBlock(block string) (int, int, error) {
	start, end, ok := strings.Cut(block, ",")
	if !ok {
		return 0, 0, fmt.Errorf("invalid block %q", block)
	}
	startLine, err := lineOfCoverPoint(start)
	if err != nil {
		return 0, 0, err
	}
	endLine, err := lineOfCoverPoint(end)
	if err != nil {
		return 0, 0, err
	}
	if endLine < startLine {
		endLine = startLine
	}
	return startLine, endLine, nil
}

func lineOfCoverPoint(point string) (int, error) {
	lineText, _, ok := strings.Cut(strings.TrimSpace(point), ".")
	if !ok {
		return 0, fmt.Errorf("invalid position %q", point)
	}
	line, err := strconv.Atoi(strings.TrimSpace(lineText))
	if err != nil || line <= 0 {
		return 0, fmt.Errorf("invalid line number %q", lineText)
	}
	return line, nil
}

// parseLCOV reads lcov/tracefile output: SF names the file, DA records each
// executable line with its hit count, end_of_record closes it.
func parseLCOV(r io.Reader, report coverageReport) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	current := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "SF:"):
			current = strings.TrimSpace(strings.TrimPrefix(line, "SF:"))
		case strings.HasPrefix(line, "DA:"):
			if current == "" {
				continue
			}
			fields := strings.Split(strings.TrimPrefix(line, "DA:"), ",")
			if len(fields) < 2 {
				continue
			}
			lineNo, err := strconv.Atoi(strings.TrimSpace(fields[0]))
			if err != nil || lineNo <= 0 {
				continue
			}
			hits, err := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64)
			if err != nil {
				continue
			}
			sectionFor(report, current).Hits[lineNo] = hits
		case line == "end_of_record":
			current = ""
		}
	}
	return scanner.Err()
}

// coberturaDocument covers the subset of the cobertura XML schema this gate
// reads: class filename plus the line numbers it reports.
type coberturaDocument struct {
	Classes []struct {
		Filename string `xml:"filename,attr"`
		Lines    []struct {
			Number int `xml:"number,attr"`
			Hits   int `xml:"hits,attr"`
		} `xml:"lines>line"`
	} `xml:"packages>package>classes>class"`
}

func parseCobertura(r io.Reader, report coverageReport) error {
	var doc coberturaDocument
	if err := xml.NewDecoder(r).Decode(&doc); err != nil {
		return fmt.Errorf("cobertura report: %w", err)
	}
	for _, class := range doc.Classes {
		if strings.TrimSpace(class.Filename) == "" {
			continue
		}
		section := sectionFor(report, class.Filename)
		for _, line := range class.Lines {
			if line.Number <= 0 {
				continue
			}
			section.Hits[line.Number] = int64(line.Hits)
		}
	}
	return nil
}

// ParseUnifiedDiffChangedLines reads `git diff -U0` output and returns the
// added/modified line ranges per repository-relative path. Deleted files and
// pure deletions contribute nothing. Renames keep the new path.
func ParseUnifiedDiffChangedLines(r io.Reader) (ChangedLines, error) {
	changed := ChangedLines{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var current string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "+++ "):
			target := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			if strings.HasPrefix(target, "b/") {
				target = strings.TrimPrefix(target, "b/")
			}
			if target == "/dev/null" {
				current = ""
				continue
			}
			current = normalizeCoveragePath(target)
		case strings.HasPrefix(line, "@@ "):
			if current == "" {
				continue
			}
			start, count, err := parseDiffHunkHeader(line)
			if err != nil {
				return nil, err
			}
			if count == 0 {
				// "0,0" marks a pure deletion at this position: no added line.
				continue
			}
			changed[current] = append(changed[current], LineRange{Start: start, End: start + count - 1})
		case strings.HasPrefix(line, "diff --git "):
			current = ""
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	for file, ranges := range changed {
		changed[file] = mergeLineRanges(ranges)
	}
	return changed, nil
}

// parseDiffHunkHeader reads the "+start,count" half of "@@ -a,b +c,d @@".
func parseDiffHunkHeader(header string) (int, int, error) {
	_, after, ok := strings.Cut(header, "+")
	if !ok {
		return 0, 0, fmt.Errorf("invalid hunk header %q", header)
	}
	span, _, _ := strings.Cut(after, " ")
	startText, countText, hasCount := strings.Cut(span, ",")
	start, err := strconv.Atoi(strings.TrimSpace(startText))
	if err != nil || start < 0 {
		return 0, 0, fmt.Errorf("invalid hunk header %q", header)
	}
	count := 1
	if hasCount {
		count, err = strconv.Atoi(strings.TrimSpace(countText))
		if err != nil || count < 0 {
			return 0, 0, fmt.Errorf("invalid hunk header %q", header)
		}
	}
	return start, count, nil
}
