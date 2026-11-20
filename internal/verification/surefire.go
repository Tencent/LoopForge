package verification

import (
	"encoding/xml"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SurefireSummary is the aggregate of every surefire report under a repo.
// AegisOS decides pass/fail from these reports, never from Maven's exit code
// (VerificationRunner), so a red suite cannot pass because of a wrapper.
type SurefireSummary struct {
	Reports  int      `json:"reports"`
	Tests    int      `json:"tests"`
	Failures int      `json:"failures"`
	Errors   int      `json:"errors"`
	Skipped  int      `json:"skipped"`
	Problems []string `json:"problems,omitempty"`
}

// Pass reports whether the suite passed. No reports means no evidence of a
// run, which never counts as a pass.
func (s SurefireSummary) Pass() bool {
	return s.Reports > 0 && s.Failures == 0 && s.Errors == 0
}

// FailuresText explains a failing summary.
func (s SurefireSummary) FailuresText() string {
	if s.Reports == 0 {
		return "no surefire reports found"
	}
	return fmt.Sprintf("%d failing, %d errors across %d tests in %d report(s)", s.Failures, s.Errors, s.Tests, s.Reports)
}

type surefireSuite struct {
	XMLName  xml.Name           `xml:"testsuite"`
	Name     string             `xml:"name,attr"`
	Tests    int                `xml:"tests,attr"`
	Failures int                `xml:"failures,attr"`
	Errors   int                `xml:"errors,attr"`
	Skipped  int                `xml:"skipped,attr"`
	Cases    []surefireTestCase `xml:"testcase"`
}

type surefireTestCase struct {
	Name      string    `xml:"name,attr"`
	Classname string    `xml:"classname,attr"`
	Failure   *struct{} `xml:"failure"`
	Error     *struct{} `xml:"error"`
}

// ParseSurefire walks root for target/surefire-reports/TEST-*.xml and sums the
// suite attributes. Unreadable files are reported, never silently zeroed.
func ParseSurefire(root string) (SurefireSummary, error) {
	summary, _, err := ParseSurefireResults(root)
	return summary, err
}

// ParseSurefireResults also returns one boolean per test (true = failed or
// errored). The key is "class#method", which is what baseline diffing and
// flaky detection compare across runs.
func ParseSurefireResults(root string) (SurefireSummary, map[string]bool, error) {
	var summary SurefireSummary
	results := map[string]bool{}
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "TEST-") || !strings.HasSuffix(name, ".xml") {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) != "surefire-reports" {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return summary, results, err
	}
	sort.Strings(files)
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			summary.Problems = append(summary.Problems, fmt.Sprintf("%s: %v", file, err))
			continue
		}
		var suite surefireSuite
		if err := xml.Unmarshal(raw, &suite); err != nil {
			summary.Problems = append(summary.Problems, fmt.Sprintf("%s: %v", file, err))
			continue
		}
		summary.Reports++
		summary.Tests += suite.Tests
		summary.Failures += suite.Failures
		summary.Errors += suite.Errors
		summary.Skipped += suite.Skipped
		for _, test := range suite.Cases {
			key := test.Name
			if test.Classname != "" {
				key = test.Classname + "#" + test.Name
			}
			results[key] = test.Failure != nil || test.Error != nil
		}
	}
	return summary, results, nil
}
