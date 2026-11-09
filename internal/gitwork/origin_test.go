package gitwork

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestRequireOriginURLsChecksEffectiveFetchAndPushDestinations(t *testing.T) {
	const expected = "https://gitlab.example.test/group/repo.git"
	for _, tc := range []struct {
		name   string
		config [][]string
		valid  bool
	}{
		{"unchanged", nil, true},
		{"same push URL", [][]string{{"remote.origin.pushurl", expected}}, true},
		{"other project fetch", [][]string{{"remote.origin.url", "https://gitlab.example.test/group/other.git"}}, false},
		{"other project push", [][]string{{"remote.origin.pushurl", "https://gitlab.example.test/group/other.git"}}, false},
		{"additional push", [][]string{{"--add", "remote.origin.pushurl", expected}, {"--add", "remote.origin.pushurl", "https://elsewhere.example.test/repo.git"}}, false},
		{"additional fetch", [][]string{{"--add", "remote.origin.url", "https://elsewhere.example.test/repo.git"}}, false},
		{"fetch rewrite", [][]string{{"url.https://elsewhere.example.test/.insteadOf", "https://gitlab.example.test/"}}, false},
		{"push rewrite", [][]string{{"url.https://gitlab.example.test/other/.pushInsteadOf", "https://gitlab.example.test/group/"}}, false},
		{"credential URL", [][]string{{"remote.origin.pushurl", "https://oauth2:untrusted-config-secret@gitlab.example.test/group/repo.git"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			run := func(args ...string) {
				t.Helper()
				if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("git failed: %v %s", err, out)
				}
			}
			run("init", "-q")
			run("remote", "add", "origin", expected)
			for _, config := range tc.config {
				run(append([]string{"config", "--local"}, config...)...)
			}
			err := RequireOriginURLs(context.Background(), dir, func(actual string) bool { return actual == expected })
			if (err == nil) != tc.valid {
				t.Fatalf("validation = %v, want valid=%t", err, tc.valid)
			}
			if err != nil && (strings.Contains(err.Error(), "untrusted-config-secret") || strings.Contains(err.Error(), "https://")) {
				t.Fatalf("unsafe diagnostic: %v", err)
			}
		})
	}
}

func TestRequireOriginURLsRejectsMissingOrigin(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := RequireOriginURLs(context.Background(), dir, func(string) bool { return true }); err == nil {
		t.Fatal("missing origin accepted")
	}
}
