package gitwork

import "testing"

// These cases pin behaviour that the isTestArtifactFileName /
// hasTestArtifactDir split must keep, but that the per-helper tests cannot see:
// the caller lower-cases the basename and extension before delegating, the
// junit/clover/coverage name patterns only apply to .xml files, source and
// config protections run before the file-name rules, and a bare directory path
// (no trailing file) still counts as test output.

func TestIsTestArtifactOrCachePathMatchesNamesCaseInsensitively(t *testing.T) {
	cases := []string{
		"LCOV.INFO",                 // exact basename table, upper case
		"build/Jacoco.Exec",         // exact basename table, mixed case
		"reports/Unit-COVERAGE.XML", // -coverage.xml pattern
		"reports/TEST-JUNIT-a.XML",  // junit pattern
		"Coverage-Final.Xml",        // coverage* prefix pattern
		"pkg/Foo.PHPUNIT.CACHE",     // .phpunit.cache suffix
		"pkg/Bar.PHPUNIT.RESULT.CACHE",
		"obj/main.GCDA", // counter extension, upper case
		"obj/main.Gcov",
	}
	for _, rel := range cases {
		if !IsTestArtifactOrCachePath(rel) {
			t.Errorf("IsTestArtifactOrCachePath(%q) = false, want true", rel)
		}
	}
}

func TestIsTestArtifactFileNameUsesLowerBaseForTable(t *testing.T) {
	// Callers pass the original base alongside its lower-cased form; the
	// basename table only holds lower-case keys, so the lower-cased lookup is
	// what makes upper-case report names match.
	if !isTestArtifactFileName("LCOV.INFO", "lcov.info", ".info") {
		t.Fatal(`isTestArtifactFileName("LCOV.INFO", "lcov.info", ".info") = false, want true`)
	}
	if !isTestArtifactFileName("Cobertura.XML", "cobertura.xml", ".xml") {
		t.Fatal(`isTestArtifactFileName("Cobertura.XML", "cobertura.xml", ".xml") = false, want true`)
	}
}

func TestIsTestArtifactOrCachePathNamePatternsRequireXML(t *testing.T) {
	// junit / clover / coverage* / *-coverage name patterns only mark .xml
	// files; documentation or tooling that merely mentions them stays.
	cases := []string{
		"docs/junit-migration.md",
		"docs/clover-setup.md",
		"coverage-guide.md",
		"scripts/unit-coverage.sh",
		"reports/junit.html",
	}
	for _, rel := range cases {
		if IsTestArtifactOrCachePath(rel) {
			t.Errorf("IsTestArtifactOrCachePath(%q) = true, want false", rel)
		}
	}
}

func TestIsTestArtifactOrCachePathSourceProtectionBeatsNameRules(t *testing.T) {
	// Source files whose names happen to match a file-name rule (here the
	// .coverage.* prefix) are still protected, regardless of extension case.
	cases := []string{
		"tools/.coverage.setup.py",
		"src/.coverage.helper.ts",
		"coverage/Main.GO",
		"htmlcov/Report.PY",
	}
	for _, rel := range cases {
		if IsTestArtifactOrCachePath(rel) {
			t.Errorf("IsTestArtifactOrCachePath(%q) = true, want false", rel)
		}
	}
}

func TestIsTestArtifactOrCachePathBareDirectory(t *testing.T) {
	// A directory entry without a trailing file (e.g. a collapsed untracked
	// directory) is test output when its own name is a test output directory.
	cases := []struct {
		rel  string
		want bool
	}{
		{"coverage", true},
		{"test-results", true},
		{"web/.nyc_output", true},
		{"./htmlcov", true},
		{"tests/_output", true},
		{"tests", false},
		{"_output", false},
		{"src", false},
	}
	for _, tc := range cases {
		if got := IsTestArtifactOrCachePath(tc.rel); got != tc.want {
			t.Errorf("IsTestArtifactOrCachePath(%q) = %v, want %v", tc.rel, got, tc.want)
		}
	}
}
