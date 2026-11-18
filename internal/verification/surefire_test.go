package verification

import (
	"os"
	"path/filepath"
	"testing"
)

func writeReport(t *testing.T, root, module, name, body string) {
	t.Helper()
	dir := filepath.Join(root, module, "target", "surefire-reports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "TEST-"+name+".xml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseSurefireSumsModulesAndPasses(t *testing.T) {
	root := t.TempDir()
	writeReport(t, root, "core", "a.CoreTest", `<testsuite name="a.CoreTest" tests="10" failures="0" errors="0" skipped="1"></testsuite>`)
	writeReport(t, root, "api", "b.ApiTest", `<testsuite name="b.ApiTest" tests="5" failures="0" errors="0" skipped="0"></testsuite>`)
	summary, err := ParseSurefire(root)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Reports != 2 || summary.Tests != 15 || summary.Skipped != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if !summary.Pass() {
		t.Fatalf("green suite did not pass: %+v", summary)
	}
}

func TestParseSurefireFailsOnTraceAndErrors(t *testing.T) {
	root := t.TempDir()
	writeReport(t, root, "core", "a.CoreTest", `<testsuite name="a.CoreTest" tests="10" failures="2" errors="1" skipped="0"></testsuite>`)
	summary, err := ParseSurefire(root)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Pass() || summary.Failures != 2 || summary.Errors != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.FailuresText() == "" {
		t.Fatal("failing summary has no explanation")
	}
}

func TestParseSurefireWithoutReportsNeverPasses(t *testing.T) {
	summary, err := ParseSurefire(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Pass() || summary.Reports != 0 {
		t.Fatalf("empty repo passed: %+v", summary)
	}
}

func TestParseSurefireReportsUnreadableFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "m", "target", "surefire-reports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "TEST-bad.xml"), []byte("<testsuite"), 0o644); err != nil {
		t.Fatal(err)
	}
	summary, err := ParseSurefire(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Problems) != 1 || summary.Pass() {
		t.Fatalf("malformed report was not reported: %+v", summary)
	}
}

func TestParseSurefireResultsPerTest(t *testing.T) {
	root := t.TempDir()
	writeReport(t, root, "core", "a.CoreTest", `<testsuite name="a.CoreTest" tests="4" failures="1" errors="1" skipped="1">
  <testcase name="passes" classname="a.CoreTest"/>
  <testcase name="fails" classname="a.CoreTest"><failure message="boom"/></testcase>
  <testcase name="errors" classname="a.CoreTest"><error message="bad"/></testcase>
  <testcase name="skipped" classname="a.CoreTest"><skipped/></testcase>
</testsuite>`)
	summary, results, err := ParseSurefireResults(root)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Tests != 4 || len(results) != 4 {
		t.Fatalf("summary=%+v results=%v", summary, results)
	}
	want := map[string]bool{
		"a.CoreTest#passes": false, "a.CoreTest#fails": true,
		"a.CoreTest#errors": true, "a.CoreTest#skipped": false,
	}
	for name, failed := range want {
		if results[name] != failed {
			t.Fatalf("%s = %v, want %v", name, results[name], failed)
		}
	}
}
