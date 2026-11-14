package gitwork

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestIsTestArtifactFileName(t *testing.T) {
	cases := []struct {
		base string
		want bool
	}{
		{"junit.xml", true},
		{"JUnit.xml", true},
		{"jacoco.exec", true},
		{"main.gcda", true},
		{"main.GCNO", true},
		{"foo.phpunit.result.cache", true},
		{"foo.phpunit.cache", true},
		{".coverage.host.123", true},
		{"report-junit.xml", true},
		{"my-clover.xml", true},
		{"coverage-final.xml", true},
		{"unit_coverage.xml", true},
		{"unit-coverage.xml", true},
		{"pom.xml", false},
		{"README.md", false},
		{"package.json", false},
	}
	for _, tc := range cases {
		lowerBase := strings.ToLower(tc.base)
		ext := strings.ToLower(filepath.Ext(tc.base))
		if got := isTestArtifactFileName(tc.base, lowerBase, ext); got != tc.want {
			t.Errorf("isTestArtifactFileName(%q) = %v, want %v", tc.base, got, tc.want)
		}
	}
}

func TestHasTestArtifactDir(t *testing.T) {
	cases := []struct {
		rel  string
		want bool
	}{
		{"coverage/index.html", true},
		{"web/htmlcov/index.html", true},
		{"pkg/.phpunit.cache/x", true},
		{"tests/_output/report.html", true},
		{"a/tests/_output/b", true},
		{"_output/report.html", false},
		{"other/_output/report.html", false},
		{"Coverage/index.html", false},
		{"src/coverage.go", false},
		{"docs/guide.md", false},
	}
	for _, tc := range cases {
		if got := hasTestArtifactDir(tc.rel); got != tc.want {
			t.Errorf("hasTestArtifactDir(%q) = %v, want %v", tc.rel, got, tc.want)
		}
	}
}

func TestIsTestArtifactOrCachePathProtectionsWin(t *testing.T) {
	cases := []struct {
		rel  string
		want bool
	}{
		// Test configuration and source files are never artifacts, even
		// inside an artifact directory.
		{"coverage/phpunit.xml", false},
		{"coverage/phpunit.xml.dist", false},
		{"coverage/helper.go", false},
		{"tests/_output/fixture.py", false},
		// Non-protected files fall through to name and directory rules.
		{"./build/junit.xml", true},
		{"coverage/index.html", true},
		{"docs/guide.md", false},
	}
	for _, tc := range cases {
		if got := IsTestArtifactOrCachePath(tc.rel); got != tc.want {
			t.Errorf("IsTestArtifactOrCachePath(%q) = %v, want %v", tc.rel, got, tc.want)
		}
	}
}
